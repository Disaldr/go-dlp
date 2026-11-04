// Package vimeo — экстрактор Vimeo через конфиг плеера player.vimeo.com.
package vimeo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/hls"
	"github.com/Disaldr/go-dlp/media"
)

const ieKey = "vimeo"

var matcher = ex.MustMatcher(
	`^https?://player\.vimeo\.com/video/(?P<id>\d+)(?:/?\?(?:.*&)?h=(?P<hash>[\da-f]+))?`,
	`^https?://(?:www\.)?vimeo\.com/(?:(?:channels/[^/?#]+|groups/[^/?#]+/videos|album/\d+/video|showcase/\d+/video)/)?(?P<id>\d+)(?:/(?P<hash>[\da-f]{10}))?(?:[/?#]|$)`,
)

// Vimeo — экстрактор видео Vimeo.
type Vimeo struct{}

// New возвращает экстрактор Vimeo.
func New() *Vimeo { return &Vimeo{} }

func (*Vimeo) Name() string           { return ieKey }
func (*Vimeo) Description() string    { return "Vimeo videos (player config)" }
func (*Vimeo) Suitable(u string) bool { return matcher.Match(u) }

func (v *Vimeo) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := matcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	id, hash := g["id"], g["hash"]
	if hash == "" {
		if pu, err := url.Parse(rawURL); err == nil {
			hash = pu.Query().Get("h")
		}
	}
	cfgURL := "https://player.vimeo.com/video/" + id + "/config"
	if hash != "" {
		cfgURL += "?h=" + hash
	}
	referer := env.Arg(ieKey, "referer", "https://vimeo.com/"+id)
	headers := map[string]string{"Referer": referer}

	var cfg any
	var err error
	if env.Cookie("https://vimeo.com", "vimeo") != "" {
		// авторизованный путь: JWT зрителя → API → config_url (работает и для приватных видео)
		if cfg, err = configViaAPI(ctx, env, id, hash, headers); err != nil {
			env.Warn("vimeo: API extraction failed, falling back to player config", "err", err)
		}
	}
	if cfg == nil {
		err = env.GetJSON(ctx, cfgURL, headers, &cfg)
	}
	if err != nil {
		var he *ex.HTTPError
		if errors.As(err, &he) {
			msg := ex.Str(decode(he.Body), "message")
			if msg == "" {
				msg = he.Error()
			}
			kind := ex.ErrUnavailable
			if he.StatusCode == 403 {
				kind = ex.ErrLoginRequired
				msg += " (private or embed-restricted video; try extractor arg vimeo:referer=<embedding page>)"
			}
			return nil, ex.Errorf(ieKey, id, kind, "%s", msg)
		}
		return nil, ex.Wrap(ieKey, id, err, "unable to download player config")
	}

	video := ex.Map(cfg, "video")
	info := &media.Info{
		Kind:        media.KindVideo,
		ID:          id,
		Title:       ex.Str(video, "title"),
		WebpageURL:  "https://vimeo.com/" + id,
		Duration:    ex.Float(video, "duration"),
		Uploader:    ex.Str(video, "owner", "name"),
		UploaderURL: ex.Str(video, "owner", "url"),
		UploaderID:  strconv.FormatInt(ex.Int(video, "owner", "id"), 10),
		IsLive:      ex.Str(video, "live_event", "status") == "started",
		HTTPHeaders: map[string]string{"Referer": referer},
	}
	if info.UploaderID == "0" {
		info.UploaderID = ""
	}
	if u := ex.Str(video, "share_url"); u != "" {
		info.WebpageURL = u
	}
	// превью
	thumbs := ex.Map(video, "thumbs")
	keys := make([]string, 0, len(thumbs))
	for k := range thumbs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return atoi(keys[i]) < atoi(keys[j]) })
	for _, k := range keys {
		if u, ok := thumbs[k].(string); ok && strings.HasPrefix(u, "http") {
			info.Thumbnails = append(info.Thumbnails, media.Thumbnail{ID: k, URL: u, Width: atoi(k)})
			info.Thumbnail = u
		}
	}

	files := ex.Map(cfg, "request", "files")
	for _, p := range ex.Slice(files, "progressive") {
		u := ex.Str(p, "url")
		if u == "" {
			continue
		}
		q := ex.Str(p, "quality")
		info.Formats = append(info.Formats, &media.Format{
			FormatID: "http-" + q,
			URL:      u,
			Ext:      "mp4",
			Protocol: media.ProtoHTTPS,
			Width:    int(ex.Int(p, "width")),
			Height:   int(ex.Int(p, "height")),
			FPS:      ex.Float(p, "fps"),
			VCodec:   "avc1",
			ACodec:   "mp4a",
		})
	}

	hlsCfg := ex.Map(files, "hls")
	cdns := ex.Map(hlsCfg, "cdns")
	order := []string{ex.Str(hlsCfg, "default_cdn")}
	for name := range cdns {
		order = append(order, name)
	}
	for _, name := range order {
		manifest := ex.Str(cdns, name, "url")
		if manifest == "" {
			continue
		}
		body, err := env.GetText(ctx, manifest, headers)
		if err != nil {
			env.Warn("vimeo: unable to download HLS manifest", "cdn", name, "err", err)
			continue
		}
		info.Formats = append(info.Formats, hls.ParseMaster(body, manifest, "hls")...)
		break // достаточно одного CDN
	}

	for _, t := range ex.Slice(cfg, "request", "text_tracks") {
		lang := ex.Str(t, "lang")
		u := ex.Str(t, "url")
		if lang == "" || u == "" {
			continue
		}
		if info.Subtitles == nil {
			info.Subtitles = map[string][]media.Subtitle{}
		}
		info.Subtitles[lang] = append(info.Subtitles[lang], media.Subtitle{
			URL: ex.URLJoin("https://vimeo.com", u), Ext: "vtt", Name: ex.Str(t, "label"),
		})
	}

	if len(info.Formats) == 0 {
		return nil, ex.Errorf(ieKey, id, ex.ErrNoFormats, "no formats in player config")
	}
	return info, nil
}

// configViaAPI — авторизованный путь yt-dlp (web-клиент): требует cookies
// залогиненного пользователя vimeo.com.
func configViaAPI(ctx context.Context, env *ex.Env, id, hash string, headers map[string]string) (any, error) {
	var viewer any
	if err := env.GetJSON(ctx, "https://vimeo.com/_next/viewer", map[string]string{"Accept": "application/json"}, &viewer); err != nil {
		return nil, fmt.Errorf("viewer token: %w", err)
	}
	jwt := ex.Str(viewer, "jwt")
	if jwt == "" {
		return nil, errors.New("viewer token: no jwt in response")
	}
	ref := id
	if hash != "" {
		ref += ":" + hash
	}
	var video any
	err := env.GetJSON(ctx, "https://api.vimeo.com/videos/"+ref+"?fields=config_url", map[string]string{
		"Authorization":   "jwt " + jwt,
		"Accept":          "application/vnd.vimeo.*+json; version=3.4.10",
		"Accept-Language": "en",
	}, &video)
	if err != nil {
		return nil, fmt.Errorf("videos API: %w", err)
	}
	cfgURL := ex.Str(video, "config_url")
	if cfgURL == "" {
		return nil, errors.New("videos API: no config_url")
	}
	var cfg any
	if err := env.GetJSON(ctx, cfgURL, headers, &cfg); err != nil {
		return nil, fmt.Errorf("player config: %w", err)
	}
	return cfg, nil
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func decode(b []byte) any {
	var v any
	_ = json.Unmarshal(b, &v)
	return v
}
