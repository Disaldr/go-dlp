// Package twitch — экстракторы Twitch: записи (VOD), клипы и прямые эфиры.
// Работают через публичный GraphQL (gql.twitch.tv) и usher, как в yt-dlp.
package twitch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"strconv"
	"strings"

	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/hls"
	"github.com/Disaldr/go-dlp/media"
)

const (
	vodKey    = "twitch:vod"
	clipKey   = "twitch:clips"
	streamKey = "twitch:stream"

	gqlURL   = "https://gql.twitch.tv/gql"
	usherURL = "https://usher.ttvnw.net"
	// defaultClientID — публичный client-id веб-плеера (переопределяется twitch:client_id).
	defaultClientID = "ue6666qo983tsx6so1t0vnawi233wa"
)

// operationHashes — persisted queries веб-клиента.
var operationHashes = map[string]string{
	"ShareClipRenderStatus":       "2db6a3b20eabf510bd3cf465ae2408834b59eb6b8af89ca73ab1486cacecfb63",
	"StreamMetadata":              "ad022ca32220d5523d03a23cbcb5beaa1e0999889c1f8f78f9f2520dafb5cae6",
	"ComscoreStreamingQuery":      "e1edae8122517d013405f237ffcc124515dc6ded82480a88daef69c83b53ac01",
	"VideoPreviewOverlay":         "9515480dee68a77e667cb19de634739d33f243572b007e98e67184b1a5d8369f",
	"VideoMetadata":               "45111672eea2e507f8ba44d101a61862f9c56b11dee09a15634cb75cb9b9084d",
	"FilterableVideoTower_Videos": "67004f7881e65c297936f32c75246470629557a393788fb5a69d6d9a25a8fd5f",
}

func headers(env *ex.Env) map[string]string {
	h := map[string]string{
		"Content-Type": "text/plain;charset=UTF-8",
		"Client-ID":    env.Arg("twitch", "client_id", defaultClientID),
	}
	if tok := env.Cookie("https://gql.twitch.tv", "auth-token"); tok != "" {
		h["Authorization"] = "OAuth " + tok
	} else if tok := env.Cookie("https://www.twitch.tv", "auth-token"); tok != "" {
		h["Authorization"] = "OAuth " + tok
	}
	return h
}

type op struct {
	OperationName string         `json:"operationName"`
	Variables     map[string]any `json:"variables"`
	Extensions    map[string]any `json:"extensions"`
}

// gql выполняет набор persisted-запросов; возвращает массив ответов.
func gql(ctx context.Context, env *ex.Env, ops ...op) ([]any, error) {
	for i := range ops {
		ops[i].Extensions = map[string]any{
			"persistedQuery": map[string]any{"version": 1, "sha256Hash": operationHashes[ops[i].OperationName]},
		}
	}
	var out []any
	if err := env.PostJSON(ctx, gqlURL, headers(env), ops, &out); err != nil {
		return nil, err
	}
	return out, nil
}

type accessToken struct{ value, signature string }

// playbackToken запрашивает токен для usher. kind: "video" или "stream".
func playbackToken(ctx context.Context, env *ex.Env, kind, param, id string) (*accessToken, error) {
	method := kind + "PlaybackAccessToken"
	q := fmt.Sprintf(`{ %s(%s: %q, params: {platform: "web", playerBackend: "mediaplayer", playerType: "site"}) { value signature } }`,
		method, param, id)
	var out any
	if err := env.PostJSON(ctx, gqlURL, headers(env), map[string]any{"query": q}, &out); err != nil {
		return nil, err
	}
	t := ex.Map(out, "data", method)
	if t == nil {
		if msg := ex.Str(out, "errors", 0, "message"); msg != "" {
			return nil, errors.New(msg)
		}
		return nil, errors.New("no playback access token")
	}
	return &accessToken{value: ex.Str(t, "value"), signature: ex.Str(t, "signature")}, nil
}

