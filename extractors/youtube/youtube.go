// Package youtube — экстракторы YouTube: видео (innertube player API),
// плейлисты, вкладки каналов и поиск ytsearch.
package youtube

import (
	"context"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/media"
)

const (
	ieKey   = "youtube"
	baseURL = "https://www.youtube.com"
	// chunkSize — YouTube режет скорость при загрузке без Range-чанков.
	chunkSize = 10 << 20
)

var consentHeaders = map[string]string{
	"Cookie": "PREF=hl=en&tz=UTC; SOCS=CAI",
}

const idRe = `[0-9A-Za-z_-]{11}`

var videoMatcher = ex.MustMatcher(
	`^(?:https?://)?(?:(?:www|m|music)\.)?(?:youtube\.com|youtube-nocookie\.com)/(?:watch\?(?:.*&)?v=|embed/|shorts/|live/|v/|e/)(?P<id>`+idRe+`)`,
	`^(?:https?://)?youtu\.be/(?P<id>`+idRe+`)`,
	`^(?P<id>`+idRe+`)$`,
)

var reList = regexp.MustCompile(`[?&]list=([0-9A-Za-z_-]+)`)

// Video — экстрактор отдельных видео.
type Video struct{}

// New возвращает экстрактор видео YouTube.
func New() *Video { return &Video{} }

func (*Video) Name() string        { return ieKey }
func (*Video) Description() string { return "YouTube videos (innertube API)" }
func (*Video) Suitable(u string) bool {
	return videoMatcher.Match(u)
}

func (y *Video) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := videoMatcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	id := g["id"]

	// watch?v=X&list=Y — по умолчанию плейлист, как в yt-dlp
	if m := reList.FindStringSubmatch(rawURL); m != nil && !env.NoPlaylist && !strings.HasPrefix(m[1], "RD") {
		env.Debug("downloading playlist; use NoPlaylist to download only the video", "list", m[1], "video", id)
		return media.NewURL(baseURL+"/playlist?list="+m[1], playlistKey), nil
	}
	return extractVideo(ctx, env, id)
}

func extractVideo(ctx context.Context, env *ex.Env, id string) (*media.Info, error) {
	watchURL := baseURL + "/watch?v=" + id

	// 1. Веб-страница: visitorData и initial player response (метаданные).
	var initialPR, ytcfg, initialData any
	page, err := env.GetText(ctx, watchURL+"&bpctr=9999999999&has_verified=1", consentHeaders)
	if err != nil {
		env.Warn("youtube: unable to download webpage", "id", id, "err", err)
	} else {
		ytcfg, _ = ex.JSONAfter(page, "ytcfg.set(")
		initialPR, _ = ex.JSONAfter(page, "var ytInitialPlayerResponse = ", "ytInitialPlayerResponse = ")
		initialData, _ = ex.JSONAfter(page, "var ytInitialData = ", `window["ytInitialData"] = `)
	}
	visitor := ex.FirstStr(ytcfg, ex.P("VISITOR_DATA"), ex.P("INNERTUBE_CONTEXT", "client", "visitorData"))
	if visitor == "" {
		visitor = ex.Str(initialPR, "responseContext", "visitorData")
	}
	sts := ex.Int(ytcfg, "STS")
	env.Debug("youtube: webpage config", "id", id, "visitor_data", visitor != "", "sts", sts)

	// 2. Player API для выбранных клиентов.
	clients := env.ArgList(ieKey, "player_client")
	if len(clients) == 0 {
		clients = DefaultClients
	}
	var (
		responses []any
		lastErr   error
		formats   []*media.Format
		seen      = map[string]bool{}
	)
	for _, name := range clients {
		c, ok := Clients[name]
		if !ok {
			env.Warn("youtube: unknown player client, skipping", "client", name)
			continue
		}
		pr, err := playerRequest(ctx, env, c, id, visitor, sts)
		if err != nil {
			lastErr = err
			env.Warn("youtube: player request failed", "client", name, "err", err)
			continue
		}
		if err := playability(id, pr); err != nil {
			lastErr = err
			env.Debug("youtube: client not playable", "client", name, "err", err)
			continue
		}
		responses = append(responses, pr)
		for _, f := range parseFormats(ctx, env, c, pr) {
			key := f.FormatID
			if seen[key] {
				continue
			}
			seen[key] = true
			formats = append(formats, f)
		}
	}
	if len(responses) == 0 {
		if lastErr == nil {
			lastErr = ex.Errorf(ieKey, id, ex.ErrNoFormats, "no usable player clients")
		}
		// если web-страница сообщает причину — покажем её
		if initialPR != nil {
			if err := playability(id, initialPR); err != nil {
				return nil, err
			}
		}
		return nil, lastErr
	}

	meta := initialPR
	if ex.Map(meta, "videoDetails") == nil {
		meta = responses[0]
	}
	info := buildInfo(id, meta, responses[0])
	info.Formats = formats
	if len(formats) == 0 {
		return nil, ex.Errorf(ieKey, id, ex.ErrNoFormats, "no formats found (all formats may require JS player or PO Token)")
	}
	info.Subtitles, info.AutomaticCaptions = captions(responses[0], meta)
	info.Chapters = chapters(initialData, info.Description, info.Duration)
	return info, nil
}

