// Package okru — экстрактор видео Одноклассников (ok.ru): видео, embed,
// прямые эфиры; встроенные YouTube-ролики передаются экстрактору YouTube.
package okru

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/Disaldr/go-dlp/dash"
	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/hls"
	"github.com/Disaldr/go-dlp/media"
)

const ieKey = "odnoklassniki"

var matcher = ex.MustMatcher(
	`^https?://(?:www\.|m\.)?(?:odnoklassniki|ok)\.ru/(?:video(?P<embed>embed)?/|web-api/video/moviePlayer/|live/|dk\?.*?st\.mvId=)(?P<id>[\d-]+)`,
)

// OK — экстрактор видео ok.ru.
type OK struct{}

// New возвращает экстрактор ok.ru.
func New() *OK { return &OK{} }

func (*OK) Name() string           { return ieKey }
func (*OK) Description() string    { return "Odnoklassniki (ok.ru) videos and live streams" }
func (*OK) Suitable(u string) bool { return matcher.Match(u) }

// qualityHeights — высота кадра по имени качества OK.ru.
var qualityHeights = map[string]int{
	"mobile": 144, "lowest": 240, "low": 360, "sd": 480, "hd": 720, "full": 1080, "quad": 1440, "ultra": 2160,
}

// qualityOrder — порядок качества по параметру type в URL (как в yt-dlp).
var qualityOrder = []string{"4", "0", "1", "2", "3", "5", "6", "7"}

var (
	reType = regexp.MustCompile(`\btype[/=](\d)`)
	reStub = regexp.MustCompile(`(?s)class="vp_video_stub_txt"[^>]*>(.*?)</`)
)

