// Package peertube — экстрактор PeerTube (федеративный видеохостинг).
// Инстансов тысячи, поэтому URL узнаётся по характерным путям, а принадлежность
// к PeerTube подтверждается запросом к API; если API не отвечает как PeerTube,
// экстрактор возвращает ErrUnsupportedURL и URL уходит следующему экстрактору.
package peertube

import (
	"context"
	"errors"
	"strconv"
	"strings"

	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/hls"
	"github.com/Disaldr/go-dlp/media"
)

const ieKey = "peertube"

const idRe = `(?P<id>[\da-zA-Z]{22}|[\da-fA-F]{8}-[\da-fA-F]{4}-[\da-fA-F]{4}-[\da-fA-F]{4}-[\da-fA-F]{12})`

var matcher = ex.MustMatcher(
	`^https?://(?P<host>[^/?#]+)/(?:w/|videos/(?:watch|embed)/|api/v\d/videos/)`+idRe+`(?:[/?#]|$)`,
	`^peertube:(?P<host>[^:]+):`+idRe+`$`,
)

// PeerTube — экстрактор видео PeerTube.
type PeerTube struct{}

// New возвращает экстрактор PeerTube.
func New() *PeerTube { return &PeerTube{} }

func (*PeerTube) Name() string           { return ieKey }
func (*PeerTube) Description() string    { return "PeerTube videos (any instance)" }
func (*PeerTube) Priority() int          { return -10 } // после экстракторов конкретных сайтов
func (*PeerTube) Suitable(u string) bool { return matcher.Match(u) }

func (p *PeerTube) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := matcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	host, id := g["host"], g["id"]
	api := "https://" + host + "/api/v1/videos/" + id
	var v any
	if err := env.GetJSON(ctx, api, map[string]string{"Accept": "application/json"}, &v); err != nil {
		var he *ex.HTTPError
		if errors.As(err, &he) && (he.StatusCode == 401 || he.StatusCode == 403) && looksLikePeerTube(he.Body) {
			return nil, ex.Errorf(ieKey, id, ex.ErrLoginRequired, "private or internal video")
		}
		if errors.As(err, &he) && he.StatusCode == 404 && looksLikePeerTube(he.Body) {
			return nil, ex.Errorf(ieKey, id, ex.ErrUnavailable, "video not found")
		}
		// не PeerTube (или API недоступен) — пусть попробуют другие экстракторы
		return nil, ex.Errorf(ieKey, id, ex.ErrUnsupportedURL, "%s does not look like a PeerTube instance: %v", host, err)
	}
	if ex.Str(v, "uuid") == "" {
		return nil, ex.Errorf(ieKey, id, ex.ErrUnsupportedURL, "%s does not look like a PeerTube instance", host)
	}
	uuid := ex.Str(v, "uuid")
	info := &media.Info{
		Kind:        media.KindVideo,
		ID:          uuid,
		Title:       ex.Str(v, "name"),
		Description: ex.Str(v, "description"),
		Duration:    ex.Float(v, "duration"),
		ViewCount:   ex.Int(v, "views"),
		LikeCount:   ex.Int(v, "likes"),
		IsLive:      ex.Bool(v, "isLive"),
		Uploader:    ex.Str(v, "account", "displayName"),
		UploaderID:  ex.Str(v, "account", "name") + "@" + ex.Str(v, "account", "host"),
		UploaderURL: ex.Str(v, "account", "url"),
		Channel:     ex.Str(v, "channel", "displayName"),
		ChannelID:   ex.Str(v, "channel", "name") + "@" + ex.Str(v, "channel", "host"),
		ChannelURL:  ex.Str(v, "channel", "url"),
		UploadDate:  ex.UnifiedDate(ex.FirstStr(v, ex.P("originallyPublishedAt"), ex.P("publishedAt"))),
		WebpageURL:  "https://" + host + "/w/" + ex.FirstStr(v, ex.P("shortUUID"), ex.P("uuid")),
	}
	if t := ex.FirstStr(v, ex.P("previewPath"), ex.P("thumbnailPath")); t != "" {
		info.Thumbnail = ex.URLJoin("https://"+host, t)
	}
	if ex.Bool(v, "nsfw") {
		info.AgeLimit = 18
	}
	for _, t := range ex.Slice(v, "tags") {
		if s, ok := t.(string); ok {
			info.Tags = append(info.Tags, s)
		}
	}
	if c := ex.Str(v, "category", "label"); c != "" {
		info.Categories = []string{c}
	}

	addFiles := func(files []any) {
		for _, f := range files {
			u := ex.FirstStr(f, ex.P("fileUrl"), ex.P("fileDownloadUrl"))
			if u == "" {
				continue
			}
			res := int(ex.Int(f, "resolution", "id"))
			fm := &media.Format{
				FormatID: strconv.Itoa(res) + "p",
				URL:      u,
				Ext:      ex.URLExt(u),
				Height:   res,
				FPS:      ex.Float(f, "fps"),
				Filesize: ex.Int(f, "size"),
			}
			if res == 0 {
				fm.FormatID, fm.VCodec, fm.Height = "audio", media.CodecNone, 0
			}
			if hv, ok := ex.Get(f, "hasVideo").(bool); ok && !hv {
				fm.VCodec = media.CodecNone
			}
			if ha, ok := ex.Get(f, "hasAudio").(bool); ok && !ha {
				fm.ACodec = media.CodecNone
			}
			info.Formats = append(info.Formats, fm)
		}
	}
	addFiles(ex.Slice(v, "files"))
	for _, sp := range ex.Slice(v, "streamingPlaylists") {
		if pl := ex.Str(sp, "playlistUrl"); pl != "" {
			if body, err := env.GetText(ctx, pl, nil); err == nil {
				info.Formats = append(info.Formats, hls.ParseMaster(body, pl, "hls")...)
			}
		}
		addFiles(ex.Slice(sp, "files"))
	}
	// субтитры
	var caps any
	if env.GetJSON(ctx, api+"/captions", nil, &caps) == nil {
		for _, c := range ex.Slice(caps, "data") {
			u := ex.FirstStr(c, ex.P("fileUrl"))
			if u == "" {
				if cp := ex.Str(c, "captionPath"); cp != "" {
					u = ex.URLJoin("https://"+host, cp)
				}
			}
			lang := ex.Str(c, "language", "id")
			if u == "" || lang == "" {
				continue
			}
			if info.Subtitles == nil {
				info.Subtitles = map[string][]media.Subtitle{}
			}
			info.Subtitles[lang] = append(info.Subtitles[lang], media.Subtitle{URL: u, Ext: ex.URLExt(u)})
		}
	}
	if len(info.Formats) == 0 {
		return nil, ex.Errorf(ieKey, uuid, ex.ErrNoFormats, "no formats found")
	}
	return info, nil
}

// looksLikePeerTube — тело 404 похоже на ошибку API PeerTube.
func looksLikePeerTube(body []byte) bool {
	s := string(body)
	return len(s) < 4096 && (strings.Contains(s, "docs.joinpeertube.org") || strings.Contains(s, `"code":"video_not_found"`))
}
