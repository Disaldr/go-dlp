// Package dailymotion — экстрактор Dailymotion через player metadata API.
package dailymotion

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"

	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/hls"
	"github.com/Disaldr/go-dlp/media"
)

const ieKey = "dailymotion"

var matcher = ex.MustMatcher(
	`^(?i)https?://(?:(?:www|touch|geo)\.)?dailymotion\.[a-z]{2,3}/(?:(?:embed/)?video/|player(?:/\w+)?\.html\?(?:.*&)?video=)(?P<id>x[0-9a-z]+)`,
	`^(?i)https?://(?:www\.)?dai\.ly/(?P<id>x[0-9a-z]+)`,
	`^(?i)https?://geo\.dailymotion\.com/player(?:/\w+)?\.html\?(?:.*&)?video=(?P<id>x[0-9a-z]+)`,
)

// Dailymotion — экстрактор видео Dailymotion.
type Dailymotion struct{}

// New возвращает экстрактор Dailymotion.
func New() *Dailymotion { return &Dailymotion{} }

func (*Dailymotion) Name() string           { return ieKey }
func (*Dailymotion) Description() string    { return "Dailymotion videos" }
func (*Dailymotion) Suitable(u string) bool { return matcher.Match(u) }

var reH264 = regexp.MustCompile(`/H264-(\d+)x(\d+)(?:-(60)/)?`)

func (d *Dailymotion) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := matcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	id := strings.ToLower(g["id"])
	var meta any
	err := env.GetJSON(ctx, "https://www.dailymotion.com/player/metadata/video/"+id+"?app=com.dailymotion.neon", nil, &meta)
	if err != nil {
		return nil, ex.Wrap(ieKey, id, err, "unable to download metadata")
	}
	if e := ex.Map(meta, "error"); e != nil {
		msg := ex.FirstStr(e, ex.P("title"), ex.P("raw_message"), ex.P("message"))
		kind := ex.ErrUnavailable
		if ex.Str(e, "code") == "DM007" {
			kind = ex.ErrGeoRestricted
		}
		return nil, ex.Errorf(ieKey, id, kind, "dailymotion said: %s", msg)
	}

	info := &media.Info{
		Kind:       media.KindVideo,
		ID:         id,
		Title:      ex.Str(meta, "title"),
		WebpageURL: "https://www.dailymotion.com/video/" + id,
		Duration:   ex.Float(meta, "duration"),
		Timestamp:  ex.Int(meta, "created_time"),
		Uploader:   ex.FirstStr(meta, ex.P("owner", "screenname"), ex.P("owner", "username")),
		UploaderID: ex.Str(meta, "owner", "id"),
		IsLive:     ex.Bool(meta, "is_live") || ex.Bool(meta, "isOnAir"),
		Tags:       nil,
	}
	info.UploadDate = ex.UnixToDate(info.Timestamp)
	if u := ex.Str(meta, "owner", "url"); u != "" {
		info.UploaderURL = u
	}
	for _, t := range ex.Slice(meta, "tags") {
		if s, ok := t.(string); ok {
			info.Tags = append(info.Tags, s)
		}
	}
	if ex.Bool(meta, "explicit") {
		info.AgeLimit = 18
	}

	for quality, list := range ex.Map(meta, "qualities") {
		items, _ := list.([]any)
		for _, m := range items {
			u := strings.SplitN(ex.Str(m, "url"), "#", 2)[0]
			typ := ex.Str(m, "type")
			if u == "" || typ == "application/vnd.lumberjack.manifest" {
				continue
			}
			if typ == "application/x-mpegURL" {
				body, target, err := fetchManifest(ctx, env, u)
				if err != nil {
					env.Warn("dailymotion: unable to download HLS manifest", "err", err)
					continue
				}
				for _, f := range hls.ParseMaster(body, u, "hls") {
					f.URL = strings.SplitN(f.URL, "#", 2)[0]
					f.Impersonate = target
					info.Formats = append(info.Formats, f)
				}
				continue
			}
			f := &media.Format{FormatID: "http-" + quality, URL: u, Protocol: media.ProtoHTTPS, Ext: ex.URLExt(u)}
			if sm := reH264.FindStringSubmatch(u); sm != nil {
				f.Width, _ = strconv.Atoi(sm[1])
				f.Height, _ = strconv.Atoi(sm[2])
				if sm[3] != "" {
					f.FPS = 60
				}
			}
			info.Formats = append(info.Formats, f)
		}
	}
	for _, f := range info.Formats {
		if f.FPS == 0 && strings.HasSuffix(f.FormatID, "@60") {
			f.FPS = 60
		}
	}

	posters := ex.Map(meta, "posters")
	if posters == nil {
		posters = ex.Map(meta, "thumbnails")
	}
	keys := make([]string, 0, len(posters))
	for k := range posters {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return atoi(keys[i]) < atoi(keys[j]) })
	for _, k := range keys {
		if u, ok := posters[k].(string); ok && u != "" {
			info.Thumbnails = append(info.Thumbnails, media.Thumbnail{ID: k, URL: u, Height: atoi(k)})
			info.Thumbnail = u
		}
	}

	for lang, s := range ex.Map(meta, "subtitles", "data") {
		for _, u := range ex.Slice(s, "urls") {
			us, _ := u.(string)
			if us == "" {
				continue
			}
			if info.Subtitles == nil {
				info.Subtitles = map[string][]media.Subtitle{}
			}
			info.Subtitles[lang] = append(info.Subtitles[lang], media.Subtitle{URL: us, Ext: ex.URLExt(us), Name: ex.Str(s, "label")})
		}
	}

	if len(info.Formats) == 0 {
		return nil, ex.Errorf(ieKey, id, ex.ErrNoFormats, "no formats found")
	}
	return info, nil
}

// fetchManifest качает m3u8. CDN Dailymotion отвечает 403 «нестандартным»
// клиентам (yt-dlp#15526), поэтому, как и yt-dlp, повторяем запрос с имитацией
// Chrome, затем Firefox. Возвращает цель имитации, с которой запрос прошёл.
func fetchManifest(ctx context.Context, env *ex.Env, u string) (string, string, error) {
	var lastErr error
	for _, target := range []string{env.Impersonate, "chrome", "firefox"} {
		r, err := env.Fetch(ctx, ex.Request{URL: u, Impersonate: target})
		if err == nil {
			return r.Text(), target, nil
		}
		lastErr = err
		var he *ex.HTTPError
		if !errors.As(err, &he) || he.StatusCode != 403 {
			return "", "", err
		}
		env.Debug("dailymotion: manifest blocked, retrying with impersonation", "status", he.StatusCode, "next_after", target)
	}
	return "", "", lastErr
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
