// Package rutube — экстракторы Rutube: видео, embed, каналы и плейлисты.
package rutube

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/hls"
	"github.com/Disaldr/go-dlp/media"
)

const (
	ieKey       = "rutube"
	playlistKey = "rutube:playlist"
	apiBase     = "https://rutube.ru/api"
	maxPages    = 200
)

var videoMatcher = ex.MustMatcher(
	`^https?://rutube\.ru/(?:(?:live/)?video(?:/private)?|(?:play/)?embed)/(?P<id>[\da-z]{32})`,
	`^https?://rutube\.ru/(?:video|play)/embed/(?P<embed>\d+)(?:[?#/]|$)`,
)

// Video — видео Rutube.
type Video struct{}

// New возвращает экстрактор видео Rutube.
func New() *Video { return &Video{} }

func (*Video) Name() string           { return ieKey }
func (*Video) Description() string    { return "Rutube videos" }
func (*Video) Suitable(u string) bool { return videoMatcher.Match(u) }

// query переносит параметры исходной ссылки (например, p= для приватных видео).
func query(rawURL string) url.Values {
	q := url.Values{}
	if pu, err := url.Parse(rawURL); err == nil {
		q = pu.Query()
	}
	q.Set("format", "json")
	return q
}

func (v *Video) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := videoMatcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	q := query(rawURL)
	id := g["id"]
	if id == "" {
		id = g["embed"]
	}

	var opts any
	if err := fetch(ctx, env, apiBase+"/play/options/"+id+"/", q, &opts); err != nil {
		return nil, apiError(id, err)
	}
	if eff := ex.Str(opts, "effective_video"); eff != "" {
		id = eff
	}
	var video any
	if err := fetch(ctx, env, apiBase+"/video/"+id+"/", q, &video); err != nil {
		return nil, apiError(id, err)
	}

	info := infoFromVideo(video, id)
	info.Kind = media.KindVideo

	balancer := ex.Map(opts, "video_balancer")
	for fid, raw := range balancer {
		u, _ := raw.(string)
		if u == "" {
			continue
		}
		// default и m3u8 обычно указывают на один и тот же поток
		if fid == "default" && ex.Str(balancer, "m3u8") != "" && ex.URLExt(u) == "m3u8" {
			continue
		}
		if ex.URLExt(u) == "m3u8" || fid == "m3u8" {
			info.Formats = append(info.Formats, hlsFormats(ctx, env, u, fid)...)
			continue
		}
		if ex.URLExt(u) == "f4m" {
			continue // Adobe HDS не поддерживается
		}
		info.Formats = append(info.Formats, &media.Format{FormatID: fid, URL: u})
	}
	for _, ls := range ex.Slice(opts, "live_streams", "hls") {
		if u := ex.Str(ls, "url"); u != "" {
			info.Formats = append(info.Formats, hlsFormats(ctx, env, u, "hls")...)
		}
	}
	for _, c := range ex.Slice(opts, "captions") {
		lang, u := ex.Str(c, "code"), ex.Str(c, "file")
		if u == "" {
			continue
		}
		if lang == "" {
			lang = "ru"
		}
		if info.Subtitles == nil {
			info.Subtitles = map[string][]media.Subtitle{}
		}
		info.Subtitles[lang] = append(info.Subtitles[lang], media.Subtitle{URL: u, Ext: ex.URLExt(u), Name: ex.Str(c, "langTitle")})
	}
	if len(info.Formats) == 0 {
		if msg := ex.FirstStr(opts, ex.P("detail", "languages", 0, "description"), ex.P("detail", "name")); msg != "" {
			return nil, ex.Errorf(ieKey, id, ex.ErrUnavailable, "%s", msg)
		}
		return nil, ex.Errorf(ieKey, id, ex.ErrNoFormats, "no formats found")
	}
	return info, nil
}

func fetch(ctx context.Context, env *ex.Env, u string, q url.Values, out any) error {
	r, err := env.Fetch(ctx, ex.Request{URL: u, Query: q, Headers: map[string]string{"Accept": "application/json"}})
	if err != nil {
		return err
	}
	return json.Unmarshal(r.Body, out)
}

func apiError(id string, err error) error {
	if he, ok := err.(*ex.HTTPError); ok {
		var body any
		_ = json.Unmarshal(he.Body, &body)
		msg := ex.FirstStr(body, ex.P("detail"), ex.P("detail", "name"), ex.P("detail", "languages", 0, "title"))
		kind := ex.ErrUnavailable
		switch he.StatusCode {
		case 401, 403:
			if strings.Contains(strings.ToLower(msg), "регион") || strings.Contains(strings.ToLower(msg), "country") {
				kind = ex.ErrGeoRestricted
			} else {
				kind = ex.ErrLoginRequired
			}
		}
		if msg == "" {
			msg = he.Error()
		}
		return ex.Errorf(ieKey, id, kind, "%s", msg)
	}
	return ex.Wrap(ieKey, id, err, "API request failed")
}

