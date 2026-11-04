// Package coub — экстрактор coub.com (зацикленные ролики с отдельной звуковой дорожкой).
package coub

import (
	"context"
	"strings"

	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/media"
)

const ieKey = "coub"

var matcher = ex.MustMatcher(
	`^(?:coub:|https?://(?:coub\.com/(?:view|embed|coubs)/|c-cdn\.coub\.com/fb-player\.swf\?.*\bcoub(?:ID|id)=))(?P<id>[\da-z]+)`,
)

// Coub — экстрактор Coub.
type Coub struct{}

// New возвращает экстрактор Coub.
func New() *Coub { return &Coub{} }

func (*Coub) Name() string           { return ieKey }
func (*Coub) Description() string    { return "Coub loops (video + separate audio)" }
func (*Coub) Suitable(u string) bool { return matcher.Match(u) }

var qualityRank = map[string]float64{"low": 0, "med": 1, "high": 2, "higher": 3}

func (c *Coub) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := matcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	id := g["id"]
	var coub any
	if err := env.GetJSON(ctx, "https://coub.com/api/v2/coubs/"+id+".json", nil, &coub); err != nil {
		if he, ok := err.(*ex.HTTPError); ok && he.StatusCode == 404 {
			return nil, ex.Errorf(ieKey, id, ex.ErrUnavailable, "coub not found")
		}
		return nil, ex.Wrap(ieKey, id, err, "unable to download coub JSON")
	}
	if e := ex.Str(coub, "error"); e != "" {
		return nil, ex.Errorf(ieKey, id, ex.ErrUnavailable, "coub said: %s", e)
	}
	info := &media.Info{
		Kind:       media.KindVideo,
		ID:         id,
		Title:      ex.Str(coub, "title"),
		Thumbnail:  ex.Str(coub, "picture"),
		Duration:   ex.Float(coub, "duration"),
		Uploader:   ex.Str(coub, "channel", "title"),
		UploaderID: ex.Str(coub, "channel", "permalink"),
		ViewCount:  ex.Int(coub, "views_count"),
		LikeCount:  ex.Int(coub, "likes_count"),
		UploadDate: ex.UnifiedDate(ex.FirstStr(coub, ex.P("published_at"), ex.P("created_at"))),
		WebpageURL: "https://coub.com/view/" + id,
	}
	if ex.Bool(coub, "age_restricted") || ex.Bool(coub, "age_restricted_by_admin") {
		info.AgeLimit = 18
	}
	for _, t := range ex.Slice(coub, "tags") {
		if s := ex.Str(t, "title"); s != "" {
			info.Tags = append(info.Tags, s)
		}
	}
	html5 := ex.Map(coub, "file_versions", "html5")
	for _, kind := range []string{"video", "audio"} {
		for q, item := range ex.Map(html5, kind) {
			u := ex.Str(item, "url")
			if u == "" {
				continue
			}
			f := &media.Format{
				FormatID: "html5-" + kind + "-" + q,
				URL:      u,
				Ext:      ex.URLExt(u),
				Filesize: ex.Int(item, "size"),
				Quality:  qualityRank[q],
				// html5-дорожки — лучший источник (как source_preference в yt-dlp)
				Preference: 2,
			}
			if kind == "audio" {
				f.VCodec = media.CodecNone
			} else {
				f.ACodec = media.CodecNone
			}
			info.Formats = append(info.Formats, f)
		}
	}
	if u := ex.Str(coub, "file_versions", "iphone", "url"); u != "" {
		info.Formats = append(info.Formats, &media.Format{FormatID: "iphone", URL: u, Ext: ex.URLExt(u), Preference: 1})
	}
	if u := ex.Str(coub, "file_versions", "mobile", "audio_url"); u != "" {
		info.Formats = append(info.Formats, &media.Format{FormatID: "mobile-audio", URL: u, Ext: ex.URLExt(u), VCodec: media.CodecNone})
	}
	for _, f := range info.Formats {
		if f.Ext == "" {
			f.Ext = "mp4"
		}
		if strings.HasSuffix(f.Ext, "mp3") {
			f.ACodec = "mp3"
		}
	}
	if len(info.Formats) == 0 {
		return nil, ex.Errorf(ieKey, id, ex.ErrNoFormats, "no formats found")
	}
	return info, nil
}