func playerRequest(ctx context.Context, env *ex.Env, c *Client, id, visitor string, sts int64) (any, error) {
	pbc := map[string]any{"html5Preference": "HTML5_PREF_WANTS"}
	if sts > 0 {
		pbc["signatureTimestamp"] = sts
	}
	body := map[string]any{
		"context":         c.context(),
		"videoId":         id,
		"playbackContext": map[string]any{"contentPlaybackContext": pbc},
		"contentCheckOk":  true,
		"racyCheckOk":     true,
	}
	headers := map[string]string{
		"Cookie":                   consentHeaders["Cookie"],
		"X-YouTube-Client-Name":    strconv.Itoa(c.ID),
		"X-YouTube-Client-Version": c.Version,
		"Origin":                   baseURL,
		"User-Agent":               c.UserAgent,
	}
	if visitor != "" {
		headers["X-Goog-Visitor-Id"] = visitor
	}
	var pr any
	err := env.PostJSON(ctx, baseURL+"/youtubei/v1/player?prettyPrint=false", headers, body, &pr)
	return pr, err
}

func playability(id string, pr any) error {
	status := ex.Str(pr, "playabilityStatus", "status")
	if status == "OK" || status == "" && ex.Map(pr, "streamingData") != nil {
		return nil
	}
	reason := ex.FirstStr(pr,
		ex.P("playabilityStatus", "reason"),
		ex.P("playabilityStatus", "errorScreen", "playerErrorMessageRenderer", "reason", "simpleText"),
		ex.P("playabilityStatus", "messages", 0),
	)
	if reason == "" {
		reason = "video unavailable (" + status + ")"
	}
	kind := ex.ErrUnavailable
	switch {
	case status == "LOGIN_REQUIRED":
		kind = ex.ErrLoginRequired
	case strings.Contains(strings.ToLower(reason), "country"):
		kind = ex.ErrGeoRestricted
	}
	return ex.Errorf(ieKey, id, kind, "%s", reason)
}

var reMimeCodecs = regexp.MustCompile(`^([^;]+);\s*codecs="([^"]*)"`)

func parseFormats(ctx context.Context, env *ex.Env, c *Client, pr any) []*media.Format {
	sd := ex.Map(pr, "streamingData")
	var out []*media.Format
	skippedCipher := 0
	for _, list := range []string{"formats", "adaptiveFormats"} {
		for _, raw := range ex.Slice(sd, list) {
			fm, _ := raw.(map[string]any)
			u := ex.Str(fm, "url")
			if u == "" {
				if ex.Str(fm, "signatureCipher") != "" || ex.Str(fm, "cipher") != "" {
					skippedCipher++
				}
				continue
			}
			if f := parseFormat(fm, u, c); f != nil {
				out = append(out, f)
			}
		}
	}
	if skippedCipher > 0 {
		env.Warn("youtube: skipped formats that require signature deciphering (JS player is not supported)", "client", c.Name, "count", skippedCipher)
	}
	if hlsURL := ex.Str(sd, "hlsManifestUrl"); hlsURL != "" {
		out = append(out, hlsFormats(ctx, env, c, hlsURL)...)
	}
	return out
}

