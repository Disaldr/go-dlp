// Package bluesky — экстрактор видео из постов Bluesky (публичный AppView API).
package bluesky

import (
	"context"
	"net/url"
	"path"
	"strings"

	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/hls"
	"github.com/Disaldr/go-dlp/media"
)

const (
	ieKey   = "bluesky"
	apiBase = "https://public.api.bsky.app/xrpc/"
)

var matcher = ex.MustMatcher(
	`^https?://(?:www\.)?(?:bsky\.app|main\.bsky\.dev)/profile/(?P<handle>[\w.:%-]+)/post/(?P<id>\w+)`,
	`^at://(?P<handle>[\w.:%-]+)/app\.bsky\.feed\.post/(?P<id>\w+)`,
)

// Bluesky — экстрактор постов Bluesky.
type Bluesky struct{}

// New возвращает экстрактор Bluesky.
func New() *Bluesky { return &Bluesky{} }

func (*Bluesky) Name() string           { return ieKey }
func (*Bluesky) Description() string    { return "Bluesky posts with video (and quoted/linked media)" }
func (*Bluesky) Suitable(u string) bool { return matcher.Match(u) }

func (b *Bluesky) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := matcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	handle, _ := url.PathUnescape(g["handle"])
	id := g["id"]
	var resp any
	q := url.Values{
		"uri":          {"at://" + handle + "/app.bsky.feed.post/" + id},
		"depth":        {"0"},
		"parentHeight": {"0"},
	}
	if err := env.GetJSON(ctx, apiBase+"app.bsky.feed.getPostThread?"+q.Encode(), nil, &resp); err != nil {
		if he, ok := err.(*ex.HTTPError); ok && (he.StatusCode == 400 || he.StatusCode == 404) {
			return nil, ex.Errorf(ieKey, id, ex.ErrUnavailable, "post not found")
		}
		return nil, ex.Wrap(ieKey, id, err, "unable to download post")
	}
	post := ex.Map(resp, "thread", "post")
	if post == nil {
		return nil, ex.Errorf(ieKey, id, ex.ErrUnavailable, "post not found (%s)", ex.Str(resp, "thread", "$type"))
	}

	var entries []*media.Info
	// обычное видео / внешняя ссылка
	entries = append(entries, b.videos(ctx, env, post, id, []any{"embed"}, []any{"record", "embed"})...)
	// видео вместе с цитатой (recordWithMedia)
	entries = append(entries, b.videos(ctx, env, post, id, []any{"embed", "media"}, []any{"record", "embed", "media"})...)
	// процитированный пост с видео
	if nested := ex.Map(post, "embed", "record", "record"); nested != nil && ex.Map(nested, "embeds", 0) != nil {
		entries = append(entries, b.videos(ctx, env, nested, id, []any{"embeds", 0}, []any{"value", "embed"})...)
	} else if nested := ex.Map(post, "embed", "record"); nested != nil && ex.Map(nested, "embeds", 0) != nil {
		entries = append(entries, b.videos(ctx, env, nested, id, []any{"embeds", 0}, []any{"value", "embed"})...)
	}
	switch len(entries) {
	case 0:
		return nil, ex.Errorf(ieKey, id, ex.ErrNoFormats, "no video could be found in this post")
	case 1:
		return entries[0], nil
	}
	return &media.Info{Kind: media.KindPlaylist, ID: id, Title: entries[0].Title, Entries: entries, PlaylistCount: len(entries), WebpageURL: rawURL}, nil
}

