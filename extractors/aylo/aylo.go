// Package aylo — экстракторы сайтов Aylo: Pornhub, RedTube, YouPorn (18+).
// У всех трёх плеер описывает потоки массивом mediaDefinitions.
//
// Проверки возраста: выставляется только cookie обычного дисклеймера
// «мне есть 18» (аналог нажатия кнопки). Обязательная по закону проверка
// возраста (в некоторых странах/штатах) не обходится — экстрактор вернёт ошибку.
// JS-челлендж Pornhub (RNKEY) тоже не решается.
package aylo

import (
	"context"
	"encoding/json"
	"html"
	"regexp"
	"strconv"
	"strings"

	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/hls"
	"github.com/Disaldr/go-dlp/media"
)

var reBitrate = regexp.MustCompile(`(\d{3,4})[pP]_(\d+)[kK]_\d+`)

// mediaFormats превращает mediaDefinitions в форматы. Записи с format=hls/mp4
// без конкретного качества указывают на JSON со списком потоков — он догружается.
func mediaFormats(ctx context.Context, env *ex.Env, base string, defs []any, headers map[string]string, depth int) []*media.Format {
	var out []*media.Format
	seen := map[string]bool{}
	for _, d := range defs {
		u := ex.URLJoin(base, ex.Str(d, "videoUrl"))
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		format := ex.Str(d, "format")
		quality := ex.Get(d, "quality")
		_, qualityIsList := quality.([]any)
		qs := ex.Str(d, "quality")
		if depth == 0 && (strings.Contains(u, "/video/get_media") || (format == "mp4" && (qs == "" || qualityIsList)) || (format == "hls" && qualityIsList)) {
			r, err := env.Fetch(ctx, ex.Request{URL: u, Headers: headers})
			if err != nil {
				env.Warn("media definitions request failed", "err", err)
				continue
			}
			var more []any
			if json.Unmarshal(r.Body, &more) == nil {
				out = append(out, mediaFormats(ctx, env, base, more, headers, depth+1)...)
			}
			continue
		}
		if format == "hls" || ex.URLExt(u) == "m3u8" {
			body, err := env.GetText(ctx, u, headers)
			if err != nil {
				continue
			}
			fs := hls.ParseMaster(body, u, "hls")
			for _, f := range fs {
				f.HTTPHeaders = headers // CDN проверяет Referer (иначе HTTP 412)
			}
			// одиночный «master» на одно качество — подписываем высотой
			if h, _ := strconv.Atoi(qs); h > 0 && len(fs) == 1 && fs[0].Height == 0 {
				fs[0].Height = h
				fs[0].FormatID = "hls-" + qs + "p"
			}
			out = append(out, fs...)
			continue
		}
		f := &media.Format{FormatID: qs, URL: u, Ext: "mp4", Filesize: ex.Int(d, "videoSize"), HTTPHeaders: headers}
		f.Height, _ = strconv.Atoi(qs)
		if m := reBitrate.FindStringSubmatch(u); m != nil {
			if f.Height == 0 {
				f.Height, _ = strconv.Atoi(m[1])
			}
			f.TBR, _ = strconv.ParseFloat(m[2], 64)
		}
		if f.Height > 0 {
			f.FormatID = strconv.Itoa(f.Height) + "p"
		}
		if f.FormatID == "" {
			f.FormatID = "mp4"
		}
		out = append(out, f)
	}
	return out
}

// jsonAfterRegex декодирует JSON-значение, открывающая скобка которого —
// последний символ совпадения re.
func jsonAfterRegex(page string, re *regexp.Regexp) any {
	loc := re.FindStringIndex(page)
	if loc == nil {
		return nil
	}
	var v any
	if json.NewDecoder(strings.NewReader(page[loc[1]-1:])).Decode(&v) != nil {
		return nil
	}
	return v
}

func firstMatch(page string, res ...*regexp.Regexp) string {
	for _, re := range res {
		if m := re.FindStringSubmatch(page); m != nil {
			return ex.CleanHTML(m[len(m)-1])
		}
	}
	return ""
}

func ageCookie(names ...string) map[string]string {
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = n + "=1"
	}
	return map[string]string{"Cookie": strings.Join(parts, "; ")}
}

// --- Pornhub ---

const pornhubKey = "pornhub"

var pornhubMatcher = ex.MustMatcher(
	`^https?://(?:[^/]+\.)?(?P<host>pornhub(?:premium)?\.(?:com|net|org))/(?:(?:view_video\.php|video/show)\?(?:.*&)?viewkey=|embed/)(?P<id>[\da-z]+)`,
)

var (
	reFlashvars   = regexp.MustCompile(`var\s+flashvars_\d+\s*=\s*\{`)
	rePHChallenge = regexp.MustCompile(`document\.cookie\s*=\s*["']RNKEY=|<body\b[^>]+\bonload=["']go\(\)`)
	rePHRemoved   = regexp.MustCompile(`(?s)<div[^>]+class=["'](?:removed|geoBlocked)["'][^>]*>\s*<div[^>]*>(.+?)</div>`)
	rePHUploader  = regexp.MustCompile(`(?s)<div[^>]+class=["']video-info-row[^"']*["'][^>]*>.*?<a[^>]+href=["'][^"']*/(?:model|users|channels|pornstar)/[^"']+["'][^>]*>([^<]+)</a>`)
)