func parseFormat(fm map[string]any, u string, c *Client) *media.Format {
	itag := ex.Int(fm, "itag")
	f := &media.Format{
		FormatID:      strconv.FormatInt(itag, 10),
		URL:           u,
		Protocol:      media.ProtoHTTPS,
		Width:         int(ex.Int(fm, "width")),
		Height:        int(ex.Int(fm, "height")),
		FPS:           ex.Float(fm, "fps"),
		TBR:           ex.Float(fm, "averageBitrate") / 1000,
		Filesize:      ex.Int(fm, "contentLength"),
		ASR:           int(ex.Int(fm, "audioSampleRate")),
		AudioChannels: int(ex.Int(fm, "audioChannels")),
		HTTPHeaders:   map[string]string{"User-Agent": c.UserAgent},
		HTTPChunkSize: chunkSize,
	}
	if f.TBR == 0 {
		f.TBR = ex.Float(fm, "bitrate") / 1000
	}
	mime := ex.Str(fm, "mimeType")
	if m := reMimeCodecs.FindStringSubmatch(mime); m != nil {
		typ := m[1]
		codecs := strings.Split(m[2], ",")
		for i := range codecs {
			codecs[i] = strings.TrimSpace(codecs[i])
		}
		major, sub, _ := strings.Cut(typ, "/")
		switch sub {
		case "mp4":
			f.Ext = "mp4"
			if major == "audio" {
				f.Ext = "m4a"
			}
		case "webm":
			f.Ext = "webm"
		case "3gpp":
			f.Ext = "3gp"
		default:
			f.Ext = sub
		}
		switch {
		case major == "audio":
			f.VCodec, f.ACodec = media.CodecNone, codecs[0]
			f.ABR = f.TBR
		case len(codecs) >= 2:
			f.VCodec, f.ACodec = codecs[0], codecs[1]
		default:
			f.VCodec, f.ACodec = codecs[0], media.CodecNone
			f.VBR = f.TBR
		}
	}
	if f.Filesize == 0 && f.TBR > 0 {
		if dur := ex.Float(fm, "approxDurationMs") / 1000; dur > 0 {
			f.FilesizeApprox = int64(f.TBR * 1000 / 8 * dur)
		}
	}
	notes := []string{}
	if q := ex.Str(fm, "qualityLabel"); q != "" {
		notes = append(notes, q)
	} else if q := ex.Str(fm, "audioQuality"); q != "" {
		notes = append(notes, strings.ToLower(strings.TrimPrefix(q, "AUDIO_QUALITY_")))
	}
	if lang := ex.Str(fm, "audioTrack", "displayName"); lang != "" {
		notes = append(notes, lang)
		id := ex.Str(fm, "audioTrack", "id")
		f.Language, _, _ = strings.Cut(id, ".")
		f.FormatID += "-" + strings.ReplaceAll(id, ".", "_")
		if ex.Bool(fm, "audioTrack", "audioIsDefault") {
			f.LanguagePreference = 10
		} else {
			f.LanguagePreference = -1
		}
	}
	if ex.Bool(fm, "isDrc") {
		notes = append(notes, "DRC")
		f.FormatID += "-drc"
		f.Preference = -1
	}
	if ex.Str(fm, "colorInfo", "transferCharacteristics") == "COLOR_TRANSFER_CHARACTERISTICS_SMPTEST2084" {
		f.DynamicRange = "HDR"
	}
	f.FormatNote = strings.Join(notes, ", ")
	return f
}

var reItag = regexp.MustCompile(`/itag/(\d+)/`)

func hlsFormats(ctx context.Context, env *ex.Env, c *Client, manifest string) []*media.Format {
	body, err := env.GetText(ctx, manifest, map[string]string{"User-Agent": c.UserAgent})
	if err != nil {
		env.Warn("youtube: unable to download HLS manifest", "err", err)
		return nil
	}
	fs := parseHLS(body, manifest)
	for _, f := range fs {
		if m := reItag.FindStringSubmatch(f.URL); m != nil {
			f.FormatID = m[1]
		}
		f.HTTPHeaders = map[string]string{"User-Agent": c.UserAgent}
	}
	return fs
}