func (b *Bluesky) videos(ctx context.Context, env *ex.Env, root map[string]any, id string, embedPath, recordPath []any) []*media.Info {
	var out []*media.Info
	embed := ex.Map(root, embedPath...)
	if ext := ex.Str(embed, "external", "uri"); ext != "" {
		out = append(out, media.NewURL(ext, ""))
	}
	playlist := ex.Str(embed, "playlist")
	if playlist == "" {
		return out
	}
	record := ex.Map(root, recordPath...)
	author := ex.Map(root, "author")
	text := ex.Str(root, "record", "text")
	if text == "" {
		text = ex.Str(root, "value", "text")
	}
	info := &media.Info{
		Kind:        media.KindVideo,
		ID:          path.Base(ex.Str(root, "uri")),
		Title:       truncate(strings.ReplaceAll(text, "\n", " "), 72),
		Description: text,
		Thumbnail:   ex.Str(embed, "thumbnail"),
		Uploader:    ex.Str(author, "displayName"),
		UploaderID:  ex.Str(author, "handle"),
		ChannelID:   ex.Str(author, "did"),
		LikeCount:   ex.Int(root, "likeCount"),
		UploadDate:  ex.UnifiedDate(ex.Str(root, "indexedAt")),
	}
	if info.ID == "." || info.ID == "" {
		info.ID = id
	}
	if info.UploaderID != "" {
		info.UploaderURL = "https://bsky.app/profile/" + info.UploaderID
		info.WebpageURL = info.UploaderURL + "/post/" + info.ID
	}
	if info.Title == "" {
		info.Title = info.ID
	}
	for _, l := range ex.Slice(root, "labels") {
		switch ex.Str(l, "val") {
		case "sexual", "porn", "graphic-media":
			info.AgeLimit = 18
		}
	}
	if body, err := env.GetText(ctx, playlist, nil); err == nil {
		info.Formats = append(info.Formats, hls.ParseMaster(body, playlist, "hls")...)
	} else {
		env.Warn("bluesky: HLS playlist", "err", err)
	}
	// оригинальный файл — blob в персональном сервере (PDS) автора
	did := ex.Str(author, "did")
	cid := ex.Str(embed, "cid")
	if cid == "" {
		cid = ex.Str(record, "video", "ref", "$link")
	}
	if did != "" && cid != "" {
		endpoint := pds(ctx, env, did)
		blob := endpoint + "/xrpc/com.atproto.sync.getBlob?" + url.Values{"did": {did}, "cid": {cid}}.Encode()
		f := &media.Format{
			FormatID: "blob",
			URL:      blob,
			Ext:      ex.MimeToExt(ex.Str(record, "video", "mimeType")),
			Width:    int(ex.Int(embed, "aspectRatio", "width")),
			Height:   int(ex.Int(embed, "aspectRatio", "height")),
			Filesize: ex.Int(record, "video", "size"),
			Quality:  1,
		}
		if f.Ext == "" {
			f.Ext = "mp4"
		}
		info.Formats = append(info.Formats, f)
		for _, c := range ex.Slice(record, "captions") {
			ccid := ex.Str(c, "file", "ref", "$link")
			if ccid == "" {
				continue
			}
			lang := ex.Str(c, "lang")
			if lang == "" {
				lang = "und"
			}
			if info.Subtitles == nil {
				info.Subtitles = map[string][]media.Subtitle{}
			}
			info.Subtitles[lang] = append(info.Subtitles[lang], media.Subtitle{
				URL: endpoint + "/xrpc/com.atproto.sync.getBlob?" + url.Values{"did": {did}, "cid": {ccid}}.Encode(),
				Ext: "vtt",
			})
		}
	}
	if len(info.Formats) > 0 {
		out = append(out, info)
	}
	return out
}

// pds находит персональный сервер данных автора по DID.
func pds(ctx context.Context, env *ex.Env, did string) string {
	u := "https://plc.directory/" + did
	if web, ok := strings.CutPrefix(did, "did:web:"); ok {
		u = "https://" + web + "/.well-known/did.json"
	}
	var doc any
	if err := env.GetJSON(ctx, u, nil, &doc); err == nil {
		for _, s := range ex.Slice(doc, "service") {
			if ex.Str(s, "type") == "AtprotoPersonalDataServer" {
				if ep := ex.Str(s, "serviceEndpoint"); ep != "" {
					return strings.TrimRight(ep, "/")
				}
			}
		}
	}
	return "https://bsky.social"
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-3]) + "..."
}
