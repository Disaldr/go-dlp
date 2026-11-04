// Package vk — экстрактор видео ВКонтакте (vk.com, vkvideo.ru, vk.ru):
// видео и клипы, embed (video_ext.php), встраивания сторонних плееров.
//
// JS-челлендж антибот-защиты VK (challenge.html) не решается: при нём
// экстрактор возвращает ошибку с предложением передать cookies.
package vk

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/text/encoding/charmap"

	"github.com/Disaldr/go-dlp/dash"
	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/hls"
	"github.com/Disaldr/go-dlp/media"
)

const ieKey = "vk"

const host = `(?:(?:m|new|vksport)\.)?vk(?:(?:video)?\.ru|\.com)`

var matcher = ex.MustMatcher(
	`^https?://`+host+`/video_ext\.php\?(?P<embed>.*?\boid=(?P<oid>-?\d+).*?\bid=(?P<id>\d+).*)`,
	`^https?://`+host+`/(?:.+?\?.*?z=)?(?:video|clip)(?P<videoid>-?\d+_\d+)(?:.*\blist=(?P<list>[\da-f]+|ln-[\da-zA-Z]+))?`,
)

// VK — экстрактор видео VK.
type VK struct{}

// New возвращает экстрактор VK.
func New() *VK { return &VK{} }

func (*VK) Name() string           { return ieKey }
func (*VK) Description() string    { return "VK videos and clips (vk.com, vkvideo.ru)" }
func (*VK) Suitable(u string) bool { return matcher.Match(u) }

// errChallenge — VK потребовал пройти JS-проверку.
var errChallenge = errors.New("VK requested an anti-bot JS challenge, which is not supported; pass --cookies from a logged-in browser session")

// decodeBody перекодирует ответ VK (часто windows-1251) в UTF-8.
func decodeBody(h http.Header, body []byte) string {
	_, params, _ := mime.ParseMediaType(h.Get("Content-Type"))
	if cs := strings.ToLower(params["charset"]); cs == "windows-1251" || cs == "cp1251" {
		if b, err := charmap.Windows1251.NewDecoder().Bytes(body); err == nil {
			return string(b)
		}
	}
	return string(body)
}

func (v *VK) fetch(ctx context.Context, env *ex.Env, req ex.Request) (string, error) {
	r, err := env.Fetch(ctx, req)
	if err != nil {
		return "", err
	}
	if pu, err := url.Parse(r.URL); err == nil && pu.Path == "/challenge.html" {
		return "", errChallenge
	}
	return decodeBody(r.Header, r.Body), nil
}

func (v *VK) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := matcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	var (
		id       = g["videoid"]
		infoPage string
		player   any
		mvData   any
	)
	if id != "" {
		var err error
		if infoPage, mvData, player, err = v.alVideo(ctx, env, id, g["list"]); err != nil {
			return nil, err
		}
	} else {
		id = g["oid"] + "_" + g["id"]
		page, err := v.fetch(ctx, env, ex.Request{URL: "https://vk.com/video_ext.php?" + g["embed"]})
		if err != nil {
			return nil, wrap(id, err)
		}
		infoPage = page
		if err := pageError(id, page); err != nil {
			return nil, err
		}
		player, _ = ex.JSONAfter(page, "var playerParams = ", "playerParams = ")
		if ex.Map(player, "params", 0) == nil && embeddedURL(env, page) == "" {
			// новый SPA-embed не содержит playerParams — берём данные как для обычной ссылки
			if infoPage, mvData, player, err = v.alVideo(ctx, env, id, ""); err != nil {
				return nil, err
			}
		}
	}
	return v.build(ctx, env, id, infoPage, mvData, player)
}

