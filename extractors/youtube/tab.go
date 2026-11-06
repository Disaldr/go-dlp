package youtube

import (
	"context"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/hls"
	"github.com/Disaldr/go-dlp/media"
)

const (
	playlistKey = "youtube:tab"
	searchKey   = "youtube:search"
	// maxPages ограничивает число continuation-запросов.
	maxPages = 500
)

var tabMatcher = ex.MustMatcher(
	`^(?:https?://)?(?:(?:www|m|music)\.)?youtube\.com/playlist\?(?:.*&)?list=(?P<list>[0-9A-Za-z_-]+)`,
	`^(?:https?://)?(?:(?:www|m)\.)?youtube\.com/(?P<channel>@[\w.\-%]+|channel/UC[\w-]{22}|c/[^/?#]+|user/[^/?#]+)(?P<tab>/(?:videos|shorts|streams|featured|playlists))?/?(?:[?#]|$)`,
)

// Tab — плейлисты и вкладки каналов YouTube.
type Tab struct{}

// NewTab возвращает экстрактор плейлистов/каналов.
func NewTab() *Tab { return &Tab{} }

func (*Tab) Name() string           { return playlistKey }
func (*Tab) Description() string    { return "YouTube playlists and channel tabs" }
func (*Tab) Priority() int          { return 10 } // раньше видео: watch?list= обрабатывает Video
func (*Tab) Suitable(u string) bool { return tabMatcher.Match(u) }

func (t *Tab) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := tabMatcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	var pageURL, id string
	if list := g["list"]; list != "" {
		id = list
		pageURL = baseURL + "/playlist?list=" + list
	} else {
		tab := g["tab"]
		if tab == "" || tab == "/featured" {
			tab = "/videos"
		}
		id = g["channel"]
		pageURL = baseURL + "/" + g["channel"] + tab
	}
	page, err := env.GetText(ctx, pageURL, consentHeaders)
	if err != nil {
		return nil, ex.Wrap(playlistKey, id, err, "unable to download page")
	}
	data, ok := ex.JSONAfter(page, "var ytInitialData = ", `window["ytInitialData"] = `, "ytInitialData = ")
	if !ok {
		return nil, ex.Errorf(playlistKey, id, ex.ErrUnavailable, "unable to find ytInitialData")
	}
	if alert := alertText(data); alert != "" && len(ex.FindAll(data, "playlistVideoRenderer")) == 0 && len(ex.FindAll(data, "videoRenderer")) == 0 && len(ex.FindAll(data, "richItemRenderer")) == 0 {
		return nil, ex.Errorf(playlistKey, id, ex.ErrUnavailable, "%s", alert)
	}
	ytcfg, _ := ex.JSONAfter(page, "ytcfg.set(")

	info := &media.Info{
		Kind:       media.KindPlaylist,
		ID:         id,
		WebpageURL: pageURL,
		Title: ex.FirstStr(data,
			ex.P("metadata", "playlistMetadataRenderer", "title"),
			ex.P("metadata", "channelMetadataRenderer", "title"),
			ex.P("microformat", "microformatDataRenderer", "title"),
		),
		Description: ex.FirstStr(data,
			ex.P("metadata", "playlistMetadataRenderer", "description"),
			ex.P("metadata", "channelMetadataRenderer", "description"),
		),
	}
	if info.Title == "" {
		info.Title = ex.HTMLTitle(page)
	}
	if cid := ex.Str(data, "metadata", "channelMetadataRenderer", "externalId"); cid != "" {
		info.ChannelID = cid
		info.Channel = info.Title
		info.ChannelURL = baseURL + "/channel/" + cid
	}
	info.Entries, err = collectEntries(ctx, env, data, ytcfg, env.MaxEntries)
	if err != nil {
		if len(info.Entries) == 0 {
			return nil, ex.Wrap(playlistKey, id, err, "unable to fetch entries")
		}
		// часть элементов собрана, но пагинация прервалась — не молчим
		env.Warn("youtube: playlist pagination stopped early", "id", id, "entries", len(info.Entries), "err", err)
	}
	info.PlaylistCount = len(info.Entries)
	return info, nil
}

func alertText(data any) string {
	for _, a := range ex.FindAll(data, "alertRenderer") {
		if s := ex.FirstStr(a, ex.P("text", "simpleText"), ex.P("text", "runs", 0, "text")); s != "" {
			return s
		}
	}
	return ""
}

