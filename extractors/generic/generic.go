// Package generic — экстрактор «последней надежды»: прямые ссылки на медиа,
// HLS-плейлисты, HTML5 <video>, og:video, JSON-LD и встраивания (iframe),
// которые умеют обработать другие зарегистрированные экстракторы.
package generic

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/Disaldr/go-dlp/dash"
	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/hls"
	"github.com/Disaldr/go-dlp/media"
)

const ieKey = "generic"

// Generic — универсальный экстрактор.
type Generic struct{}

// New возвращает generic-экстрактор.
func New() *Generic { return &Generic{} }

func (*Generic) Name() string { return ieKey }
func (*Generic) Description() string {
	return "Generic: direct links, HLS, HTML5 video, og:video, embeds (with anti-bot browser impersonation)"
}
func (*Generic) Priority() int { return ex.PriorityFallback }
func (*Generic) Suitable(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

func idFromURL(u string) string {
	pu, err := url.Parse(u)
	if err == nil {
		base := strings.TrimSuffix(path.Base(pu.Path), path.Ext(pu.Path))
		if base != "" && base != "/" && base != "." {
			return base
		}
	}
	h := sha1.Sum([]byte(u))
	return hex.EncodeToString(h[:])[:12]
}

func (g *Generic) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	id := idFromURL(rawURL)
	// Открываем поток и смотрим Content-Type, не скачивая весь файл. Если сайт
	// отвечает анти-бот-заслоном (Cloudflare и т.п.), повторяем с имитацией
	// браузера — нужно онлайн-кинотеатрам и другим защищённым сайтам.
	resp, imp, err := openAntiBot(ctx, env, rawURL)
	if err != nil {
		return nil, ex.Wrap(ieKey, id, err, "unable to open URL")
	}
	finalURL := resp.Request.URL.String()
	ctype := strings.ToLower(resp.Header.Get("Content-Type"))
	if resp.StatusCode >= 400 {
		resp.Body.Close()
		return nil, ex.Wrap(ieKey, id, &ex.HTTPError{StatusCode: resp.StatusCode, URL: rawURL}, "request failed")
	}

	ext := ex.MimeToExt(ctype)
	if ext == "" && !strings.Contains(ctype, "html") {
		if e := ex.URLExt(finalURL); ex.KnownMediaExts[e] {
			ext = e
		}
	}

	// Прямая ссылка на медиа.
	if ext != "" && ext != "m3u8" && ext != "mpd" &&
		(strings.HasPrefix(ctype, "video/") || strings.HasPrefix(ctype, "audio/") || ex.KnownMediaExts[ext]) {
		resp.Body.Close()
		f := &media.Format{FormatID: ext, URL: finalURL, Ext: ext, Protocol: protocol(finalURL), Filesize: max(resp.ContentLength, 0), Impersonate: imp}
		if strings.HasPrefix(ctype, "audio/") {
			f.VCodec = media.CodecNone
		}
		return &media.Info{Kind: media.KindVideo, ID: id, Title: id, WebpageURL: rawURL, Formats: []*media.Format{f}}, nil
	}

	body, err := readLimited(resp)
	if err != nil {
		return nil, ex.Wrap(ieKey, id, err, "unable to read response")
	}

	// HLS-плейлист.
	if ext == "m3u8" || strings.HasPrefix(strings.TrimSpace(body), "#EXTM3U") {
		fs := hls.ParseMaster(body, finalURL, "hls")
		setImpersonate(fs, imp)
		return &media.Info{Kind: media.KindVideo, ID: id, Title: id, WebpageURL: rawURL, Formats: fs}, nil
	}

	// DASH-манифест.
	if ext == "mpd" || strings.Contains(body[:min(len(body), 512)], "<MPD") {
		m, err := dash.ParseManifest([]byte(body), finalURL, "dash")
		if err != nil {
			return nil, ex.Wrap(ieKey, id, err, "unable to parse DASH manifest")
		}
		setImpersonate(m.Formats, imp)
		return &media.Info{Kind: media.KindVideo, ID: id, Title: id, WebpageURL: rawURL, Formats: m.Formats, IsLive: m.Dynamic}, nil
	}

	return g.fromHTML(ctx, env, rawURL, finalURL, id, body, imp)
}