func buildInfo(id string, meta, pr any) *media.Info {
	vd := ex.Map(meta, "videoDetails")
	if vd == nil {
		vd = ex.Map(pr, "videoDetails")
	}
	mf := ex.Map(meta, "microformat", "playerMicroformatRenderer")
	info := &media.Info{
		Kind:        media.KindVideo,
		ID:          id,
		Title:       ex.Str(vd, "title"),
		Description: ex.Str(vd, "shortDescription"),
		WebpageURL:  baseURL + "/watch?v=" + id,
		Uploader:    ex.Str(vd, "author"),
		Channel:     ex.Str(vd, "author"),
		ChannelID:   ex.Str(vd, "channelId"),
		Duration:    float64(ex.Int(vd, "lengthSeconds")),
		ViewCount:   ex.Int(vd, "viewCount"),
		IsLive:      ex.Bool(vd, "isLiveContent") && ex.Bool(vd, "isLive"),
	}
	if info.ChannelID != "" {
		info.ChannelURL = baseURL + "/channel/" + info.ChannelID
	}
	for _, k := range ex.Slice(vd, "keywords") {
		if s, ok := k.(string); ok {
			info.Tags = append(info.Tags, s)
		}
	}
	if mf != nil {
		info.UploadDate = ex.UnifiedDate(ex.FirstStr(mf, ex.P("uploadDate"), ex.P("publishDate")))
		if cat := ex.Str(mf, "category"); cat != "" {
			info.Categories = []string{cat}
		}
		if u := ex.Str(mf, "ownerProfileUrl"); u != "" {
			info.UploaderURL = u
			if i := strings.Index(u, "/@"); i >= 0 {
				info.UploaderID = u[i+1:]
			}
		}
		if info.Description == "" {
			info.Description = ex.Str(mf, "description", "simpleText")
		}
		if safe, ok := mf["isFamilySafe"].(bool); ok && !safe {
			info.AgeLimit = 18
		}
	}
	for _, t := range ex.Slice(vd, "thumbnail", "thumbnails") {
		info.Thumbnails = append(info.Thumbnails, media.Thumbnail{
			URL:    ex.Str(t, "url"),
			Width:  int(ex.Int(t, "width")),
			Height: int(ex.Int(t, "height")),
		})
	}
	for _, n := range []string{"maxresdefault", "sddefault", "hqdefault"} {
		info.Thumbnails = append(info.Thumbnails, media.Thumbnail{ID: n, URL: "https://i.ytimg.com/vi/" + id + "/" + n + ".jpg"})
	}
	info.Thumbnail = "https://i.ytimg.com/vi/" + id + "/maxresdefault.jpg"
	return info
}

func captions(prs ...any) (subs, auto map[string][]media.Subtitle) {
	subs, auto = map[string][]media.Subtitle{}, map[string][]media.Subtitle{}
	for _, pr := range prs {
		tracks := ex.Slice(pr, "captions", "playerCaptionsTracklistRenderer", "captionTracks")
		if len(tracks) == 0 {
			continue
		}
		for _, t := range tracks {
			base := ex.Str(t, "baseUrl")
			lang := ex.Str(t, "languageCode")
			if base == "" || lang == "" {
				continue
			}
			name := ex.FirstStr(t, ex.P("name", "simpleText"), ex.P("name", "runs", 0, "text"))
			target := subs
			if ex.Str(t, "kind") == "asr" {
				target = auto
			}
			for _, fmtName := range []string{"vtt", "srv3", "json3"} {
				u, err := url.Parse(ex.URLJoin(baseURL, base))
				if err != nil {
					continue
				}
				q := u.Query()
				q.Set("fmt", fmtName)
				u.RawQuery = q.Encode()
				target[lang] = append(target[lang], media.Subtitle{URL: u.String(), Ext: fmtName, Name: name})
			}
		}
		break
	}
	if len(subs) == 0 {
		subs = nil
	}
	if len(auto) == 0 {
		auto = nil
	}
	return subs, auto
}

// chapters: маркеры плеера → панель «Главы» → таймкоды в описании (как в yt-dlp).
func chapters(data any, description string, duration float64) []media.Chapter {
	text := func(v any, k string) string {
		return ex.FirstStr(v, ex.P(k, "simpleText"), ex.P(k, "runs", 0, "text"))
	}
	var raw []ex.RawChapter
	for _, c := range ex.Slice(data, "playerOverlays", "playerOverlayRenderer", "decoratedPlayerBarRenderer",
		"decoratedPlayerBarRenderer", "playerBar", "chapteredPlayerBarRenderer", "chapters") {
		r := ex.Map(c, "chapterRenderer")
		start := -1.0
		if v := ex.Get(r, "timeRangeStartMillis"); v != nil {
			start = ex.ToFloat(v) / 1000
		}
		raw = append(raw, ex.RawChapter{Start: start, Title: text(r, "title")})
	}
	if ch := ex.BuildChapters(raw, duration, false); len(ch) > 0 {
		return ch
	}
	for _, p := range ex.Slice(data, "engagementPanels") {
		raw = raw[:0]
		for _, it := range ex.Slice(p, "engagementPanelSectionListRenderer", "content", "macroMarkersListRenderer", "contents") {
			r := ex.Map(it, "macroMarkersListItemRenderer")
			if r == nil {
				continue
			}
			start := -1.0
			if t := text(r, "timeDescription"); t != "" {
				start = ex.ParseDuration(t)
			}
			raw = append(raw, ex.RawChapter{Start: start, Title: text(r, "title")})
		}
		if ch := ex.BuildChapters(raw, duration, false); len(ch) > 0 {
			return ch
		}
	}
	return ex.ChaptersFromDescription(description, duration)
}