// entriesFrom извлекает видео из любых известных рендереров.
func entriesFrom(data any, seen map[string]bool) []*media.Info {
	var out []*media.Info
	add := func(id, title string, dur float64, uploader string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		e := media.NewURL(baseURL+"/watch?v="+id, ieKey)
		e.ID, e.Title, e.Duration, e.Uploader = id, title, dur, uploader
		out = append(out, e)
	}
	text := func(v any, k string) string {
		return ex.FirstStr(v, ex.P(k, "simpleText"), ex.P(k, "runs", 0, "text"), ex.P(k, "content"))
	}
	// Обходим все объекты по порядку, ловя рендереры по ключу.
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case []any:
			for _, v := range t {
				walk(v)
			}
		case map[string]any:
			matched := false
			for _, k := range []string{"playlistVideoRenderer", "videoRenderer", "gridVideoRenderer", "compactVideoRenderer"} {
				if r, ok := t[k].(map[string]any); ok {
					dur := float64(ex.Int(r, "lengthSeconds"))
					if dur == 0 {
						dur = ex.ParseDuration(text(r, "lengthText"))
					}
					add(ex.Str(r, "videoId"), text(r, "title"), dur, ex.FirstStr(r, ex.P("shortBylineText", "runs", 0, "text"), ex.P("ownerText", "runs", 0, "text")))
					matched = true
				}
			}
			if r, ok := t["reelItemRenderer"].(map[string]any); ok {
				add(ex.Str(r, "videoId"), text(r, "headline"), 0, "")
				matched = true
			}
			if r, ok := t["shortsLockupViewModel"].(map[string]any); ok {
				vid := ex.Str(r, "onTap", "innertubeCommand", "reelWatchEndpoint", "videoId")
				add(vid, ex.Str(r, "overlayMetadata", "primaryText", "content"), 0, "")
				matched = true
			}
			if r, ok := t["lockupViewModel"].(map[string]any); ok {
				if ex.Str(r, "contentType") == "LOCKUP_CONTENT_TYPE_VIDEO" {
					add(ex.Str(r, "contentId"), ex.Str(r, "metadata", "lockupMetadataViewModel", "title", "content"), 0, "")
				}
				matched = true
			}
			if matched {
				return
			}
			// сохраняем порядок: contents/items — массивы, порядок ключей для них не важен
			for _, k := range sortedKeys(t) {
				walk(t[k])
			}
		}
	}
	walk(data)
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

type continuation struct {
	token  string
	apiURL string
}

// videoRendererKeys — ключи рендереров, которые entriesFrom считает видео.
var videoRendererKeys = []string{
	"playlistVideoRenderer", "videoRenderer", "gridVideoRenderer",
	"compactVideoRenderer", "richItemRenderer", "reelItemRenderer",
	"shortsLockupViewModel", "lockupViewModel",
}