// openAntiBot открывает URL; при признаках анти-бот-защиты и если имитация
// ещё не включена глобально, повторяет запрос с имитацией браузера ("any").
// Возвращает ответ и использованную цель имитации (""/"any").
func openAntiBot(ctx context.Context, env *ex.Env, rawURL string) (*http.Response, string, error) {
	resp, err := env.Open(ctx, ex.Request{URL: rawURL})
	if err != nil {
		return nil, "", err
	}
	if env.ImpersonateTarget("") != "" || !blocked(resp) {
		return resp, env.ImpersonateTarget(""), nil
	}
	env.Debug("generic: anti-bot response, retrying with browser impersonation", "url", rawURL, "status", resp.StatusCode)
	resp2, err2 := env.Open(ctx, ex.Request{URL: rawURL, Impersonate: "any"})
	if err2 != nil {
		return resp, "", nil // оставляем исходный ответ
	}
	resp.Body.Close()
	return resp2, "any", nil
}

// blocked распознаёт типичный анти-бот-ответ (Cloudflare и подобные).
func blocked(resp *http.Response) bool {
	switch resp.StatusCode {
	case 403, 429, 503:
		return true
	}
	return false
}

// maxPage — сколько HTML читаем для поиска видео.
const maxPage = 8 << 20

func readLimited(resp *http.Response) (string, error) {
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxPage))
	return string(b), err
}

func protocol(u string) string {
	if strings.HasPrefix(u, "http://") {
		return media.ProtoHTTP
	}
	return media.ProtoHTTPS
}

// reMediaURL — прямая ссылка на медиа (HLS/DASH/прогрессив) в разметке/скриптах.
var reMediaURL = regexp.MustCompile(`https?:\\?/\\?/[^"'\s<>]+?\.(?:m3u8|mpd|mp4|m4v|webm)(?:\?[^"'\s<>]*)?`)

// rePlayerSource — медиа-URL в конфигах плееров и JSON-состоянии страницы
// (JWPlayer/Playerjs/онлайн-кинотеатры, а также __NEXT_DATA__ / window.__*__,
// т.к. поиск идёт по всей странице): "file"/"hls"/"src"/"manifest": "...".
var rePlayerSource = regexp.MustCompile(`["']?(?:file|src|source|hls|dash|manifest|playlist|stream_url|videoUrl|contentUrl)["']?\s*:\s*["']([^"']+?\.(?:m3u8|mpd|mp4|m4v|webm)(?:\\?\?[^"']*)?)["']`)