// usherFormats получает master-плейлист и превращает его в форматы.
func usherFormats(ctx context.Context, env *ex.Env, ie, path, id string, tok *accessToken) ([]*media.Format, error) {
	q := url.Values{
		"allow_source":               {"true"},
		"allow_audio_only":           {"true"},
		"allow_spectre":              {"true"},
		"p":                          {strconv.Itoa(1000000 + rand.IntN(9000000))},
		"platform":                   {"web"},
		"player":                     {"twitchweb"},
		"supported_codecs":           {"h265,h264"},
		"playlist_include_framerate": {"true"},
		"sig":                        {tok.signature},
		"token":                      {tok.value},
	}
	u := fmt.Sprintf("%s/%s/%s.m3u8?%s", usherURL, path, id, q.Encode())
	r, err := env.Fetch(ctx, ex.Request{URL: u})
	if err != nil {
		var he *ex.HTTPError
		if errors.As(err, &he) && he.StatusCode == 403 {
			var info []map[string]any
			if json.Unmarshal(he.Body, &info) == nil && len(info) > 0 {
				code, _ := info[0]["error_code"].(string)
				msg, _ := info[0]["error"].(string)
				if code == "vod_manifest_restricted" || code == "unauthorized_entitlements" {
					return nil, ex.Errorf(ie, id, ex.ErrLoginRequired, "subscriber-only content; pass cookies with a subscribed account")
				}
				return nil, ex.Errorf(ie, id, ex.ErrUnavailable, "%s: %s", code, msg)
			}
		}
		return nil, ex.Wrap(ie, id, err, "unable to download HLS manifest")
	}
	formats := hls.ParseMaster(r.Text(), u, "hls")
	variants := variantNames(r.Text())
	for _, f := range formats {
		v, ok := variants[f.URL]
		if !ok {
			continue
		}
		f.FormatNote = v.name
		// группа "chunked" — исходное качество трансляции
		if v.group == "chunked" || strings.Contains(strings.ToLower(v.name), "source") {
			f.Preference = 10
			f.FormatNote = strings.TrimSpace(v.name + " (source)")
			if strings.Contains(strings.ToLower(v.name), "source") {
				f.FormatNote = v.name
			}
		}
		if v.group == "audio_only" || strings.EqualFold(strings.ReplaceAll(v.name, " ", "_"), "audio_only") {
			f.VCodec = media.CodecNone
			f.FormatID = "audio_only"
		}
	}
	return formats, nil
}

type variant struct{ name, group string }

// variantNames сопоставляет URL варианта с VIDEO-группой и её именем из
// EXT-X-MEDIA, например {"1080p60", "chunked"}.
func variantNames(body string) map[string]variant {
	groupName := map[string]string{}
	out := map[string]variant{}
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		l = strings.TrimSpace(l)
		if a, ok := strings.CutPrefix(l, "#EXT-X-MEDIA:"); ok {
			attrs := hls.ParseAttrs(a)
			if attrs["TYPE"] == "VIDEO" {
				groupName[attrs["GROUP-ID"]] = attrs["NAME"]
			}
		}
		if a, ok := strings.CutPrefix(l, "#EXT-X-STREAM-INF:"); ok && i+1 < len(lines) {
			attrs := hls.ParseAttrs(a)
			if g := attrs["VIDEO"]; g != "" {
				name := groupName[g]
				if name == "" {
					name = g
				}
				out[strings.TrimSpace(lines[i+1])] = variant{name: name, group: g}
			}
		}
	}
	return out
}

func thumbnails(u string) ([]media.Thumbnail, string) {
	if u == "" || strings.Contains(u, "/404_processing_") {
		return nil, ""
	}
	return []media.Thumbnail{{URL: u}}, u
}

// --- VOD ---

var vodMatcher = ex.MustMatcher(
	`^https?://(?:(?:www|go|m)\.)?twitch\.tv/(?:[^/]+/v(?:ideo)?|videos)/(?P<id>\d+)`,
	`^https?://player\.twitch\.tv/\?.*?\bvideo=v?(?P<id>\d+)`,
	`^https?://www\.twitch\.tv/[^/]+/schedule\?vodID=(?P<id>\d+)`,
)

// VOD — записи трансляций.
type VOD struct{}

// NewVOD возвращает экстрактор записей Twitch.
func NewVOD() *VOD { return &VOD{} }

func (*VOD) Name() string           { return vodKey }
func (*VOD) Description() string    { return "Twitch VODs" }
func (*VOD) Priority() int          { return 5 }
func (*VOD) Suitable(u string) bool { return vodMatcher.Match(u) }