// Pornhub — экстрактор Pornhub.
type Pornhub struct{}

// NewPornhub возвращает экстрактор Pornhub.
func NewPornhub() *Pornhub { return &Pornhub{} }

func (*Pornhub) Name() string           { return pornhubKey }
func (*Pornhub) Description() string    { return "Pornhub (18+)" }
func (*Pornhub) Suitable(u string) bool { return pornhubMatcher.Match(u) }

func (p *Pornhub) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := pornhubMatcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	host, id := g["host"], g["id"]
	pageURL := "https://www." + host + "/view_video.php?viewkey=" + id
	headers := ageCookie("age_verified", "accessAgeDisclaimerPH", "platform_pc")
	headers["Cookie"] = strings.Replace(headers["Cookie"], "platform_pc=1", "platform=pc", 1)
	page, err := env.GetText(ctx, pageURL, headers)
	if err != nil {
		return nil, ex.Wrap(pornhubKey, id, err, "unable to download page")
	}
	if rePHChallenge.MatchString(page) {
		return nil, ex.Errorf(pornhubKey, id, ex.ErrLoginRequired, "the site returned a JS anti-bot challenge, which is not supported")
	}
	fv := jsonAfterRegex(page, reFlashvars)
	if fv == nil {
		if msg := firstMatch(page, rePHRemoved); msg != "" {
			return nil, ex.Errorf(pornhubKey, id, ex.ErrUnavailable, "%s", msg)
		}
		if strings.Contains(page, `id="lockedPlayer"`) || strings.Contains(page, `id='lockedPlayer'`) {
			return nil, ex.Errorf(pornhubKey, id, ex.ErrLoginRequired, "video is locked")
		}
		return nil, ex.Errorf(pornhubKey, id, ex.ErrUnavailable,
			"no player data on page (removed video, or the site requires age verification in your region — it is not bypassed)")
	}
	info := &media.Info{
		Kind:       media.KindVideo,
		ID:         id,
		Title:      html.UnescapeString(ex.MetaContent(page, "twitter:title", "og:title")),
		Thumbnail:  ex.Str(fv, "image_url"),
		Duration:   ex.Float(fv, "video_duration"),
		Uploader:   firstMatch(page, rePHUploader),
		AgeLimit:   18,
		WebpageURL: pageURL,
	}
	if info.Title == "" {
		info.Title = ex.Str(fv, "video_title")
	}
	if cc := ex.Str(fv, "closedCaptionsFile"); cc != "" {
		info.Subtitles = map[string][]media.Subtitle{"en": {{URL: cc, Ext: "srt"}}}
	}
	reqHeaders := map[string]string{"Referer": "https://www." + host + "/", "Cookie": headers["Cookie"]}
	info.Formats = mediaFormats(ctx, env, "https://www."+host, ex.Slice(fv, "mediaDefinitions"), reqHeaders, 0)
	if len(info.Formats) == 0 {
		return nil, ex.Errorf(pornhubKey, id, ex.ErrNoFormats, "no formats found")
	}
	return info, nil
}

// --- RedTube ---

const redtubeKey = "redtube"

var redtubeMatcher = ex.MustMatcher(
	`^https?://(?:(?:\w+\.)?redtube\.com(?:\.br)?/|embed\.redtube\.com/\?.*?\bid=)(?P<id>[0-9]+)`,
)

var (
	reMediaDef  = regexp.MustCompile(`mediaDefinition["']?\s*:\s*\[`)
	reRTTitle   = regexp.MustCompile(`(?s)<h\d[^>]+class="(?:video_title_text|videoTitle|video_title)[^"]*">(.+?)</h\d>`)
	reRTViews   = regexp.MustCompile(`(?s)(?:<div[^>]*>Views</div>\s*<div[^>]*>|class=["']video_view_count[^>]*>)\s*([\d,.]+)`)
	reRTRemoved = regexp.MustCompile(`video-deleted-info|>This video has been removed`)
	reRTPrivate = regexp.MustCompile(`private_video_text|>This video is private`)
)

// RedTube — экстрактор RedTube.
type RedTube struct{}

// NewRedTube возвращает экстрактор RedTube.
func NewRedTube() *RedTube { return &RedTube{} }

func (*RedTube) Name() string           { return redtubeKey }
func (*RedTube) Description() string    { return "RedTube (18+)" }
func (*RedTube) Suitable(u string) bool { return redtubeMatcher.Match(u) }