// playerSourceURLs вытаскивает медиа-URL из конфигов плееров и JSON страницы.
func playerSourceURLs(page string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range rePlayerSource.FindAllStringSubmatch(page, -1) {
		u := strings.ReplaceAll(m[1], `\/`, "/")
		if u != "" && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

// setImpersonate проставляет цель имитации всем форматам (пусто — ничего).
func setImpersonate(fs []*media.Format, imp string) {
	if imp == "" {
		return
	}
	for _, f := range fs {
		if f.Impersonate == "" {
			f.Impersonate = imp
		}
	}
}

func (g *Generic) fromHTML(ctx context.Context, env *ex.Env, rawURL, pageURL, id, page, imp string) (*media.Info, error) {
	title := ex.MetaContent(page, "og:title", "twitter:title")
	if title == "" {
		title = ex.HTMLTitle(page)
	}
	if title == "" {
		title = id
	}
	info := &media.Info{
		Kind:        media.KindVideo,
		ID:          id,
		Title:       title,
		Description: ex.MetaContent(page, "og:description", "description", "twitter:description"),
		Thumbnail:   ex.URLJoin(pageURL, ex.MetaContent(page, "og:image", "twitter:image")),
		WebpageURL:  rawURL,
		HTTPHeaders: map[string]string{"Referer": pageURL},
	}

	// 1. Встраивания, которые умеют обработать другие экстракторы.
	var embeds []string
	seen := map[string]bool{}
	for _, tag := range []string{"iframe", "embed"} {
		for _, a := range ex.FindTags(page, tag) {
			src := a["src"]
			if src == "" {
				src = a["data-src"]
			}
			u := ex.URLJoin(pageURL, src)
			if u == "" || seen[u] {
				continue
			}
			if _, ok := env.FindEmbedded(u, ieKey); ok {
				seen[u] = true
				embeds = append(embeds, u)
			}
		}
	}

	// 1b. Встраивания известных плееров прямо в разметку (не через iframe):
	// Brightcove, Wistia, JWPlayer, Kaltura — один детектор на множество сайтов.
	for _, u := range detectInlineEmbeds(page) {
		if seen[u] {
			continue
		}
		if _, ok := env.FindEmbedded(u, ieKey); ok {
			seen[u] = true
			embeds = append(embeds, u)
		}
	}

	// 2. Собственные медиа страницы.
	var candidates []string
	addCand := func(u string) {
		u = strings.ReplaceAll(u, `\/`, "/")
		u = ex.URLJoin(pageURL, strings.TrimSpace(u))
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		candidates = append(candidates, u)
	}
	for _, name := range []string{"og:video:secure_url", "og:video:url", "og:video", "twitter:player:stream"} {
		if v := ex.MetaContent(page, name); v != "" {
			if _, ok := env.FindEmbedded(ex.URLJoin(pageURL, v), ieKey); ok {
				u := ex.URLJoin(pageURL, v)
				if !seen[u] {
					seen[u] = true
					embeds = append(embeds, u)
				}
				continue
			}
			addCand(v)
		}
	}
	for _, tag := range []string{"video", "audio", "source"} {
		for _, a := range ex.FindTags(page, tag) {
			if a["src"] != "" {
				addCand(a["src"])
			}
		}
	}
	for _, ld := range ex.JSONLD(page) {
		if t, _ := ld["@type"].(string); t != "VideoObject" && t != "AudioObject" {
			continue
		}
		if s := ex.Str(ld, "name"); s != "" && info.Title == id {
			info.Title = s
		}
		if s := ex.Str(ld, "description"); s != "" && info.Description == "" {
			info.Description = s
		}
		if d := ex.ParseDuration(ex.Str(ld, "duration")); d > 0 {
			info.Duration = d
		}
		if dt := ex.UnifiedDate(ex.Str(ld, "uploadDate")); dt != "" {
			info.UploadDate = dt
		}
		if u := ex.Str(ld, "contentUrl"); u != "" {
			addCand(u)
		}
		if u := ex.URLJoin(pageURL, ex.Str(ld, "embedUrl")); u != "" {
			if _, ok := env.FindEmbedded(u, ieKey); ok && !seen[u] {
				seen[u] = true
				embeds = append(embeds, u)
			}
		}
	}
	// 2c. Медиа из конфигов плееров и JSON-состояния (онлайн-кинотеатры и пр.).
	for _, u := range playerSourceURLs(page) {
		addCand(u)
	}
	// Последняя попытка: любые прямые ссылки на медиа в разметке.
	if len(candidates) == 0 && len(embeds) == 0 {
		for _, m := range reMediaURL.FindAllString(page, 10) {
			addCand(m)
		}
	}

	for i, c := range candidates {
		ext := ex.URLExt(c)
		if ext == "mpd" {
			if r, err := env.Fetch(ctx, ex.Request{URL: c, Impersonate: imp}); err == nil {
				if fs, err := dash.Parse(r.Body, r.URL, "dash-"+itoa(i)); err == nil {
					setImpersonate(fs, imp)
					info.Formats = append(info.Formats, fs...)
				}
			}
			continue
		}
		if ext == "m3u8" {
			info.Formats = append(info.Formats, &media.Format{
				FormatID: "hls-" + itoa(i), URL: c, ManifestURL: c, Ext: "mp4", Protocol: media.ProtoHLS, Impersonate: imp,
			})
			continue
		}
		if ext == "" || !ex.KnownMediaExts[ext] {
			ext = "mp4"
		}
		info.Formats = append(info.Formats, &media.Format{FormatID: ext + "-" + itoa(i), URL: c, Ext: ext, Protocol: protocol(c), Impersonate: imp})
	}

	switch {
	case len(info.Formats) > 0:
		return info, nil
	case len(embeds) == 1:
		return media.NewURL(embeds[0], ""), nil
	case len(embeds) > 1:
		pl := &media.Info{Kind: media.KindPlaylist, ID: id, Title: info.Title, WebpageURL: rawURL}
		for _, e := range embeds {
			pl.Entries = append(pl.Entries, media.NewURL(e, ""))
		}
		pl.PlaylistCount = len(pl.Entries)
		return pl, nil
	}
	return nil, ex.Errorf(ieKey, id, ex.ErrUnsupportedURL, "no video found on page %s", rawURL)
}

func itoa(i int) string { return strconv.Itoa(i) }