func hlsFormats(ctx context.Context, env *ex.Env, u, prefix string) []*media.Format {
	body, err := env.GetText(ctx, u, nil)
	if err != nil {
		env.Warn("rutube: unable to download HLS manifest", "err", err)
		return nil
	}
	if prefix == "m3u8" {
		prefix = "hls"
	}
	return hls.ParseMaster(body, u, prefix)
}

func infoFromVideo(v any, fallbackID string) *media.Info {
	id := ex.Str(v, "id")
	if id == "" {
		id = fallbackID
	}
	info := &media.Info{
		ID:          id,
		Title:       ex.Str(v, "title"),
		Description: ex.Str(v, "description"),
		Thumbnail:   ex.Str(v, "thumbnail_url"),
		Duration:    ex.Float(v, "duration"),
		Uploader:    ex.Str(v, "author", "name"),
		ViewCount:   ex.Int(v, "hits"),
		IsLive:      ex.Bool(v, "is_livestream"),
		WebpageURL:  ex.Str(v, "video_url"),
	}
	if info.WebpageURL == "" {
		info.WebpageURL = "https://rutube.ru/video/" + id + "/"
	}
	if aid := ex.Int(v, "author", "id"); aid != 0 {
		info.UploaderID = fmt.Sprint(aid)
		info.UploaderURL = "https://rutube.ru/channel/" + info.UploaderID + "/"
	}
	if cat := ex.Str(v, "category", "name"); cat != "" {
		info.Categories = []string{cat}
	}
	if ex.Bool(v, "is_adult") {
		info.AgeLimit = 18
	}
	if ts := ex.Str(v, "created_ts"); ts != "" {
		info.UploadDate = ex.UnifiedDate(ts)
	}
	return info
}

// --- каналы и плейлисты ---

var playlistMatcher = ex.MustMatcher(
	`^https?://rutube\.ru/channel/(?P<channel>\d+)(?:/(?P<section>videos|shorts))?/?(?:[?#]|$)`,
	`^https?://rutube\.ru/plst/(?P<plst>\d+)`,
)

// Playlist — каналы (/channel/ID[/videos|/shorts]) и плейлисты (/plst/ID).
type Playlist struct{}

// NewPlaylist возвращает экстрактор каналов и плейлистов Rutube.
func NewPlaylist() *Playlist { return &Playlist{} }

func (*Playlist) Name() string           { return playlistKey }
func (*Playlist) Description() string    { return "Rutube channels and playlists" }
func (*Playlist) Suitable(u string) bool { return playlistMatcher.Match(u) }

func (p *Playlist) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := playlistMatcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	var pageURL func(n int) string
	info := &media.Info{Kind: media.KindPlaylist, WebpageURL: rawURL}
	if id := g["plst"]; id != "" {
		info.ID = id
		pageURL = func(n int) string {
			return fmt.Sprintf("%s/playlist/custom/%s/videos?page=%d&format=json", apiBase, id, n)
		}
		var meta any
		if err := fetch(ctx, env, apiBase+"/playlist/custom/"+id+"/", url.Values{"format": {"json"}}, &meta); err == nil {
			info.Title = ex.Str(meta, "title")
			info.Description = ex.Str(meta, "description")
		}
	} else {
		id := g["channel"]
		section := g["section"]
		origin := map[string]string{"videos": "rtb,rst,ifrm,rspa", "shorts": "rshorts", "": ""}[section]
		info.ID = id
		if section != "" {
			info.ID += "_" + section
		}
		pageURL = func(n int) string {
			return fmt.Sprintf("%s/video/person/%s/?page=%d&format=json&origin__type=%s", apiBase, id, n, origin)
		}
	}

	next := pageURL(1)
	for page := 1; next != "" && page <= maxPages; page++ {
		var data any
		if err := env.GetJSON(ctx, next, nil, &data); err != nil {
			if len(info.Entries) > 0 {
				env.Warn("rutube: stopping pagination", "err", err)
				break
			}
			return nil, apiError(info.ID, err)
		}
		for _, r := range ex.Slice(data, "results") {
			vu := ex.Str(r, "video_url")
			if vu == "" {
				continue
			}
			e := infoFromVideo(r, "")
			e.Kind, e.URL, e.IEKey = media.KindURL, vu, ieKey
			info.Entries = append(info.Entries, e)
			if info.Title == "" {
				info.Title = ex.Str(r, "author", "name")
			}
		}
		if env.MaxEntries > 0 && len(info.Entries) >= env.MaxEntries {
			break
		}
		next = ""
		if ex.Bool(data, "has_next") {
			next = ex.Str(data, "next")
			if next == "" {
				next = pageURL(page + 1)
			}
		}
	}
	if info.Title == "" {
		info.Title = info.ID
	}
	info.PlaylistCount = len(info.Entries)
	return info, nil
}
