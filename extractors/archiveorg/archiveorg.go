// Package archiveorg — экстрактор Internet Archive (archive.org): видео и аудио
// из элементов коллекций через metadata API. Каждый исходный медиафайл
// становится отдельным элементом, его производные (перекодированные копии) —
// дополнительными форматами.
package archiveorg

import (
	"context"
	"net/url"
	"path"
	"strings"

	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/media"
)

const ieKey = "archive.org"

var matcher = ex.MustMatcher(`^https?://(?:www\.)?archive\.org/(?:details|embed)/(?P<id>[^?#]+)`)

// ArchiveOrg — экстрактор archive.org.
type ArchiveOrg struct{}

// New возвращает экстрактор archive.org.
func New() *ArchiveOrg { return &ArchiveOrg{} }

func (*ArchiveOrg) Name() string           { return ieKey }
func (*ArchiveOrg) Description() string    { return "Internet Archive video and audio" }
func (*ArchiveOrg) Suitable(u string) bool { return matcher.Match(u) }

// mediaExts — расширения медиафайлов, которые считаем элементами.
var mediaExts = map[string]string{
	"mp4": "video", "m4v": "video", "ogv": "video", "webm": "video", "mkv": "video", "mpeg": "video",
	"mpg": "video", "avi": "video", "mov": "video",
	"mp3": "audio", "ogg": "audio", "flac": "audio", "m4a": "audio", "wav": "audio", "opus": "audio",
}

func (a *ArchiveOrg) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := matcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	full, _ := url.PathUnescape(strings.TrimRight(g["id"], "/"))
	identifier, only, _ := strings.Cut(full, "/")

	var meta any
	if err := env.GetJSON(ctx, "https://archive.org/metadata/"+url.PathEscape(identifier), nil, &meta); err != nil {
		return nil, ex.Wrap(ieKey, identifier, err, "unable to download metadata")
	}
	md := ex.Map(meta, "metadata")
	if md == nil {
		return nil, ex.Errorf(ieKey, identifier, ex.ErrUnavailable, "item not found or dark")
	}
	identifier = ex.Str(md, "identifier")
	base := &media.Info{
		Title:       strOrJoin(md["title"]),
		Description: ex.CleanHTML(strOrJoin(md["description"])),
		Uploader:    ex.FirstStr(md, ex.P("uploader"), ex.P("adder")),
		UploadDate:  ex.UnifiedDate(ex.FirstStr(md, ex.P("date"), ex.P("publicdate"))),
		WebpageURL:  "https://archive.org/details/" + identifier,
	}
	if ex.Bool(meta, "is_dark") {
		return nil, ex.Errorf(ieKey, identifier, ex.ErrUnavailable, "item is not available (dark)")
	}

	download := func(name string) string {
		return "https://archive.org/download/" + url.PathEscape(identifier) + "/" + escapePath(name)
	}
	files := ex.Slice(meta, "files")
	fileByName := map[string]any{}
	for _, f := range files {
		fileByName[ex.Str(f, "name")] = f
	}
	// группа = исходный файл; производные (перекодированные копии) — его форматы.
	// Исходник сам может не быть медиа (например, ISO-образ с видео-производными).
	group := func(f any) string {
		name := ex.Str(f, "name")
		if ex.Str(f, "source") == "original" {
			return name
		}
		if o := ex.Str(f, "original"); o != "" {
			// производная от производной (миниатюры видео и т.п.) — поднимаемся к исходнику
			for depth := 0; depth < 4; depth++ {
				of, ok := fileByName[o]
				if !ok || ex.Str(of, "source") == "original" || ex.Str(of, "original") == "" {
					break
				}
				o = ex.Str(of, "original")
			}
			return o
		}
		return name
	}
	byName := map[string]*media.Info{}
	var order []string
	entryFor := func(name string) *media.Info {
		if e, ok := byName[name]; ok {
			return e
		}
		f := fileByName[name]
		e := &media.Info{
			Kind:        media.KindVideo,
			ID:          identifier + "/" + name,
			Title:       ex.FirstStr(f, ex.P("title"), ex.P("name")),
			Description: base.Description,
			Uploader:    base.Uploader,
			UploadDate:  base.UploadDate,
			Duration:    ex.ParseDuration(ex.Str(f, "length")),
			WebpageURL:  base.WebpageURL + "/" + escapePath(name),
		}
		if e.Title == "" {
			e.Title = name
		}
		byName[name] = e
		order = append(order, name)
		return e
	}
	// сначала заводим элементы для групп, где есть медиафайлы
	for _, f := range files {
		if mediaExts[ext(ex.Str(f, "name"))] == "" {
			continue
		}
		g := group(f)
		if only != "" && g != only && ex.Str(f, "name") != only {
			continue
		}
		e := entryFor(g)
		if e.Duration == 0 {
			e.Duration = ex.ParseDuration(ex.Str(f, "length"))
		}
	}
	for _, f := range files {
		name := ex.Str(f, "name")
		target := byName[group(f)]
		if target == nil {
			continue
		}
		if ex.Str(f, "format") == "Thumbnail" || strings.HasSuffix(name, ".jpg") || strings.HasSuffix(name, ".png") {
			if target.Thumbnail == "" {
				target.Thumbnail = download(name)
			}
			continue
		}
		e := ext(name)
		kind := mediaExts[e]
		if kind == "" {
			if e == "vtt" || e == "srt" {
				if target.Subtitles == nil {
					target.Subtitles = map[string][]media.Subtitle{}
				}
				target.Subtitles["und"] = append(target.Subtitles["und"], media.Subtitle{URL: download(name), Ext: e})
			}
			continue
		}
		fm := &media.Format{
			FormatID:   ex.Str(f, "format"),
			FormatNote: ex.Str(f, "source"),
			URL:        download(name),
			Ext:        e,
			Filesize:   ex.Int(f, "size"),
			Width:      int(ex.Int(f, "width")),
			Height:     int(ex.Int(f, "height")),
			TBR:        ex.Float(f, "bitrate"),
		}
		if fm.FormatID == "" {
			fm.FormatID = e
		}
		fm.FormatID = strings.NewReplacer(" ", "_", "/", "_").Replace(fm.FormatID)
		if kind == "audio" {
			fm.VCodec = media.CodecNone
		}
		if ex.Str(f, "source") == "original" {
			fm.Preference = 1
		}
		target.Formats = append(target.Formats, fm)
	}
	var entries []*media.Info
	for _, n := range order {
		if e := byName[n]; len(e.Formats) > 0 {
			entries = append(entries, e)
		}
	}
	switch len(entries) {
	case 0:
		return nil, ex.Errorf(ieKey, identifier, ex.ErrNoFormats, "no video or audio files in item")
	case 1:
		e := entries[0]
		if only == "" {
			e.ID = identifier
			if base.Title != "" {
				e.Title = base.Title
			}
			e.WebpageURL = base.WebpageURL
		}
		return e, nil
	}
	return &media.Info{
		Kind: media.KindPlaylist, ID: identifier, Title: base.Title, Description: base.Description,
		Uploader: base.Uploader, WebpageURL: base.WebpageURL, Entries: entries, PlaylistCount: len(entries),
	}, nil
}

func ext(name string) string { return strings.TrimPrefix(strings.ToLower(path.Ext(name)), ".") }

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// strOrJoin — поле metadata может быть строкой или массивом строк.
func strOrJoin(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		var parts []string
		for _, x := range t {
			if s, ok := x.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}
