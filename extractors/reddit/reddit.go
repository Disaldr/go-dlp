// Package reddit — экстрактор постов Reddit: видео v.redd.it (HLS + DASH),
// видео внутри текстовых постов и внешние ссылки (YouTube и т.п.).
// API Reddit отвергает не-браузерные TLS-отпечатки, поэтому запросы к
// reddit.com идут с имитацией браузера.
package reddit

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"net/url"
	"regexp"
	"strings"

	"github.com/Disaldr/go-dlp/dash"
	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/hls"
	"github.com/Disaldr/go-dlp/media"
)

const ieKey = "reddit"

var matcher = ex.MustMatcher(
	`^https?://(?:\w+\.)?reddit(?:media)?\.com/(?P<slug>(?:(?:r|user)/[^/]+/)?comments/(?P<id>[^/?#&]+))`,
)

// Reddit — экстрактор постов Reddit.
type Reddit struct{}

// New возвращает экстрактор Reddit.
func New() *Reddit { return &Reddit{} }

func (*Reddit) Name() string           { return ieKey }
func (*Reddit) Description() string    { return "Reddit posts (v.redd.it video, embeds)" }
func (*Reddit) Suitable(u string) bool { return matcher.Match(u) }

// target — цель имитации для запросов к reddit.com.
func target(env *ex.Env) string {
	if t := env.ImpersonateTarget(env.Arg(ieKey, "impersonate", "")); t != "" {
		return t
	}
	return "chrome"
}

func (r *Reddit) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := matcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	slug, id := g["slug"], g["id"]
	imp := target(env)

	// сессия (cookie loid), как в yt-dlp: без неё API отвечает 403
	if env.Cookie("https://www.reddit.com/", "loid") == "" && env.Cookie("https://www.reddit.com/", "reddit_session") == "" {
		_, err := env.Fetch(ctx, ex.Request{
			URL:         "https://www.reddit.com/svc/shreddit/" + slug,
			Query:       url.Values{"seeker-session": {"false"}, "render-mode": {"partial"}, "referer": {rawURL}},
			Impersonate: imp,
		})
		if err != nil {
			env.Debug("reddit: session request failed", "err", err)
		}
	}

	var data []any
	resp, err := env.Fetch(ctx, ex.Request{
		URL:         "https://www.reddit.com/" + slug + "/.json",
		Headers:     map[string]string{"Accept": "application/json"},
		Impersonate: imp,
	})
	if err != nil {
		var he *ex.HTTPError
		if errors.As(err, &he) && he.StatusCode == 403 {
			var body any
			_ = decode(he.Body, &body)
			switch ex.Str(body, "reason") {
			case "quarantined":
				return nil, ex.Errorf(ieKey, id, ex.ErrLoginRequired, "quarantined subreddit; an account that has opted in is required")
			case "private":
				return nil, ex.Errorf(ieKey, id, ex.ErrLoginRequired, "private subreddit; an approved account is required")
			}
			return nil, ex.Errorf(ieKey, id, ex.ErrLoginRequired, "HTTP 403 from Reddit API (blocked IP or login required; try --cookies)")
		}
		return nil, ex.Wrap(ieKey, id, err, "unable to download post JSON")
	}
	if err := decode(resp.Body, &data); err != nil {
		return nil, ex.Errorf(ieKey, id, ex.ErrLoginRequired, "Reddit returned non-JSON response (authentication may be required)")
	}
	post := ex.Map(data, 0, "data", "children", 0, "data")
	if post == nil {
		return nil, ex.Errorf(ieKey, id, ex.ErrUnavailable, "post not found")
	}

	info := &media.Info{
		Kind:        media.KindVideo,
		ID:          id,
		Title:       truncate(ex.Str(post, "title"), 72),
		Description: ex.Str(post, "selftext"),
		Timestamp:   int64(ex.Float(post, "created_utc")),
		Uploader:    ex.Str(post, "author"),
		ChannelID:   ex.Str(post, "subreddit"),
		LikeCount:   ex.Int(post, "ups"),
		WebpageURL:  "https://www.reddit.com/" + slug + "/",
	}
	info.UploadDate = ex.UnixToDate(info.Timestamp)
	if ex.Bool(post, "over_18") {
		info.AgeLimit = 18
	}
	for _, img := range ex.Slice(post, "preview", "images") {
		if u := html.UnescapeString(ex.Str(img, "source", "url")); u != "" {
			info.Thumbnails = append(info.Thumbnails, media.Thumbnail{
				URL: u, Width: int(ex.Int(img, "source", "width")), Height: int(ex.Int(img, "source", "height")),
			})
			info.Thumbnail = u
		}
	}

	videoURL := ex.Str(post, "url")
	pu, _ := url.Parse(videoURL)

	// 1. Видео, загруженное на Reddit (в т.ч. в кросспосте).
	rv := redditVideo(post)
	if rv != nil {
		return withRedditVideo(ctx, env, info, rv)
	}

	// 2. Текстовый пост со встроенными видео.
	if pu != nil && strings.Contains(pu.Host, "reddit.com") && strings.Contains(pu.Path, "/"+id+"/") {
		var entries []*media.Info
		for _, m := range ex.Map(post, "media_metadata") {
			if ex.Str(m, "e") != "RedditVideo" || ex.Str(m, "id") == "" {
				continue
			}
			e := *info
			e.ID = ex.Str(m, "id")
			e.Formats = nil
			if u := html.UnescapeString(ex.Str(m, "hlsUrl")); u != "" {
				e.Formats = append(e.Formats, hlsFormats(ctx, env, u)...)
			}
			if u := html.UnescapeString(ex.Str(m, "dashUrl")); u != "" {
				e.Formats = append(e.Formats, dashFormats(ctx, env, u)...)
			}
			if len(e.Formats) > 0 {
				entries = append(entries, &e)
			}
		}
		if len(entries) == 0 {
			return nil, ex.Errorf(ieKey, id, ex.ErrNoFormats, "no media found in post")
		}
		return &media.Info{
			Kind: media.KindPlaylist, ID: id, Title: info.Title, WebpageURL: info.WebpageURL,
			Entries: entries, PlaylistCount: len(entries),
		}, nil
	}

	if pu != nil && pu.Host == "v.redd.it" {
		return nil, ex.Errorf(ieKey, id, ex.ErrUnavailable, "this video is processing")
	}
	if videoURL == "" {
		return nil, ex.Errorf(ieKey, id, ex.ErrNoFormats, "post has no media")
	}

	// 3. Внешняя ссылка — отдаём другому экстрактору.
	link := media.NewURL(videoURL, "")
	link.Title = info.Title
	return link, nil
}