// alVideo запрашивает al_video.php?act=show: HTML страницы видео, mvData и параметры плеера.
func (v *VK) alVideo(ctx context.Context, env *ex.Env, id, list string) (infoPage string, mvData, player any, err error) {
	{
		form := url.Values{"act": {"show"}, "video": {id}, "al": {"1"}}
		if list != "" {
			form.Set("list", list)
		}
		body, err := v.fetch(ctx, env, ex.Request{
			Method: http.MethodPost,
			URL:    "https://vk.com/al_video.php",
			Body:   strings.NewReader(form.Encode()),
			Headers: map[string]string{
				"Content-Type":     "application/x-www-form-urlencoded",
				"Referer":          "https://vk.com/al_video.php",
				"X-Requested-With": "XMLHttpRequest",
			},
		})
		if err != nil {
			return "", nil, nil, wrap(id, err)
		}
		var resp struct {
			Payload []json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal([]byte(body), &resp); err != nil || len(resp.Payload) < 2 {
			return "", nil, nil, ex.Errorf(ieKey, id, ex.ErrUnavailable, "unexpected al_video response")
		}
		var code string
		_ = json.Unmarshal(resp.Payload[0], &code)
		if code == "" {
			var n int
			_ = json.Unmarshal(resp.Payload[0], &n)
			code = strconv.Itoa(n)
		}
		var payload []any
		_ = json.Unmarshal(resp.Payload[1], &payload)
		switch code {
		case "3":
			return "", nil, nil, ex.Errorf(ieKey, id, ex.ErrLoginRequired, "login required (pass --cookies)")
		case "8":
			msg, _ := ex.Get(payload, 0).(string)
			return "", nil, nil, ex.Errorf(ieKey, id, ex.ErrUnavailable, "%s", ex.CleanHTML(strings.Trim(msg, "<!>")))
		}
		opts := ex.Get(payload, -1)
		var mv, pl any
		if m := ex.Map(opts, "mvData"); m != nil {
			mv = m
		}
		if p := ex.Map(opts, "player"); p != nil {
			pl = p
		}
		return ex.Str(payload, 1), mv, pl, nil
	}
}

func (v *VK) build(ctx context.Context, env *ex.Env, id, infoPage string, mvData, player any) (*media.Info, error) {
	params := ex.Map(player, "params", 0)
	if params == nil {
		// видео стороннего плеера (YouTube, Rutube, OK, ...) — отдаём его экстрактору
		if u := embeddedURL(env, infoPage); u != "" {
			return media.NewURL(u, ""), nil
		}
		if err := pageError(id, infoPage); err != nil {
			return nil, err
		}
		return nil, ex.Errorf(ieKey, id, ex.ErrNoFormats, "no player data")
	}

	info := &media.Info{
		Kind:        media.KindVideo,
		ID:          id,
		Title:       html.UnescapeString(ex.FirstStr(mvData, ex.P("title"))),
		Description: ex.CleanHTML(ex.Str(mvData, "desc")),
		Duration:    ex.Float(mvData, "duration"),
		LikeCount:   ex.Int(mvData, "likes"),
		Thumbnail:   ex.Str(params, "jpg"),
		Uploader:    html.UnescapeString(ex.Str(params, "md_author")),
		UploaderID:  ex.FirstStr(params, ex.P("author_id"), ex.P("authorId")),
		IsLive:      ex.Int(params, "live") == 2,
		Timestamp:   ex.Int(params, "date"),
		WebpageURL:  "https://vk.com/video" + id,
	}
	if info.UploaderID == "" {
		if n := ex.Int(params, "author_id"); n != 0 {
			info.UploaderID = strconv.FormatInt(n, 10)
		}
	}
	if info.Title == "" {
		info.Title = html.UnescapeString(ex.Str(params, "md_title"))
	}
	if info.Description == "" {
		info.Description = ex.CleanHTML(ex.Str(params, "description"))
	}
	if info.Duration == 0 {
		info.Duration = ex.Float(params, "duration")
	}
	info.UploadDate = ex.UnixToDate(info.Timestamp)

	for key, raw := range params {
		u, _ := raw.(string)
		if !strings.HasPrefix(u, "http") && !strings.HasPrefix(u, "//") {
			continue
		}
		if strings.HasPrefix(u, "//") {
			u = "https:" + u
		}
		switch {
		case strings.HasPrefix(key, "url") || strings.HasPrefix(key, "cache") || key == "extra_data" || key == "live_mp4" || key == "postlive_mp4":
			f := &media.Format{FormatID: key, URL: u, Ext: "mp4", Preference: 1}
			if m := reHeight.FindStringSubmatch(key); m != nil {
				f.Height, _ = strconv.Atoi(m[1])
			}
			if strings.HasPrefix(key, "cache") {
				f.Preference = 2 // кэширующие CDN обычно быстрее
			}
			info.Formats = append(info.Formats, f)
		case strings.HasPrefix(key, "hls") && key != "hls_live_playback":
			if body, err := env.GetText(ctx, u, nil); err == nil {
				info.Formats = append(info.Formats, hls.ParseMaster(body, u, key)...)
			} else {
				env.Warn("vk: HLS manifest", "key", key, "err", err)
			}
		case strings.HasPrefix(key, "dash") && key != "dash_live_playback" && key != "dash_uni":
			if r, err := env.Fetch(ctx, ex.Request{URL: u}); err == nil {
				if fs, err := dash.Parse(r.Body, r.URL, key); err == nil {
					info.Formats = append(info.Formats, fs...)
				}
			} else {
				env.Warn("vk: DASH manifest", "key", key, "err", err)
			}
		}
	}
	for _, s := range ex.Slice(params, "subs") {
		u := ex.Str(s, "url")
		if u == "" {
			continue
		}
		lang := ex.Str(s, "lang")
		if lang == "" {
			lang = "en"
		}
		ext := "srt"
		if t := ex.Str(s, "title"); strings.Contains(t, ".") {
			ext = t[strings.LastIndexByte(t, '.')+1:]
		}
		if info.Subtitles == nil {
			info.Subtitles = map[string][]media.Subtitle{}
		}
		info.Subtitles[lang] = append(info.Subtitles[lang], media.Subtitle{URL: u, Ext: ext})
	}
	if len(info.Formats) == 0 {
		return nil, ex.Errorf(ieKey, id, ex.ErrNoFormats, "no formats found")
	}
	return info, nil
}

var reHeight = regexp.MustCompile(`^(?:url|cache)(\d+)`)

func wrap(id string, err error) error {
	if errors.Is(err, errChallenge) {
		return ex.Errorf(ieKey, id, ex.ErrLoginRequired, "%v", err)
	}
	return ex.Wrap(ieKey, id, err, "request failed")
}

var pageErrors = []struct {
	re   *regexp.Regexp
	kind error
	msg  string
}{
	{regexp.MustCompile(`(?:изъята из публичного доступа|removed from public access by request of the copyright holder)`), ex.ErrUnavailable, "removed by rightholder complaint"},
	{regexp.MustCompile(`<!>Please log in or <|act=security_check`), ex.ErrLoginRequired, "available only for registered users (pass --cookies)"},
	{regexp.MustCompile(`<!>Unknown error`), ex.ErrUnavailable, "video does not exist"},
	{regexp.MustCompile(`Видео временно недоступно`), ex.ErrUnavailable, "temporarily unavailable"},
	{regexp.MustCompile(`<!>Access denied`), ex.ErrLoginRequired, "access denied"},
	{regexp.MustCompile(`(?:автор был заблокирован|its author has been blocked)`), ex.ErrUnavailable, "author has been blocked"},
	{regexp.MustCompile(`it has been deleted`), ex.ErrUnavailable, "video has been deleted"},
	{regexp.MustCompile(`is not available in your region`), ex.ErrGeoRestricted, "not available in your region"},
}

var reStub = regexp.MustCompile(`(?s)<div[^>]+(?:class="video_layer_message"|id="video_ext_msg")[^>]*>(.+?)</div>`)

func pageError(id, page string) error {
	for _, e := range pageErrors {
		if e.re.MatchString(page) {
			return ex.Errorf(ieKey, id, e.kind, "%s", e.msg)
		}
	}
	if m := reStub.FindStringSubmatch(page); m != nil {
		return ex.Errorf(ieKey, id, ex.ErrUnavailable, "%s", ex.CleanHTML(m[1]))
	}
	return nil
}

var reIframe = regexp.MustCompile(`(?i)<iframe[^>]+src=\\?["']([^"'\\]+)`)

// embeddedURL ищет в странице плеер, который умеет обработать другой экстрактор.
func embeddedURL(env *ex.Env, page string) string {
	page = strings.ReplaceAll(page, `\/`, "/")
	for _, m := range reIframe.FindAllStringSubmatch(page, -1) {
		u := html.UnescapeString(m[1])
		if strings.HasPrefix(u, "//") {
			u = "https:" + u
		}
		if _, ok := env.FindEmbedded(u, ieKey); ok {
			return u
		}
	}
	return ""
}

// --- списки видео ---

const videosKey = "vk:uservideos"

var videosMatcher = ex.MustMatcher(
	`^https?://(?:(?:m|new)\.)?vk(?:video\.ru|\.com/video)/playlist/(?P<playlist>-?\d+_-?\d+)`,
	`^https?://(?:(?:m|new)\.)?vk(?:video\.ru|\.com/video)/(?P<handle>@[^/?#]+)(?:/all)?/?(?:[?#]|$)`,
)

// maxVideoPages ограничивает число страниц списка.
const maxVideoPages = 100

// Videos — видео сообщества/пользователя (vk.com/video/@handle) и плейлисты.
type Videos struct{}

// NewVideos возвращает экстрактор списков видео VK.
func NewVideos() *Videos { return &Videos{} }

func (*Videos) Name() string        { return videosKey }
func (*Videos) Description() string { return "VK user/community videos and playlists" }
func (*Videos) Priority() int       { return 5 }
func (*Videos) Suitable(u string) bool {
	return videosMatcher.Match(u) && !strings.Contains(u, "z=video")
}

var reTitlePanel = regexp.MustCompile(`(?s)class="[^"]*VideoInfoPanel__title[^"]*"[^>]*>(.*?)</`)

func (vs *Videos) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := videosMatcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	v := &VK{}
	var oid, section, title string
	if pl := g["playlist"]; pl != "" {
		var num string
		oid, num, _ = strings.Cut(pl, "_")
		section = "playlist_" + num
	} else {
		page, err := v.fetch(ctx, env, ex.Request{URL: rawURL})
		if err != nil {
			env.Debug("vk: videos page unavailable, resolving via profile page", "err", err)
		}
		cur, _ := ex.JSONAfter(page, "var newCur =", "var newCur=")
		oid = strconv.FormatInt(ex.Int(cur, "oid"), 10)
		if oid == "0" {
			// vk.com/video/@… теперь уводит в SPA vkvideo.ru — id берём со страницы профиля
			if oid, err = resolveOwner(ctx, env, v, strings.TrimPrefix(g["handle"], "@")); err != nil {
				return nil, ex.Errorf(videosKey, g["handle"], ex.ErrUnavailable, "unable to find page id: %v", err)
			}
		}
		section = "all"
		if pu, err := url.Parse(rawURL); err == nil && pu.Query().Get("section") != "" {
			section = pu.Query().Get("section")
		}
		if m := reTitlePanel.FindStringSubmatch(page); m != nil {
			title = ex.CleanHTML(m[1])
		}
	}
	info := &media.Info{Kind: media.KindPlaylist, ID: oid + "_" + section, Title: title, WebpageURL: rawURL}
	offset := 0
	for page := 0; page < maxVideoPages; page++ {
		body, err := v.fetch(ctx, env, ex.Request{
			Method: http.MethodPost,
			URL:    "https://vk.com/al_video.php",
			Body: strings.NewReader(url.Values{
				"act": {"load_videos_silent"}, "offset": {strconv.Itoa(offset)}, "oid": {oid}, "section": {section}, "al": {"1"},
			}.Encode()),
			Headers: map[string]string{
				"Content-Type":     "application/x-www-form-urlencoded",
				"Referer":          "https://vk.com/al_video.php",
				"X-Requested-With": "XMLHttpRequest",
			},
		})
		if err != nil {
			if len(info.Entries) > 0 {
				break
			}
			return nil, wrap(info.ID, err)
		}
		var resp any
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			return nil, ex.Errorf(videosKey, info.ID, ex.ErrUnavailable, "unexpected response")
		}
		if code := ex.Str(resp, "payload", 0); code == "3" {
			return nil, ex.Errorf(videosKey, info.ID, ex.ErrLoginRequired, "login required (pass --cookies)")
		}
		sec := ex.Map(resp, "payload", 1, 0, section)
		list := ex.Slice(sec, "list")
		for _, it := range list {
			owner, id := ex.Int(it, 0), ex.Int(it, 1)
			if id == 0 {
				continue
			}
			vid := strconv.FormatInt(owner, 10) + "_" + strconv.FormatInt(id, 10)
			e := media.NewURL("https://vk.com/video"+vid, ieKey)
			e.ID = vid
			e.Title = html.UnescapeString(ex.Str(it, 3))
			info.Entries = append(info.Entries, e)
		}
		total := int(ex.Int(sec, "total"))
		count := int(ex.Int(sec, "count"))
		if len(list) == 0 || count <= offset || count >= total || (env.MaxEntries > 0 && len(info.Entries) >= env.MaxEntries) {
			break
		}
		offset = count
	}
	if info.Title == "" {
		info.Title = info.ID
	}
	info.PlaylistCount = len(info.Entries)
	return info, nil
}

var (
	reGroupID = regexp.MustCompile(`\b(?:public|club|event)(\d{3,})\b`)
	reUserID  = regexp.MustCompile(`["/]id(\d{3,})\b`)
)

// resolveOwner находит oid по короткому имени: самый частый id сообщества
// на странице (→ -N) или, если это пользователь, его id.
func resolveOwner(ctx context.Context, env *ex.Env, v *VK, handle string) (string, error) {
	page, err := v.fetch(ctx, env, ex.Request{URL: "https://vk.com/" + url.PathEscape(handle)})
	if err != nil {
		return "", err
	}
	mostFrequent := func(re *regexp.Regexp) string {
		counts := map[string]int{}
		best := ""
		for _, m := range re.FindAllStringSubmatch(page, -1) {
			counts[m[1]]++
			if counts[m[1]] > counts[best] {
				best = m[1]
			}
		}
		return best
	}
	if id := mostFrequent(reGroupID); id != "" {
		return "-" + id, nil
	}
	if id := mostFrequent(reUserID); id != "" {
		return id, nil
	}
	return "", errors.New("no owner id on profile page")
}
