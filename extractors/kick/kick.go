// Package kick — экстракторы Kick: прямые эфиры, записи (VOD), клипы и
// список записей канала. Как и yt-dlp, запросы к API идут с имитацией браузера.
package kick

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/hls"
	"github.com/Disaldr/go-dlp/media"
)

const (
	liveKey   = "kick:live"
	vodKey    = "kick:vod"
	clipKey   = "kick:clip"
	videosKey = "kick:videos"
	base      = "https://kick.com"
)

const uuidRe = `[\da-f]{8}-(?:[\da-f]{4}-){3}[\da-f]{12}`

var (
	liveMatcher   = ex.MustMatcher(`^https?://(?:www\.)?kick\.com/(?P<id>[\w-]+)/?(?:[?#]|$)`)
	vodMatcher    = ex.MustMatcher(`^https?://(?:www\.)?kick\.com/[\w-]+/videos/(?P<id>` + uuidRe + `)`)
	clipMatcher   = ex.MustMatcher(`^https?://(?:www\.)?kick\.com/[\w-]+(?:/clips/|/?\?(?:[^#]+&)?clip=)(?P<id>clip_[\w-]+)`)
	videosMatcher = ex.MustMatcher(`^https?://(?:www\.)?kick\.com/(?P<id>[\w-]+)/videos/?(?:[?#]|$)`)
)

var reserved = map[string]bool{"video": true, "categories": true, "search": true, "auth": true, "browse": true, "following": true}

func decode(b []byte, out any) error { return json.Unmarshal(b, out) }

// imp — цель имитации: глобальная (--impersonate) или Chrome по умолчанию.
func imp(env *ex.Env) string {
	if env.Impersonate != "" {
		return env.Impersonate
	}
	return "chrome"
}

// api вызывает API Kick: "v1/..." — web.kick.com, "v2/..." — kick.com.
func api(ctx context.Context, env *ex.Env, path string, body any, out any) error {
	host := "kick.com"
	if strings.HasPrefix(path, "v1/") {
		host = "web.kick.com"
	}
	h := map[string]string{"Accept": "application/json"}
	if tok, _ := url.QueryUnescape(env.Cookie(base, "session_token")); tok != "" {
		h["Authorization"] = "Bearer " + tok
	}
	req := ex.Request{URL: "https://" + host + "/api/" + path, Headers: h, Impersonate: imp(env)}
	if body != nil {
		req.Method, req.JSON = http.MethodPost, body
	}
	r, err := env.Fetch(ctx, req)
	if err != nil {
		var he *ex.HTTPError
		if errors.As(err, &he) && he.StatusCode == 404 {
			return decode(he.Body, out)
		}
		return err
	}
	return decode(r.Body, out)
}

func hlsFormats(ctx context.Context, env *ex.Env, u string) []*media.Format {
	r, err := env.Fetch(ctx, ex.Request{URL: u, Impersonate: imp(env)})
	if err != nil {
		env.Warn("kick: HLS manifest", "err", err)
		return nil
	}
	fs := hls.ParseMaster(r.Text(), u, "hls")
	for _, f := range fs {
		f.Impersonate = imp(env)
	}
	return fs
}

// --- эфир ---

// Live — прямой эфир канала.
type Live struct{}

// NewLive возвращает экстрактор эфиров Kick.
func NewLive() *Live { return &Live{} }

func (*Live) Name() string        { return liveKey }
func (*Live) Description() string { return "Kick live streams" }
func (*Live) Suitable(u string) bool {
	g, ok := liveMatcher.Groups(u)
	return ok && !reserved[strings.ToLower(g["id"])] && !clipMatcher.Match(u)
}

