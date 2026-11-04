// Package soundcloud — экстракторы SoundCloud: треки, плейлисты (sets)
// и треки пользователя. Работают через api-v2 с публичным client_id,
// который извлекается из JS-бандла сайта (как в yt-dlp).
package soundcloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"

	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/media"
)

const (
	trackKey    = "soundcloud"
	setKey      = "soundcloud:set"
	userKey     = "soundcloud:user"
	apiBase     = "https://api-v2.soundcloud.com/"
	siteBase    = "https://soundcloud.com/"
	maxUserPage = 100
)

// --- client_id ---

var (
	clientMu sync.Mutex
	clientID string
	reScript = regexp.MustCompile(`<script[^>]+src="([^"]+)"`)
	reCID    = regexp.MustCompile(`client_id\s*:\s*"([0-9a-zA-Z]{32})"`)
)

func getClientID(ctx context.Context, env *ex.Env, refresh bool) (string, error) {
	if id := env.Arg("soundcloud", "client_id", ""); id != "" {
		return id, nil
	}
	clientMu.Lock()
	defer clientMu.Unlock()
	if clientID != "" && !refresh {
		return clientID, nil
	}
	page, err := env.GetText(ctx, siteBase, nil)
	if err != nil {
		return "", fmt.Errorf("download main page: %w", err)
	}
	scripts := reScript.FindAllStringSubmatch(page, -1)
	// client_id обычно в последних бандлах
	for i := len(scripts) - 1; i >= 0; i-- {
		js, err := env.GetText(ctx, scripts[i][1], nil)
		if err != nil {
			continue
		}
		if m := reCID.FindStringSubmatch(js); m != nil {
			clientID = m[1]
			return clientID, nil
		}
	}
	return "", errors.New("unable to extract client_id")
}

// api вызывает api-v2; при 401/403 обновляет client_id и повторяет.
func api(ctx context.Context, env *ex.Env, u string, q url.Values, out any) error {
	for attempt := 0; attempt < 2; attempt++ {
		cid, err := getClientID(ctx, env, attempt > 0)
		if err != nil {
			return err
		}
		qq := url.Values{}
		for k, v := range q {
			qq[k] = v
		}
		qq.Set("client_id", cid)
		h := map[string]string{}
		if tok := env.Cookie(siteBase, "oauth_token"); tok != "" {
			h["Authorization"] = "OAuth " + tok
		}
		r, err := env.Fetch(ctx, ex.Request{URL: u, Query: qq, Headers: h})
		if err != nil {
			var he *ex.HTTPError
			if errors.As(err, &he) && (he.StatusCode == 401 || he.StatusCode == 403) && attempt == 0 {
				continue
			}
			return err
		}
		return json.Unmarshal(r.Body, out)
	}
	return errors.New("unreachable")
}

func resolve(ctx context.Context, env *ex.Env, permalink string, out any) error {
	return api(ctx, env, apiBase+"resolve", url.Values{"url": {permalink}}, out)
}

func apiErr(ie, id string, err error) error {
	var he *ex.HTTPError
	if errors.As(err, &he) && he.StatusCode == 404 {
		return ex.Errorf(ie, id, ex.ErrUnavailable, "not found")
	}
	return ex.Wrap(ie, id, err, "API request failed")
}

// --- треки ---

var trackMatcher = ex.MustMatcher(
	`^(?:https?://)?(?:www\.|m\.)?soundcloud\.com/(?P<uploader>[\w-]+)/(?P<title>[\w-]+)(?:/(?P<token>s-[\w-]+))?/?(?:[?#].*)?$`,
	`^https?://api(?:-v2)?\.soundcloud\.com/tracks/(?:soundcloud%3Atracks%3A)?(?P<track_id>\d+)(?:/?\?secret_token=(?P<secret>[^&]+))?`,
)

// второй сегмент, который не является треком
var notTrack = map[string]bool{
	"tracks": true, "albums": true, "sets": true, "reposts": true, "likes": true,
	"spotlight": true, "comments": true, "popular-tracks": true, "followers": true, "following": true,
}

// Track — трек SoundCloud.
type Track struct{}

// New возвращает экстрактор треков SoundCloud.
func New() *Track { return &Track{} }