func redditVideo(post map[string]any) map[string]any {
	candidates := []any{post}
	for _, cp := range ex.Slice(post, "crosspost_parent_list") {
		candidates = append(candidates, cp)
	}
	for _, c := range candidates {
		for _, k := range []string{"secure_media", "media"} {
			if rv := ex.Map(c, k, "reddit_video"); rv != nil {
				return rv
			}
		}
	}
	return nil
}

var reVreddit = regexp.MustCompile(`https?://v\.redd\.it/([^/?#&]+)`)

func withRedditVideo(ctx context.Context, env *ex.Env, info *media.Info, rv map[string]any) (*media.Info, error) {
	fallback := html.UnescapeString(ex.Str(rv, "fallback_url"))
	if m := reVreddit.FindStringSubmatch(fallback); m != nil {
		info.ID = m[1]
	}
	info.Duration = ex.Float(rv, "duration")
	dashURL := html.UnescapeString(ex.Str(rv, "dash_url"))
	if dashURL == "" {
		dashURL = "https://v.redd.it/" + info.ID + "/DASHPlaylist.mpd"
	}
	hlsURL := html.UnescapeString(ex.Str(rv, "hls_url"))
	if hlsURL == "" {
		hlsURL = "https://v.redd.it/" + info.ID + "/HLSPlaylist.m3u8"
	}
	// f=hd,subsAll — HLS с субтитрами и всеми качествами (как в yt-dlp)
	if hu, err := url.Parse(hlsURL); err == nil {
		q := hu.Query()
		f := q.Get("f")
		if f == "" {
			f = "hd"
		}
		q.Set("f", f+",subsAll")
		hu.RawQuery = q.Encode()
		hlsURL = hu.String()
	}

	if fallback != "" {
		info.Formats = append(info.Formats, &media.Format{
			FormatID:   "fallback",
			URL:        fallback,
			Ext:        "mp4",
			Width:      int(ex.Int(rv, "width")),
			Height:     int(ex.Int(rv, "height")),
			TBR:        ex.Float(rv, "bitrate_kbps"),
			VCodec:     "h264",
			ACodec:     media.CodecNone,
			FormatNote: "DASH video, mp4_dash",
		})
	}
	info.Formats = append(info.Formats, hlsFormats(ctx, env, hlsURL)...)
	info.Formats = append(info.Formats, dashFormats(ctx, env, dashURL)...)
	info.Subtitles = map[string][]media.Subtitle{
		"en": {{URL: "https://v.redd.it/" + info.ID + "/wh_ben_en.vtt", Ext: "vtt"}},
	}
	if len(info.Formats) == 0 {
		return nil, ex.Errorf(ieKey, info.ID, ex.ErrNoFormats, "no formats found")
	}
	return info, nil
}

func hlsFormats(ctx context.Context, env *ex.Env, u string) []*media.Format {
	body, err := env.GetText(ctx, u, nil)
	if err != nil {
		env.Warn("reddit: unable to download HLS manifest", "err", err)
		return nil
	}
	return hls.ParseMaster(body, u, "hls")
}

func dashFormats(ctx context.Context, env *ex.Env, u string) []*media.Format {
	r, err := env.Fetch(ctx, ex.Request{URL: u})
	if err != nil {
		env.Warn("reddit: unable to download DASH manifest", "err", err)
		return nil
	}
	fs, err := dash.Parse(r.Body, r.URL, "dash")
	if err != nil {
		env.Warn("reddit: unable to parse DASH manifest", "err", err)
	}
	return fs
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-3]) + "..."
}

func decode(b []byte, out any) error { return json.Unmarshal(b, out) }