func (l *Live) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := liveMatcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	slug := strings.ToLower(g["id"])
	var ch any
	if err := api(ctx, env, "v2/channels/"+slug, nil, &ch); err != nil {
		return nil, ex.Wrap(liveKey, slug, err, "unable to download channel")
	}
	if ex.Get(ch, "id") == nil {
		return nil, ex.Errorf(liveKey, slug, ex.ErrUnavailable, "channel %s not found", slug)
	}
	ls := ex.Map(ch, "livestream")
	if ls == nil {
		return nil, ex.Errorf(liveKey, slug, ex.ErrUnavailable, "%s is offline", slug)
	}
	pb := ex.Str(ch, "playback_url")
	if pb == "" {
		return nil, ex.Errorf(liveKey, slug, ex.ErrNoFormats, "no playback URL")
	}
	info := &media.Info{
		Kind:        media.KindVideo,
		ID:          strconv.FormatInt(ex.Int(ls, "id"), 10),
		Title:       ex.CleanHTML(ex.Str(ls, "session_title")),
		Description: ex.CleanHTML(ex.Str(ch, "user", "bio")),
		Uploader:    ex.Str(ch, "user", "username"),
		UploaderID:  slug,
		Channel:     ex.Str(ch, "user", "username"),
		ChannelID:   strconv.FormatInt(ex.Int(ch, "id"), 10),
		IsLive:      true,
		ViewCount:   ex.Int(ls, "viewer_count"),
		Thumbnail:   ex.Str(ls, "thumbnail", "url"),
		UploadDate:  ex.UnifiedDate(ex.Str(ls, "start_time")),
		WebpageURL:  base + "/" + slug,
		Formats:     hlsFormats(ctx, env, pb),
	}
	if info.Title == "" {
		info.Title = slug
	}
	if ex.Bool(ls, "is_mature") {
		info.AgeLimit = 18
	}
	for _, c := range ex.Slice(ls, "categories") {
		if n := ex.Str(c, "name"); n != "" {
			info.Categories = append(info.Categories, n)
		}
	}
	if len(info.Formats) == 0 {
		return nil, ex.Errorf(liveKey, slug, ex.ErrNoFormats, "no formats found")
	}
	return info, nil
}

// --- запись ---

// VOD — запись трансляции.
type VOD struct{}

// NewVOD возвращает экстрактор записей Kick.
func NewVOD() *VOD { return &VOD{} }

func (*VOD) Name() string           { return vodKey }
func (*VOD) Description() string    { return "Kick VODs" }
func (*VOD) Priority() int          { return 5 }
func (*VOD) Suitable(u string) bool { return vodMatcher.Match(u) }

func (v *VOD) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := vodMatcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	id := g["id"]
	var pb any
	err := api(ctx, env, "v1/stream/"+id+"/playback", map[string]any{
		"video_player":  map[string]any{"player": map[string]any{}},
		"video_session": map[string]any{},
		"user_session":  map[string]any{"non_personalised_ads": true},
	}, &pb)
	if err != nil {
		return nil, ex.Wrap(vodKey, id, err, "unable to download playback info")
	}
	if d := ex.Map(pb, "data"); d != nil {
		return nil, ex.Errorf(vodKey, id, ex.ErrUnavailable, "%s %s", ex.Str(d, "type"), ex.Str(d, "details"))
	}
	creator := ex.Str(pb, "video_session", "creator_id")
	info := &media.Info{Kind: media.KindVideo, ID: id, ChannelID: creator, WebpageURL: rawURL}
	if creator != "" {
		var videos any
		if api(ctx, env, "v1/channels/"+creator+"/videos", nil, &videos) == nil {
			for _, vd := range ex.Slice(videos, "data") {
				if ex.Str(vd, "id") == id {
					fillVOD(info, vd)
				}
			}
		}
	}
	m3u8 := ex.Str(pb, "playback_url", "vod")
	if m3u8 == "" {
		return nil, ex.Errorf(vodKey, id, ex.ErrLoginRequired, "no playback URL (subscriber-only video? pass --cookies)")
	}
	info.Formats = hlsFormats(ctx, env, m3u8)
	if info.Title == "" {
		info.Title = id
	}
	if len(info.Formats) == 0 {
		return nil, ex.Errorf(vodKey, id, ex.ErrNoFormats, "no formats found")
	}
	return info, nil
}

func fillVOD(info *media.Info, vd any) {
	info.Title = ex.CleanHTML(ex.Str(vd, "title"))
	info.Duration = ex.Float(vd, "duration")
	info.ViewCount = ex.Int(vd, "viewer_count")
	info.UploadDate = ex.UnifiedDate(ex.Str(vd, "start_time"))
	info.Uploader = ex.Str(vd, "channel", "username")
	info.UploaderID = ex.Str(vd, "channel", "slug")
	if ex.Bool(vd, "is_mature") {
		info.AgeLimit = 18
	}
	if c := ex.Str(vd, "category", "name"); c != "" {
		info.Categories = []string{c}
	}
	// srcSet: "url 1920w, url 1280w"
	if set := ex.Str(vd, "thumbnail", "srcSet"); set != "" {
		for _, part := range strings.Split(set, ",") {
			if u, _, _ := strings.Cut(strings.TrimSpace(part), " "); u != "" {
				info.Thumbnails = append(info.Thumbnails, media.Thumbnail{URL: u})
				info.Thumbnail = u
			}
		}
	}
}

