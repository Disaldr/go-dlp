// Package streamable — экстрактор streamable.com.
package streamable

import (
	"context"
	"strings"

	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/media"
)

const ieKey = "streamable"

var matcher = ex.MustMatcher(`^https?://streamable\.com/(?:[es]/)?(?P<id>\w+)`)

// Streamable — экстрактор видео Streamable.
type Streamable struct{}

// New возвращает экстрактор Streamable.
func New() *Streamable { return &Streamable{} }

func (*Streamable) Name() string           { return ieKey }
func (*Streamable) Description() string    { return "Streamable videos" }
func (*Streamable) Suitable(u string) bool { return matcher.Match(u) }

func (s *Streamable) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := matcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	id := g["id"]
	var v any
	if err := env.GetJSON(ctx, "https://ajax.streamable.com/videos/"+id, nil, &v); err != nil {
		if he, ok := err.(*ex.HTTPError); ok && he.StatusCode == 404 {
			return nil, ex.Errorf(ieKey, id, ex.ErrUnavailable, "video not found")
		}
		return nil, ex.Wrap(ieKey, id, err, "unable to download video JSON")
	}
	// 0 — загружается, 1 — обрабатывается, 2 — готово, 3 — ошибка
	if st := ex.Int(v, "status"); st != 2 {
		return nil, ex.Errorf(ieKey, id, ex.ErrUnavailable, "video is unavailable (status %d); it may still be uploading or processing", st)
	}
	title := ex.Str(v, "reddit_title")
	if title == "" {
		title = ex.Str(v, "title")
	}
	info := &media.Info{
		Kind:        media.KindVideo,
		ID:          id,
		Title:       title,
		Description: ex.Str(v, "description"),
		Thumbnail:   protoRelative(ex.Str(v, "thumbnail_url")),
		Uploader:    ex.Str(v, "owner", "user_name"),
		Timestamp:   ex.Int(v, "date_added"),
		Duration:    ex.Float(v, "duration"),
		ViewCount:   ex.Int(v, "plays"),
		WebpageURL:  "https://streamable.com/" + id,
	}
	info.UploadDate = ex.UnixToDate(info.Timestamp)
	for key, f := range ex.Map(v, "files") {
		u := protoRelative(ex.Str(f, "url"))
		if u == "" {
			continue
		}
		fm := &media.Format{
			FormatID: key,
			URL:      u,
			Width:    int(ex.Int(f, "width")),
			Height:   int(ex.Int(f, "height")),
			Filesize: ex.Int(f, "size"),
			FPS:      ex.Float(f, "framerate"),
			VBR:      ex.Float(f, "bitrate") / 1000,
			Ext:      ex.URLExt(u),
		}
		if vc := ex.Str(f, "input_metadata", "video_codec_name"); vc != "" {
			fm.VCodec = vc
		}
		if ac := ex.Str(f, "input_metadata", "audio_codec_name"); ac != "" {
			fm.ACodec = ac
		}
		info.Formats = append(info.Formats, fm)
	}
	if len(info.Formats) == 0 {
		return nil, ex.Errorf(ieKey, id, ex.ErrNoFormats, "no files")
	}
	return info, nil
}

func protoRelative(u string) string {
	if strings.HasPrefix(u, "//") {
		return "https:" + u
	}
	return u
}