// isVideoItem — элемент списка с одним из видео-рендереров.
func isVideoItem(m map[string]any) bool {
	for _, k := range videoRendererKeys {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

// continuationFrom извлекает токен continuation из ноды continuationItemRenderer
// (через continuationEndpoint) или continuationItemViewModel (через
// continuationCommand, новый layout плейлистов).
func continuationFrom(node any) *continuation {
	ep := ex.Map(node, "continuationEndpoint")
	cc := ex.Map(node, "continuationCommand")
	tok := ex.Str(ep, "continuationCommand", "token")
	if tok == "" {
		tok = ex.Str(cc, "innertubeCommand", "continuationCommand", "token")
	}
	if tok == "" {
		tok = ex.Str(cc, "token")
	}
	if tok == "" {
		// формат commandExecutorCommand.commands[].continuationCommand
		for _, cmd := range ex.Slice(ep, "commandExecutorCommand", "commands") {
			if t := ex.Str(cmd, "continuationCommand", "token"); t != "" {
				tok = t
			}
		}
	}
	if tok == "" {
		return nil
	}
	api := ex.Str(ep, "commandMetadata", "webCommandMetadata", "apiUrl")
	if api == "" {
		api = ex.Str(cc, "innertubeCommand", "commandMetadata", "webCommandMetadata", "apiUrl")
	}
	if api == "" {
		api = "/youtubei/v1/browse"
		if ex.Str(ep, "continuationCommand", "request") == "CONTINUATION_REQUEST_TYPE_SEARCH" ||
			ex.Str(cc, "innertubeCommand", "continuationCommand", "request") == "CONTINUATION_REQUEST_TYPE_SEARCH" {
			api = "/youtubei/v1/search"
		}
	}
	return &continuation{token: tok, apiURL: api}
}

// findContinuation ищет continuation списка видео: токен, лежащий в том же
// массиве, что и видео-рендереры. Привязка к массиву видео исключает
// continuation посторонних секций страницы — в первую очередь комментариев,
// чей continuationItemRenderer раньше ошибочно подхватывался первым (из-за чего
// плейлист обрывался на первой странице). Поддерживаются оба формата ноды:
// continuationItemRenderer (классический) и continuationItemViewModel (новый).
func findContinuation(data any) *continuation {
	var found *continuation
	var walk func(any)
	walk = func(x any) {
		if found != nil {
			return
		}
		switch t := x.(type) {
		case []any:
			hasVideo := false
			var contNode any
			for _, v := range t {
				m, ok := v.(map[string]any)
				if !ok {
					continue
				}
				if isVideoItem(m) {
					hasVideo = true
				}
				if n, ok := m["continuationItemRenderer"]; ok {
					contNode = n
				} else if n, ok := m["continuationItemViewModel"]; ok {
					contNode = n
				}
			}
			if hasVideo && contNode != nil {
				if c := continuationFrom(contNode); c != nil {
					found = c
					return
				}
			}
			for _, v := range t {
				walk(v)
				if found != nil {
					return
				}
			}
		case map[string]any:
			for _, k := range sortedKeys(t) {
				walk(t[k])
				if found != nil {
					return
				}
			}
		}
	}
	walk(data)
	return found
}

// collectEntries собирает видео со страницы и всех continuation-страниц.
// limit > 0 — остановиться после limit видео.
func collectEntries(ctx context.Context, env *ex.Env, data, ytcfg any, limit int) ([]*media.Info, error) {
	seen := map[string]bool{}
	entries := entriesFrom(data, seen)
	ictx := ex.Map(ytcfg, "INNERTUBE_CONTEXT")
	if ictx == nil {
		ictx = Clients["web"].context()
		if cl, ok := ictx["client"].(map[string]any); ok {
			delete(cl, "userAgent")
		}
	}
	visitor := ex.Str(ytcfg, "VISITOR_DATA")
	version := ex.Str(ytcfg, "INNERTUBE_CLIENT_VERSION")
	if version == "" {
		version = Clients["web"].Version
	}
	cont := findContinuation(data)
	for page := 0; cont != nil && page < maxPages; page++ {
		if limit > 0 && len(entries) >= limit {
			break
		}
		headers := map[string]string{
			"X-YouTube-Client-Name":    "1",
			"X-YouTube-Client-Version": version,
			"Origin":                   baseURL,
		}
		if visitor != "" {
			headers["X-Goog-Visitor-Id"] = visitor
		}
		var resp any
		body := map[string]any{"context": ictx, "continuation": cont.token}
		if err := env.PostJSON(ctx, baseURL+cont.apiURL+"?prettyPrint=false", headers, body, &resp); err != nil {
			return entries, err
		}
		// берём только приложенные элементы, чтобы не ловить рекомендации
		var items any = resp
		if ci := ex.FindAll(resp, "continuationItems"); len(ci) > 0 {
			items = ci
		}
		got := entriesFrom(items, seen)
		entries = append(entries, got...)
		next := findContinuation(items)
		if next == nil || len(got) == 0 && next.token == cont.token {
			break
		}
		cont = next
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

// --- ytsearch ---

var reSearch = regexp.MustCompile(`^ytsearch(\d*|all):(.+)$`)

// Search — поиск ytsearch[N|all]:запрос.
type Search struct{}

// NewSearch возвращает экстрактор поиска.
func NewSearch() *Search { return &Search{} }

func (*Search) Name() string           { return searchKey }
func (*Search) Description() string    { return `YouTube search, "ytsearch[N|all]:query"` }
func (*Search) Suitable(u string) bool { return reSearch.MatchString(u) }

func (s *Search) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	m := reSearch.FindStringSubmatch(rawURL)
	if m == nil {
		return nil, ex.ErrUnsupportedURL
	}
	n := 1
	switch m[1] {
	case "":
	case "all":
		n = 0
	default:
		n, _ = strconv.Atoi(m[1])
	}
	if n == 0 {
		n = 1000
	}
	query := strings.TrimSpace(m[2])
	pageURL := baseURL + "/results?search_query=" + url.QueryEscape(query)
	page, err := env.GetText(ctx, pageURL, consentHeaders)
	if err != nil {
		return nil, ex.Wrap(searchKey, query, err, "unable to download search page")
	}
	data, ok := ex.JSONAfter(page, "var ytInitialData = ", `window["ytInitialData"] = `, "ytInitialData = ")
	if !ok {
		return nil, ex.Errorf(searchKey, query, ex.ErrUnavailable, "unable to find ytInitialData")
	}
	ytcfg, _ := ex.JSONAfter(page, "ytcfg.set(")
	entries, err := collectEntries(ctx, env, data, ytcfg, n)
	if err != nil && len(entries) == 0 {
		return nil, ex.Wrap(searchKey, query, err, "search failed")
	}
	return &media.Info{
		Kind:          media.KindPlaylist,
		ID:            query,
		Title:         query,
		WebpageURL:    pageURL,
		Entries:       entries,
		PlaylistCount: len(entries),
	}, nil
}

func parseHLS(body, manifest string) []*media.Format {
	return hls.ParseMaster(body, manifest, "hls")
}