func (v *VOD) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := vodMatcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	id := g["id"]
	data, err := gql(ctx, env, op{OperationName: "VideoMetadata", Variables: map[string]any{"channelLogin": "", "videoID": id}})
	if err != nil {
		return nil, ex.Wrap(vodKey, id, err, "unable to download metadata")
	}
	video := ex.Map(data, 0, "data", "video")
	if video == nil {
		return nil, ex.Errorf(vodKey, id, ex.ErrUnavailable, "video %s does not exist", id)
	}
	info := &media.Info{
		Kind:        media.KindVideo,
		ID:          "v" + id,
		Title:       ex.Str(video, "title"),
		Description: ex.Str(video, "description"),
		Duration:    ex.Float(video, "lengthSeconds"),
		Uploader:    ex.Str(video, "owner", "displayName"),
		UploaderID:  ex.Str(video, "owner", "login"),
		ViewCount:   ex.Int(video, "viewCount"),
		WebpageURL:  "https://www.twitch.tv/videos/" + id,
		UploadDate:  ex.UnifiedDate(ex.Str(video, "publishedAt")),
	}
	if info.Title == "" {
		info.Title = "Untitled Broadcast"
	}
	if info.UploaderID != "" {
		info.UploaderURL = "https://www.twitch.tv/" + info.UploaderID
	}
	thumb := ex.Str(video, "previewThumbnailURL")
	if strings.Contains(thumb, "/404_processing_") {
		info.IsLive = ex.Str(video, "broadcastType") == "ARCHIVE"
	}
	info.Thumbnails, info.Thumbnail = thumbnails(thumb)

	tok, err := playbackToken(ctx, env, "video", "id", id)
	if err != nil {
		return nil, ex.Wrap(vodKey, id, err, "unable to get access token")
	}
	if info.Formats, err = usherFormats(ctx, env, vodKey, "vod", id, tok); err != nil {
		return nil, err
	}
	return info, nil
}

// --- клипы ---

var clipMatcher = ex.MustMatcher(
	`^https?://clips\.twitch\.tv/(?:embed\?.*?\bclip=|(?:[^/]+/)*)(?P<id>[^/?#&]+)`,
	`^https?://(?:(?:www|go|m)\.)?twitch\.tv/(?:[^/]+/)?clip/(?P<id>[^/?#&]+)`,
)

// Clip — клипы Twitch.
type Clip struct{}

// NewClip возвращает экстрактор клипов Twitch.
func NewClip() *Clip { return &Clip{} }

func (*Clip) Name() string           { return clipKey }
func (*Clip) Description() string    { return "Twitch clips" }
func (*Clip) Priority() int          { return 5 }
func (*Clip) Suitable(u string) bool { return clipMatcher.Match(u) }