func (r *RedTube) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := redtubeMatcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	id := g["id"]
	headers := ageCookie("age_verified")
	page, err := env.GetText(ctx, "https://www.redtube.com/"+id, headers)
	if err != nil {
		return nil, ex.Wrap(redtubeKey, id, err, "unable to download page")
	}
	switch {
	case reRTRemoved.MatchString(page):
		return nil, ex.Errorf(redtubeKey, id, ex.ErrUnavailable, "video has been removed")
	case reRTPrivate.MatchString(page):
		return nil, ex.Errorf(redtubeKey, id, ex.ErrLoginRequired, "video is private")
	}
	info := &media.Info{
		Kind:       media.KindVideo,
		ID:         id,
		Title:      firstMatch(page, reRTTitle),
		Thumbnail:  ex.MetaContent(page, "og:image"),
		AgeLimit:   18,
		WebpageURL: "https://www.redtube.com/" + id,
	}
	if info.Title == "" {
		info.Title = ex.MetaContent(page, "og:title")
	}
	for _, ld := range ex.JSONLD(page) {
		if ex.Str(ld, "@type") == "VideoObject" {
			if info.Title == "" {
				info.Title = ex.Str(ld, "name")
			}
			info.Description = ex.Str(ld, "description")
			info.Duration = ex.ParseDuration(ex.Str(ld, "duration"))
			info.UploadDate = ex.UnifiedDate(ex.Str(ld, "uploadDate"))
			info.Uploader = ex.FirstStr(ld, ex.P("author"), ex.P("author", "name"))
		}
	}
	if info.Duration == 0 {
		info.Duration, _ = strconv.ParseFloat(ex.MetaContent(page, "video:duration"), 64)
	}
	if m := reRTViews.FindStringSubmatch(page); m != nil {
		info.ViewCount = ex.ToInt(m[1])
	}
	defs, _ := jsonAfterRegex(page, reMediaDef).([]any)
	info.Formats = mediaFormats(ctx, env, "https://www.redtube.com", defs, headers, 0)
	if len(info.Formats) == 0 {
		return nil, ex.Errorf(redtubeKey, id, ex.ErrNoFormats,
			"no formats found (the site may require age verification in your region — it is not bypassed)")
	}
	return info, nil
}

// --- YouPorn ---

const youpornKey = "youporn"

var youpornMatcher = ex.MustMatcher(
	`^https?://(?:www\.)?youporn\.com/(?:watch|embed)/(?P<id>\d+)(?:/(?P<display>[^/?#&]+))?/?(?:[#?]|$)`,
)

var (
	rePlayerVars = regexp.MustCompile(`\bplayervars\s*:\s*\{`)
	reYPTitle    = regexp.MustCompile(`(?s)<div[^>]+class=["']watchVideoTitle[^>]+>(.+?)</div>`)
	reYPUploader = regexp.MustCompile(`(?s)<div[^>]+class=["']submitByLink["'][^>]*>(.+?)</div>`)
	reYPWatch    = regexp.MustCompile(`<div\s[^>]*\bid\s*=\s*["']?watch-container`)
	reYPMain     = regexp.MustCompile(`(?s)<[^>]+id=["']mainContent["'][^>]*>(.+?)</div>`)
)

// YouPorn — экстрактор YouPorn.
type YouPorn struct{}

// NewYouPorn возвращает экстрактор YouPorn.
func NewYouPorn() *YouPorn { return &YouPorn{} }

func (*YouPorn) Name() string           { return youpornKey }
func (*YouPorn) Description() string    { return "YouPorn (18+)" }
func (*YouPorn) Suitable(u string) bool { return youpornMatcher.Match(u) }

func (y *YouPorn) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := youpornMatcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	id := g["id"]
	headers := ageCookie("age_verified")
	page, err := env.GetText(ctx, "https://www.youporn.com/watch/"+id, headers)
	if err != nil {
		return nil, ex.Wrap(youpornKey, id, err, "unable to download page")
	}
	if !reYPWatch.MatchString(page) {
		msg := firstMatch(page, reYPMain)
		if i := strings.Index(msg, "  "); i > 0 {
			msg = msg[:i]
		}
		if msg == "" {
			msg = "video unavailable (or age verification is required in your region — it is not bypassed)"
		}
		return nil, ex.Errorf(youpornKey, id, ex.ErrUnavailable, "youporn says: %s", msg)
	}
	pv := jsonAfterRegex(page, rePlayerVars)
	info := &media.Info{
		Kind:        media.KindVideo,
		ID:          id,
		Title:       firstMatch(page, reYPTitle),
		Description: ex.MetaContent(page, "og:description"),
		Uploader:    firstMatch(page, reYPUploader),
		Duration:    ex.Float(pv, "duration"),
		Thumbnail:   ex.MetaContent(page, "og:image"),
		AgeLimit:    18,
		WebpageURL:  "https://www.youporn.com/watch/" + id + "/",
	}
	if info.Title == "" {
		info.Title = ex.MetaContent(page, "og:title", "title")
	}
	info.Formats = mediaFormats(ctx, env, "https://www.youporn.com", ex.Slice(pv, "mediaDefinitions"), headers, 0)
	if len(info.Formats) == 0 {
		return nil, ex.Errorf(youpornKey, id, ex.ErrNoFormats, "no formats found")
	}
	return info, nil
}