func (*Track) Name() string        { return trackKey }
func (*Track) Description() string { return "SoundCloud tracks" }
func (*Track) Suitable(u string) bool {
	g, ok := trackMatcher.Groups(u)
	return ok && !notTrack[g["title"]] && g["uploader"] != "stations"
}

func (t *Track) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := trackMatcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	var data any
	var err error
	secret := g["secret"]
	if id := g["track_id"]; id != "" {
		q := url.Values{}
		if secret != "" {
			q.Set("secret_token", secret)
		}
		err = api(ctx, env, apiBase+"tracks/"+id, q, &data)
	} else {
		secret = g["token"]
		path := g["uploader"] + "/" + g["title"]
		if secret != "" {
			path += "/" + secret
		}
		err = resolve(ctx, env, siteBase+path, &data)
	}
	if err != nil {
		return nil, apiErr(trackKey, g["title"]+g["track_id"], err)
	}
	return trackInfo(ctx, env, data, secret)
}

var reAbr = regexp.MustCompile(`(\d+)k$`)

func trackInfo(ctx context.Context, env *ex.Env, data any, secret string) (*media.Info, error) {
	id := strconv.FormatInt(ex.Int(data, "id"), 10)
	info := &media.Info{
		Kind:        media.KindVideo,
		ID:          id,
		Title:       ex.Str(data, "title"),
		Description: ex.Str(data, "description"),
		Duration:    ex.Float(data, "duration") / 1000,
		Uploader:    ex.Str(data, "user", "username"),
		UploaderID:  strconv.FormatInt(ex.Int(data, "user", "id"), 10),
		UploaderURL: ex.Str(data, "user", "permalink_url"),
		WebpageURL:  ex.Str(data, "permalink_url"),
		ViewCount:   ex.Int(data, "playback_count"),
		LikeCount:   ex.Int(data, "likes_count"),
		UploadDate:  ex.UnifiedDate(ex.Str(data, "created_at")),
	}
	if g := ex.Str(data, "genre"); g != "" {
		info.Categories = []string{g}
	}
	if art := ex.Str(data, "artwork_url"); art != "" {
		info.Thumbnail = strings.Replace(art, "-large.", "-original.", 1)
		info.Thumbnails = []media.Thumbnail{{ID: "large", URL: art}, {ID: "original", URL: info.Thumbnail}}
	}

	q := url.Values{}
	if secret != "" {
		q.Set("secret_token", secret)
	}
	if auth := ex.Str(data, "track_authorization"); auth != "" {
		q.Set("track_authorization", auth)
	}
	drm := false
	seen := map[string]bool{}
	for _, tr := range ex.Slice(data, "media", "transcodings") {
		turl, preset := ex.Str(tr, "url"), ex.Str(tr, "preset")
		if turl == "" || preset == "" {
			continue
		}
		proto := ex.Str(tr, "format", "protocol")
		if proto == "" {
			proto = "http"
		}
		switch {
		case strings.HasPrefix(proto, "ctr-") || strings.HasPrefix(proto, "cbc-"):
			drm = true
			continue
		case proto == "progressive":
			proto = "http"
		case proto == "encrypted-hls" || strings.Contains(turl, "/encrypted-hls"):
			proto = "hls-aes"
		case proto != "hls" && strings.Contains(turl, "/hls"):
			proto = "hls"
		}
		base, _, _ := strings.Cut(preset, "_")
		if base == "abr" {
			continue // сломанный adaptive-формат, yt-dlp его тоже пропускает
		}
		var stream any
		if err := api(ctx, env, turl, q, &stream); err != nil {
			env.Warn("soundcloud: transcoding unavailable", "preset", preset, "proto", proto, "err", err)
			continue
		}
		su := ex.Str(stream, "url")
		if su == "" || seen[su] {
			continue
		}
		seen[su] = true
		mime := ex.Str(tr, "format", "mime_type")
		codec := ex.SearchRegex(mime, `codecs="([^"]+)"`)
		ext := map[string]string{"mp4a": "m4a", "opus": "opus"}[prefix4(codec)]
		if ext == "" {
			if ext = ex.MimeToExt(mime); ext == "" || ext == "m3u8" {
				ext = base
			}
		}
		premium := ex.Str(tr, "quality") == "hq"
		var abr float64
		if m := reAbr.FindStringSubmatch(preset); m != nil {
			abr, _ = strconv.ParseFloat(m[1], 64)
		}
		preview := ex.Bool(tr, "snipped") || strings.Contains(turl, "/preview/")
		f := &media.Format{
			FormatID: proto + "_" + preset,
			URL:      su,
			Ext:      ext,
			VCodec:   media.CodecNone,
			ACodec:   codec,
			ABR:      abr,
			Protocol: media.ProtoHTTPS,
		}
		if f.ACodec == "" {
			f.ACodec = base
		}
		if proto == "hls" || proto == "hls-aes" {
			f.Protocol = media.ProtoHLS
		}
		switch {
		case premium:
			f.Quality, f.FormatNote = 5, "Premium"
		case abr >= 160:
			f.Quality = 0
		default:
			f.Quality = -1
		}
		if preview {
			f.FormatID += "_preview"
			f.Preference = -10
		}
		info.Formats = append(info.Formats, f)
	}
	if len(info.Formats) == 0 {
		switch {
		case drm:
			return nil, ex.Errorf(trackKey, id, ex.ErrUnavailable, "track is DRM protected")
		case ex.Str(data, "policy") == "BLOCK":
			return nil, ex.Errorf(trackKey, id, ex.ErrGeoRestricted, "track is not available in your country")
		}
		return nil, ex.Errorf(trackKey, id, ex.ErrNoFormats, "no formats found")
	}
	return info, nil
}