func (c *Clip) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := clipMatcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	slug := g["id"]
	data, err := gql(ctx, env, op{OperationName: "ShareClipRenderStatus", Variables: map[string]any{"slug": slug}})
	if err != nil {
		return nil, ex.Wrap(clipKey, slug, err, "unable to download clip metadata")
	}
	clip := ex.Map(data, 0, "data", "clip")
	if clip == nil {
		return nil, ex.Errorf(clipKey, slug, ex.ErrUnavailable, "this clip is no longer available")
	}
	access := url.Values{
		"sig":   {ex.Str(clip, "playbackAccessToken", "signature")},
		"token": {ex.Str(clip, "playbackAccessToken", "value")},
	}
	info := &media.Info{
		Kind:       media.KindVideo,
		ID:         ex.Str(clip, "id"),
		Title:      ex.Str(clip, "title"),
		Duration:   ex.Float(clip, "durationSeconds"),
		ViewCount:  ex.Int(clip, "viewCount"),
		UploadDate: ex.UnifiedDate(ex.Str(clip, "createdAt")),
		Channel:    ex.Str(clip, "broadcaster", "displayName"),
		ChannelID:  ex.Str(clip, "broadcaster", "id"),
		Uploader:   ex.Str(clip, "curator", "displayName"),
		UploaderID: ex.Str(clip, "curator", "id"),
		WebpageURL: "https://clips.twitch.tv/" + slug,
	}
	if info.ID == "" {
		info.ID = slug
	}
	if game := ex.Str(clip, "game", "displayName"); game != "" {
		info.Categories = []string{game}
	}
	for i, prefix := range []string{"", "portrait"} {
		asset := ex.Map(clip, "assets", i)
		for _, q := range ex.Slice(asset, "videoQualities") {
			src := ex.Str(q, "sourceURL")
			if src == "" {
				continue
			}
			sep := "?"
			if strings.Contains(src, "?") {
				sep = "&"
			}
			quality := ex.Str(q, "quality")
			fid := quality
			if prefix != "" {
				fid = prefix + "-" + quality
			}
			f := &media.Format{
				FormatID: fid,
				URL:      src + sep + access.Encode(),
				Ext:      "mp4",
				Height:   int(ex.Int(q, "quality")),
				FPS:      ex.Float(q, "frameRate"),
			}
			if prefix != "" {
				f.Preference = -2
			}
			info.Formats = append(info.Formats, f)
		}
		if t := ex.Str(asset, "thumbnailURL"); t != "" {
			info.Thumbnails = append(info.Thumbnails, media.Thumbnail{ID: "asset" + strconv.Itoa(i), URL: t})
		}
	}
	if t := ex.Str(clip, "thumbnailURL"); t != "" {
		info.Thumbnail = t
	}
	if len(info.Formats) == 0 {
		return nil, ex.Errorf(clipKey, slug, ex.ErrNoFormats, "no formats found")
	}
	return info, nil
}

// --- прямой эфир ---

var streamMatcher = ex.MustMatcher(
	`^https?://(?:(?:www|go|m)\.)?twitch\.tv/(?P<id>[^/#?]+)/?(?:[?#]|$)`,
	`^https?://player\.twitch\.tv/\?.*?\bchannel=(?P<id>[^/#?&]+)`,
)

// reservedPaths — разделы сайта, которые не являются каналами.
var reservedPaths = map[string]bool{
	"directory": true, "videos": true, "settings": true, "downloads": true, "jobs": true,
	"p": true, "search": true, "subscriptions": true, "turbo": true, "login": true, "signup": true,
}

// Stream — прямые эфиры каналов.
type Stream struct{}

// NewStream возвращает экстрактор прямых эфиров Twitch.
func NewStream() *Stream { return &Stream{} }

func (*Stream) Name() string        { return streamKey }
func (*Stream) Description() string { return "Twitch live streams" }
func (*Stream) Suitable(u string) bool {
	g, ok := streamMatcher.Groups(u)
	return ok && !reservedPaths[strings.ToLower(g["id"])] && !vodMatcher.Match(u) && !clipMatcher.Match(u) && !videosMatcher.Match(u)
}

func (s *Stream) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := streamMatcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	channel := strings.ToLower(g["id"])
	data, err := gql(ctx, env,
		op{OperationName: "StreamMetadata", Variables: map[string]any{"channelLogin": channel, "includeIsDJ": true}},
		op{OperationName: "ComscoreStreamingQuery", Variables: map[string]any{
			"channel": channel, "clipSlug": "", "isClip": false, "isLive": true, "isVodOrCollection": false, "vodID": "",
		}},
		op{OperationName: "VideoPreviewOverlay", Variables: map[string]any{"login": channel}},
	)
	if err != nil {
		return nil, ex.Wrap(streamKey, channel, err, "unable to download stream metadata")
	}
	user := ex.Map(data, 0, "data", "user")
	if user == nil {
		return nil, ex.Errorf(streamKey, channel, ex.ErrUnavailable, "channel %s does not exist", channel)
	}
	stream := ex.Map(user, "stream")
	if stream == nil {
		return nil, ex.Errorf(streamKey, channel, ex.ErrUnavailable, "%s is offline", channel)
	}
	tok, err := playbackToken(ctx, env, "stream", "channelName", channel)
	if err != nil {
		return nil, ex.Wrap(streamKey, channel, err, "unable to get access token")
	}
	formats, err := usherFormats(ctx, env, streamKey, "api/channel/hls", channel, tok)
	if err != nil {
		return nil, err
	}
	uploader := ex.Str(data, 1, "data", "user", "displayName")
	title := uploader
	if title == "" {
		title = channel
	}
	if t := ex.Str(stream, "type"); t == "rerun" || t == "live" {
		title += " (" + t + ")"
	}
	info := &media.Info{
		Kind:        media.KindVideo,
		ID:          ex.Str(stream, "id"),
		Title:       title,
		Description: ex.Str(data, 1, "data", "user", "broadcastSettings", "title"),
		Uploader:    uploader,
		UploaderID:  channel,
		UploaderURL: "https://www.twitch.tv/" + channel,
		ViewCount:   ex.Int(stream, "viewers"),
		IsLive:      ex.Str(stream, "type") == "live",
		UploadDate:  ex.UnifiedDate(ex.Str(stream, "createdAt")),
		WebpageURL:  "https://www.twitch.tv/" + channel,
		Formats:     formats,
	}
	if info.ID == "" {
		info.ID = channel
	}
	info.Thumbnails, info.Thumbnail = thumbnails(ex.Str(data, 2, "data", "user", "stream", "previewImageURL"))
	return info, nil
}