func (o *OK) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := matcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	id := g["id"]
	mode := "video"
	if g["embed"] != "" {
		mode = "videoembed"
	}
	headers := map[string]string{}
	if ref := env.Arg(ieKey, "referer", ""); ref != "" {
		headers["Referer"] = ref
	}
	page, err := env.GetText(ctx, "https://ok.ru/"+mode+"/"+id, headers)
	if err != nil {
		return nil, ex.Wrap(ieKey, id, err, "unable to download page")
	}
	if m := reStub.FindStringSubmatch(page); m != nil {
		return nil, ex.Errorf(ieKey, id, ex.ErrUnavailable, "%s", ex.CleanHTML(m[1]))
	}
	if strings.Contains(page, ">Access to this video is restricted</div>") {
		return nil, ex.Errorf(ieKey, id, ex.ErrLoginRequired, "access to this video is restricted (pass --cookies)")
	}

	player, err := findPlayer(page, id)
	if err != nil {
		return nil, ex.Errorf(ieKey, id, ex.ErrUnavailable, "%v", err)
	}
	if ex.Bool(player, "isExternalPlayer") && ex.Str(player, "url") != "" {
		return media.NewURL(ex.Str(player, "url"), ""), nil
	}
	flash := ex.Map(player, "flashvars")
	meta := ex.Get(flash, "metadata")
	if s, ok := meta.(string); ok {
		var v any
		if json.Unmarshal([]byte(s), &v) == nil {
			meta = v
		}
	}
	if ex.Map(meta, "movie") == nil {
		mu, _ := url.QueryUnescape(ex.Str(flash, "metadataUrl"))
		if mu == "" {
			return nil, ex.Errorf(ieKey, id, ex.ErrNoFormats, "no metadata in player")
		}
		form := url.Values{}
		if loc := ex.Str(flash, "location"); loc != "" {
			form.Set("st.location", loc)
		}
		r, err := env.Fetch(ctx, ex.Request{
			Method:  http.MethodPost,
			URL:     ex.URLJoin("https://ok.ru/", mu),
			Body:    strings.NewReader(form.Encode()),
			Headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
		})
		if err != nil {
			return nil, ex.Wrap(ieKey, id, err, "unable to download metadata")
		}
		if err := json.Unmarshal(r.Body, &meta); err != nil {
			return nil, ex.Wrap(ieKey, id, err, "bad metadata JSON")
		}
	}
	movie := ex.Map(meta, "movie")
	info := &media.Info{
		Kind:       media.KindVideo,
		ID:         id,
		Title:      ex.Str(movie, "title"),
		Thumbnail:  ex.Str(movie, "poster"),
		Duration:   ex.Float(movie, "duration"),
		Uploader:   ex.Str(meta, "author", "name"),
		UploaderID: ex.Str(meta, "author", "id"),
		LikeCount:  ex.Int(meta, "likeCount"),
		UploadDate: ex.UnifiedDate(ex.MetaContent(page, "ya:ovs:upload_date")),
		WebpageURL: "https://ok.ru/video/" + id,
	}
	if ex.MetaContent(page, "ya:ovs:adult") == "true" {
		info.AgeLimit = 18
	}
	for _, s := range ex.Slice(movie, "subtitleTracks") {
		if u := ex.Str(s, "url"); u != "" {
			lang := ex.Str(s, "language")
			if lang == "" {
				lang = "en"
			}
			if info.Subtitles == nil {
				info.Subtitles = map[string][]media.Subtitle{}
			}
			info.Subtitles[lang] = append(info.Subtitles[lang], media.Subtitle{URL: u, Ext: "vtt"})
		}
	}

	if ex.Str(meta, "provider") == "USER_YOUTUBE" {
		link := media.NewURL(ex.Str(movie, "contentId"), "youtube")
		link.Title = info.Title
		return link, nil
	}

	for _, v := range ex.Slice(meta, "videos") {
		u := ex.Str(v, "url")
		if u == "" {
			continue
		}
		name := ex.Str(v, "name")
		info.Formats = append(info.Formats, &media.Format{FormatID: name, URL: u, Ext: "mp4", Height: qualityHeights[name]})
	}
	// CDN выдаёт cookies, ломающие прямые загрузки, — манифесты качаем без cookies
	if u := ex.FirstStr(meta, ex.P("hlsManifestUrl"), ex.P("ondemandHls")); u != "" {
		info.Formats = append(info.Formats, hlsFormats(ctx, env, u)...)
	}
	for _, k := range []struct{ id, key string }{{"dash", "ondemandDash"}, {"webm", "metadataWebmUrl"}} {
		if u := ex.Str(meta, k.key); u != "" {
			if r, err := env.Fetch(ctx, ex.Request{URL: u, NoCookies: true}); err == nil {
				if fs, err := dash.Parse(r.Body, r.URL, k.id); err == nil {
					info.Formats = append(info.Formats, fs...)
				}
			}
		}
	}
	if emb := ex.Str(meta, "metadataEmbedded"); emb != "" {
		if fs, err := dash.Parse([]byte(emb), info.WebpageURL, "mpd"); err == nil {
			info.Formats = append(info.Formats, fs...)
		}
	}
	for _, f := range info.Formats {
		if m := reType.FindStringSubmatch(f.URL); m != nil {
			for i, q := range qualityOrder {
				if q == m[1] {
					f.Quality = float64(i)
				}
			}
		}
	}
	// прямой эфир
	if u := ex.Str(meta, "hlsMasterPlaylistUrl"); u != "" {
		info.IsLive = true
		info.Formats = append(info.Formats, hlsFormats(ctx, env, u)...)
	}
	if len(info.Formats) == 0 {
		if ex.Get(meta, "paymentInfo") != nil {
			return nil, ex.Errorf(ieKey, id, ex.ErrLoginRequired, "this video is paid, subscribe to download it")
		}
		return nil, ex.Errorf(ieKey, id, ex.ErrNoFormats, "no formats found")
	}
	if info.Title == "" {
		info.Title = id
	}
	return info, nil
}

var reOptions = regexp.MustCompile(`data-options=(?:"([^"]+)"|'([^']+)')`)

// findPlayer находит JSON плеера в атрибуте data-options, относящийся к видео id.
func findPlayer(page, id string) (any, error) {
	for _, m := range reOptions.FindAllStringSubmatch(page, -1) {
		raw := html.UnescapeString(m[1] + m[2])
		if !strings.Contains(raw, id) || !strings.HasPrefix(raw, "{") {
			continue
		}
		var v any
		if json.Unmarshal([]byte(raw), &v) == nil && ex.Map(v, "flashvars") != nil {
			return v, nil
		}
	}
	return nil, errNoPlayer
}

var errNoPlayer = errors.New("player data not found on page")

func hlsFormats(ctx context.Context, env *ex.Env, u string) []*media.Format {
	r, err := env.Fetch(ctx, ex.Request{URL: u, NoCookies: true})
	if err != nil {
		env.Warn("ok.ru: HLS manifest", "err", err)
		return nil
	}
	return hls.ParseMaster(r.Text(), r.URL, "hls")
}
