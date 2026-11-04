// Package bandcamp — экстракторы Bandcamp: треки и альбомы (поток mp3-128).
package bandcamp

import (
	"context"
	"encoding/json"
	"html"
	"regexp"
	"strconv"
	"strings"

	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/media"
)

const (
	trackKey = "bandcamp"
	albumKey = "bandcamp:album"
)

var (
	trackMatcher = ex.MustMatcher(`^https?://(?P<uploader>[^/]+)\.bandcamp\.com/track/(?P<id>[^/?#&]+)`)
	albumMatcher = ex.MustMatcher(`^https?://(?:(?P<uploader>[^.]+)\.)?bandcamp\.com/album/(?P<id>[^/?#&]+)`)
)

var reDataAttr = regexp.MustCompile(`data-(tralbum|embed)=(?:"([^"]+)"|'([^']+)')`)

// dataAttrs извлекает JSON из атрибутов data-tralbum / data-embed.
func dataAttrs(page string) map[string]any {
	out := map[string]any{}
	for _, m := range reDataAttr.FindAllStringSubmatch(page, -1) {
		var v any
		if json.Unmarshal([]byte(html.UnescapeString(m[2]+m[3])), &v) == nil {
			out[m[1]] = v
		}
	}
	return out
}

// Track — трек Bandcamp.
type Track struct{}

// NewTrack возвращает экстрактор треков Bandcamp.
func NewTrack() *Track { return &Track{} }

func (*Track) Name() string           { return trackKey }
func (*Track) Description() string    { return "Bandcamp tracks" }
func (*Track) Suitable(u string) bool { return trackMatcher.Match(u) }

func (t *Track) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := trackMatcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	slug := g["id"]
	page, err := env.GetText(ctx, rawURL, nil)
	if err != nil {
		return nil, ex.Wrap(trackKey, slug, err, "unable to download page")
	}
	data := dataAttrs(page)
	tralbum := data["tralbum"]
	if tralbum == nil {
		return nil, ex.Errorf(trackKey, slug, ex.ErrUnavailable, "no track data on page")
	}
	ti := ex.Map(tralbum, "trackinfo", 0)
	id := strconv.FormatInt(ex.Int(ti, "track_id"), 10)
	if id == "0" {
		id = strconv.FormatInt(ex.Int(ti, "id"), 10)
	}
	artist := ex.FirstStr(data["embed"], ex.P("artist"))
	if artist == "" {
		artist = ex.FirstStr(tralbum, ex.P("current", "artist"), ex.P("artist"))
	}
	title := ex.Str(ti, "title")
	info := &media.Info{
		Kind:       media.KindVideo,
		ID:         id,
		Title:      title,
		Uploader:   artist,
		UploaderID: g["uploader"],
		Duration:   ex.Float(ti, "duration"),
		Thumbnail:  ex.MetaContent(page, "og:image"),
		UploadDate: ex.UnifiedDate(ex.FirstStr(tralbum, ex.P("current", "publish_date"), ex.P("album_publish_date"))),
		WebpageURL: rawURL,
	}
	if artist != "" && title != "" {
		info.Title = artist + " - " + title
	}
	for fid, raw := range ex.Map(ti, "file") {
		u, _ := raw.(string)
		if u == "" {
			continue
		}
		if strings.HasPrefix(u, "//") {
			u = "https:" + u
		}
		ext, abr, _ := strings.Cut(fid, "-")
		f := &media.Format{FormatID: fid, URL: u, Ext: ext, VCodec: media.CodecNone, ACodec: ext}
		f.ABR, _ = strconv.ParseFloat(abr, 64)
		info.Formats = append(info.Formats, f)
	}
	if len(info.Formats) == 0 {
		return nil, ex.Errorf(trackKey, id, ex.ErrNoFormats, "track is not streamable (purchase may be required)")
	}
	return info, nil
}

// Album — альбом Bandcamp.
type Album struct{}

// NewAlbum возвращает экстрактор альбомов Bandcamp.
func NewAlbum() *Album { return &Album{} }

func (*Album) Name() string           { return albumKey }
func (*Album) Description() string    { return "Bandcamp albums" }
func (*Album) Suitable(u string) bool { return albumMatcher.Match(u) }

func (a *Album) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := albumMatcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	id := g["id"]
	page, err := env.GetText(ctx, rawURL, nil)
	if err != nil {
		return nil, ex.Wrap(albumKey, id, err, "unable to download page")
	}
	tralbum := dataAttrs(page)["tralbum"]
	tracks := ex.Slice(tralbum, "trackinfo")
	if len(tracks) == 0 {
		return nil, ex.Errorf(albumKey, id, ex.ErrUnavailable, "the page doesn't contain any tracks")
	}
	info := &media.Info{
		Kind:        media.KindPlaylist,
		ID:          id,
		Title:       ex.Str(tralbum, "current", "title"),
		Description: ex.Str(tralbum, "current", "about"),
		Uploader:    ex.Str(tralbum, "artist"),
		UploaderID:  g["uploader"],
		WebpageURL:  rawURL,
	}
	for _, t := range tracks {
		link := ex.Str(t, "title_link")
		// только треки с длительностью доступны для прослушивания
		if link == "" || ex.Float(t, "duration") == 0 {
			continue
		}
		e := media.NewURL(ex.URLJoin(rawURL, link), trackKey)
		e.ID = strconv.FormatInt(ex.Int(t, "track_id"), 10)
		e.Title = ex.Str(t, "title")
		e.Duration = ex.Float(t, "duration")
		info.Entries = append(info.Entries, e)
	}
	info.PlaylistCount = len(info.Entries)
	return info, nil
}