// --- клип ---

// Clip — клип Kick.
type Clip struct{}

// NewClip возвращает экстрактор клипов Kick.
func NewClip() *Clip { return &Clip{} }

func (*Clip) Name() string           { return clipKey }
func (*Clip) Description() string    { return "Kick clips" }
func (*Clip) Priority() int          { return 5 }
func (*Clip) Suitable(u string) bool { return clipMatcher.Match(u) }

func (c *Clip) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := clipMatcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	id := g["id"]
	var resp any
	if err := api(ctx, env, "v2/clips/"+id, nil, &resp); err != nil {
		return nil, ex.Wrap(clipKey, id, err, "unable to download clip")
	}
	if msg := ex.Str(resp, "message"); msg != "" && ex.Map(resp, "clip") == nil {
		return nil, ex.Errorf(clipKey, id, ex.ErrUnavailable, "%s", msg)
	}
	clip := ex.Map(resp, "clip")
	src := ex.FirstStr(clip, ex.P("playback_url"), ex.P("clip_url"), ex.P("video_url"))
	if src == "" {
		return nil, ex.Errorf(clipKey, id, ex.ErrNoFormats, "no clip source URL")
	}
	info := &media.Info{
		Kind:       media.KindVideo,
		ID:         id,
		Title:      ex.CleanHTML(ex.Str(clip, "title")),
		Duration:   ex.Float(clip, "duration"),
		Thumbnail:  ex.Str(clip, "thumbnail_url"),
		ViewCount:  ex.Int(clip, "views"),
		LikeCount:  ex.Int(clip, "likes"),
		UploadDate: ex.UnifiedDate(ex.Str(clip, "created_at")),
		Uploader:   ex.Str(clip, "channel", "username"),
		UploaderID: ex.Str(clip, "channel", "slug"),
		WebpageURL: rawURL,
	}
	if ex.URLExt(src) == "m3u8" {
		info.Formats = hlsFormats(ctx, env, src)
	} else {
		info.Formats = []*media.Format{{FormatID: "source", URL: src, Ext: "mp4", Impersonate: imp(env)}}
	}
	return info, nil
}

// --- записи канала ---

// Videos — список записей канала (kick.com/<channel>/videos[?sort=views]).
type Videos struct{}

// NewVideos возвращает экстрактор списка записей Kick.
func NewVideos() *Videos { return &Videos{} }

func (*Videos) Name() string           { return videosKey }
func (*Videos) Description() string    { return "Kick channel videos" }
func (*Videos) Priority() int          { return 5 }
func (*Videos) Suitable(u string) bool { return videosMatcher.Match(u) }

func (v *Videos) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := videosMatcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	slug := strings.ToLower(g["id"])
	var ch any
	if err := api(ctx, env, "v2/channels/"+slug, nil, &ch); err != nil {
		return nil, ex.Wrap(videosKey, slug, err, "unable to download channel")
	}
	creator := strconv.FormatInt(ex.Int(ch, "id"), 10)
	if creator == "0" {
		return nil, ex.Errorf(videosKey, slug, ex.ErrUnavailable, "channel %s not found", slug)
	}
	var videos any
	if err := api(ctx, env, "v1/channels/"+creator+"/videos", nil, &videos); err != nil {
		return nil, ex.Wrap(videosKey, slug, err, "unable to download videos")
	}
	list := ex.Slice(videos, "data")
	byViews := false
	if pu, err := url.Parse(rawURL); err == nil && pu.Query().Get("sort") == "views" {
		byViews = true
		slices.SortStableFunc(list, func(a, b any) int { return int(ex.Int(b, "viewer_count") - ex.Int(a, "viewer_count")) })
	}
	name := ex.Str(ch, "user", "username")
	if name == "" {
		name = slug
	}
	title := name + " - Videos sorted by Date"
	if byViews {
		title = name + " - Videos sorted by Views"
	}
	info := &media.Info{Kind: media.KindPlaylist, ID: slug, Title: title, WebpageURL: rawURL}
	for _, vd := range list {
		id := ex.Str(vd, "id")
		if id == "" {
			continue
		}
		e := media.NewURL(base+"/"+slug+"/videos/"+id, vodKey)
		e.ID = id
		e.Title = ex.CleanHTML(ex.Str(vd, "title"))
		e.Duration = ex.Float(vd, "duration")
		info.Entries = append(info.Entries, e)
	}
	info.PlaylistCount = len(info.Entries)
	return info, nil
}