func prefix4(s string) string {
	if len(s) >= 4 {
		return s[:4]
	}
	return s
}

// --- плейлисты (sets) ---

var setMatcher = ex.MustMatcher(
	`^https?://(?:(?:www|m)\.)?soundcloud\.com/(?P<uploader>[\w-]+)/sets/(?P<slug>[:\w-]+)(?:/(?P<token>[^?/]+))?`,
)

// Set — плейлист/альбом SoundCloud.
type Set struct{}

// NewSet возвращает экстрактор плейлистов SoundCloud.
func NewSet() *Set { return &Set{} }

func (*Set) Name() string           { return setKey }
func (*Set) Description() string    { return "SoundCloud sets (playlists, albums)" }
func (*Set) Priority() int          { return 5 }
func (*Set) Suitable(u string) bool { return setMatcher.Match(u) }

func (s *Set) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := setMatcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	path := g["uploader"] + "/sets/" + g["slug"]
	if g["token"] != "" {
		path += "/" + g["token"]
	}
	var pl any
	if err := resolve(ctx, env, siteBase+path, &pl); err != nil {
		return nil, apiErr(setKey, g["slug"], err)
	}
	id := strconv.FormatInt(ex.Int(pl, "id"), 10)
	info := &media.Info{
		Kind:        media.KindPlaylist,
		ID:          id,
		Title:       ex.Str(pl, "title"),
		Description: ex.Str(pl, "description"),
		Uploader:    ex.Str(pl, "user", "username"),
		WebpageURL:  ex.Str(pl, "permalink_url"),
	}
	tracks := ex.Slice(pl, "tracks")
	// в ответе полные данные только у первых треков — остальные догружаем по id
	var missing []string
	for _, t := range tracks {
		if ex.Str(t, "permalink_url") == "" {
			missing = append(missing, strconv.FormatInt(ex.Int(t, "id"), 10))
		}
	}
	full := map[int64]any{}
	for i := 0; i < len(missing); i += 50 {
		end := min(i+50, len(missing))
		q := url.Values{"ids": {strings.Join(missing[i:end], ",")}, "playlistId": {id}}
		if g["token"] != "" {
			q.Set("playlistSecretToken", g["token"])
		}
		var batch []any
		if err := api(ctx, env, apiBase+"tracks", q, &batch); err != nil {
			env.Warn("soundcloud: unable to fetch playlist tracks", "err", err)
			break
		}
		for _, t := range batch {
			full[ex.Int(t, "id")] = t
		}
	}
	for _, t := range tracks {
		if f, ok := full[ex.Int(t, "id")]; ok {
			t = f
		}
		if e := trackEntry(t, g["token"]); e != nil {
			info.Entries = append(info.Entries, e)
		}
	}
	info.PlaylistCount = len(info.Entries)
	return info, nil
}