// --- вкладка «Видео» канала ---

const videosKey = "twitch:videos"

var videosMatcher = ex.MustMatcher(
	`^https?://(?:(?:www|go|m)\.)?twitch\.tv/(?P<id>[^/?#]+)/(?:videos|profile)(?:/all)?/?(?:[?#]|$)`,
)

var broadcastTypes = map[string]string{
	"archives": "ARCHIVE", "highlights": "HIGHLIGHT", "uploads": "UPLOAD", "past_premieres": "PAST_PREMIERE",
}

// Videos — записи канала (twitch.tv/<channel>/videos[?filter=...&sort=views]).
type Videos struct{}

// NewVideos возвращает экстрактор списка записей канала.
func NewVideos() *Videos { return &Videos{} }

func (*Videos) Name() string        { return videosKey }
func (*Videos) Description() string { return "Twitch channel videos" }
func (*Videos) Priority() int       { return 5 }
func (*Videos) Suitable(u string) bool {
	if !videosMatcher.Match(u) {
		return false
	}
	pu, err := url.Parse(u)
	if err != nil {
		return false
	}
	f := pu.Query().Get("filter")
	return f != "clips" && f != "collections"
}

func (v *Videos) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := videosMatcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	channel := strings.ToLower(g["id"])
	var q url.Values
	if pu, err := url.Parse(rawURL); err == nil {
		q = pu.Query()
	}
	vars := map[string]any{"channelOwnerLogin": channel, "videoSort": "TIME", "limit": 100}
	if bt := broadcastTypes[q.Get("filter")]; bt != "" {
		vars["broadcastType"] = bt
	}
	if q.Get("sort") == "views" {
		vars["videoSort"] = "VIEWS"
	}
	info := &media.Info{Kind: media.KindPlaylist, ID: channel, Title: channel + " - videos", WebpageURL: rawURL}
	for page := 0; page < 100; page++ {
		data, err := gql(ctx, env, op{OperationName: "FilterableVideoTower_Videos", Variables: vars})
		if err != nil {
			if len(info.Entries) > 0 {
				break
			}
			return nil, ex.Wrap(videosKey, channel, err, "unable to download videos")
		}
		user := ex.Map(data, 0, "data", "user")
		if user == nil || ex.Str(user, "id") == "" {
			return nil, ex.Errorf(videosKey, channel, ex.ErrUnavailable, "channel %q not found", channel)
		}
		cursor := ""
		for _, edge := range ex.Slice(user, "videos", "edges") {
			node := ex.Map(edge, "node")
			id := ex.Str(node, "id")
			if id == "" {
				continue
			}
			e := media.NewURL("https://www.twitch.tv/videos/"+id, vodKey)
			e.ID = "v" + id
			e.Title = ex.Str(node, "title")
			e.Duration = ex.Float(node, "lengthSeconds")
			info.Entries = append(info.Entries, e)
			cursor = ex.Str(edge, "cursor")
		}
		if cursor == "" || (env.MaxEntries > 0 && len(info.Entries) >= env.MaxEntries) {
			break
		}
		vars["cursor"] = cursor
	}
	info.PlaylistCount = len(info.Entries)
	return info, nil
}