func trackEntry(t any, secret string) *media.Info {
	tid := ex.Int(t, "id")
	if tid == 0 {
		return nil
	}
	u := ex.Str(t, "permalink_url")
	if u == "" {
		u = apiBase + "tracks/" + strconv.FormatInt(tid, 10)
		if secret != "" {
			u += "?secret_token=" + secret
		}
	}
	e := media.NewURL(u, trackKey)
	e.ID = strconv.FormatInt(tid, 10)
	e.Title = ex.Str(t, "title")
	e.Duration = ex.Float(t, "duration") / 1000
	e.Uploader = ex.Str(t, "user", "username")
	return e
}

// --- пользователи ---

var userMatcher = ex.MustMatcher(
	`^https?://(?:(?:www|m)\.)?soundcloud\.com/(?P<user>[\w-]+)/?(?P<rsrc>tracks|albums|sets|reposts|likes|spotlight|popular-tracks)?/?(?:[?#].*)?$`,
)

var userEndpoints = map[string]string{
	"":               "users/%d/tracks",
	"tracks":         "users/%d/tracks",
	"popular-tracks": "users/%d/toptracks",
	"albums":         "users/%d/albums",
	"sets":           "users/%d/playlists",
	"likes":          "users/%d/likes",
	"reposts":        "stream/users/%d/reposts",
	"spotlight":      "users/%d/spotlight",
}

var reservedUsers = map[string]bool{"discover": true, "stream": true, "upload": true, "you": true, "search": true, "stations": true, "pages": true, "charts": true}

// User — треки/плейлисты/лайки пользователя.
type User struct{}

// NewUser возвращает экстрактор пользователей SoundCloud.
func NewUser() *User { return &User{} }

func (*User) Name() string        { return userKey }
func (*User) Description() string { return "SoundCloud users (tracks, albums, sets, likes, reposts)" }
func (*User) Suitable(u string) bool {
	g, ok := userMatcher.Groups(u)
	return ok && !reservedUsers[g["user"]]
}

func (us *User) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := userMatcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	var user any
	if err := resolve(ctx, env, siteBase+g["user"], &user); err != nil {
		return nil, apiErr(userKey, g["user"], err)
	}
	uid := ex.Int(user, "id")
	rsrc := g["rsrc"]
	info := &media.Info{
		Kind:        media.KindPlaylist,
		ID:          strconv.FormatInt(uid, 10),
		Title:       ex.Str(user, "username"),
		Uploader:    ex.Str(user, "username"),
		UploaderURL: ex.Str(user, "permalink_url"),
		WebpageURL:  rawURL,
	}
	if rsrc != "" {
		info.Title += " (" + rsrc + ")"
	}
	next := apiBase + fmt.Sprintf(userEndpoints[rsrc], uid)
	q := url.Values{"limit": {"200"}, "linked_partitioning": {"1"}}
	for page := 0; next != "" && page < maxUserPage; page++ {
		var data any
		if err := api(ctx, env, next, q, &data); err != nil {
			if len(info.Entries) > 0 {
				break
			}
			return nil, apiErr(userKey, info.ID, err)
		}
		for _, item := range ex.Slice(data, "collection") {
			// элементы бывают треками, плейлистами или обёртками {track|playlist: ...}
			for _, k := range []string{"track", "playlist"} {
				if inner := ex.Map(item, k); inner != nil {
					item = inner
				}
			}
			u := ex.Str(item, "permalink_url")
			if u == "" {
				continue
			}
			ie := trackKey
			if ex.Str(item, "kind") == "playlist" {
				ie = setKey
			}
			e := media.NewURL(u, ie)
			e.ID = strconv.FormatInt(ex.Int(item, "id"), 10)
			e.Title = ex.Str(item, "title")
			info.Entries = append(info.Entries, e)
		}
		if env.MaxEntries > 0 && len(info.Entries) >= env.MaxEntries {
			break
		}
		next = ex.Str(data, "next_href")
		q = nil // next_href уже содержит параметры (кроме client_id)
	}
	info.PlaylistCount = len(info.Entries)
	return info, nil
}
