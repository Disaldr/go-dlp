// Package eporner — экстрактор Eporner (18+). API плеера принимает хеш
// видео, перекодированный по 8 hex-символов в base36 (как в vjs.js и yt-dlp).
package eporner

import (
	"context"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/hls"
	"github.com/Disaldr/go-dlp/media"
)

const ieKey = "eporner"

var matcher = ex.MustMatcher(`^https?://(?:www\.)?eporner\.com/(?:(?:hd-porn|embed)/|video-)(?P<id>\w+)(?:/(?P<display>[\w-]+))?`)

// Eporner — экстрактор Eporner.
type Eporner struct{}

// New возвращает экстрактор Eporner.
func New() *Eporner { return &Eporner{} }

func (*Eporner) Name() string           { return ieKey }
func (*Eporner) Description() string    { return "Eporner (18+)" }
func (*Eporner) Suitable(u string) bool { return matcher.Match(u) }

// CalcHash перекодирует 32-символьный hex-хеш: 4 блока по 8 символов → base36.
func CalcHash(h string) string {
	var b strings.Builder
	for i := 0; i+8 <= len(h) && i < 32; i += 8 {
		n, err := strconv.ParseUint(h[i:i+8], 16, 64)
		if err != nil {
			return ""
		}
		b.WriteString(strconv.FormatUint(n, 36))
	}
	return b.String()
}

var (
	reHash   = regexp.MustCompile(`hash\s*[:=]\s*["']([\da-f]{32})`)
	reViews  = regexp.MustCompile(`id=["']cinemaviews1["'][^>]*>\s*([0-9,]+)`)
	reHeight = regexp.MustCompile(`(\d+)[pP]`)
	reFPS    = regexp.MustCompile(`(\d+)fps`)
)

func (e *Eporner) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	if _, ok := matcher.Groups(rawURL); !ok {
		return nil, ex.ErrUnsupportedURL
	}
	resp, err := env.Fetch(ctx, ex.Request{URL: rawURL})
	if err != nil {
		return nil, ex.Wrap(ieKey, "", err, "unable to download page")
	}
	page := resp.Text()
	g, _ := matcher.Groups(resp.URL) // id после редиректа
	if g == nil {
		g, _ = matcher.Groups(rawURL)
	}
	id := g["id"]
	m := reHash.FindStringSubmatch(page)
	if m == nil {
		return nil, ex.Errorf(ieKey, id, ex.ErrUnavailable, "video hash not found on page (video removed or unavailable)")
	}
	q := url.Values{
		"hash": {CalcHash(m[1])}, "device": {"generic"}, "domain": {"www.eporner.com"}, "fallback": {"false"},
	}
	var video any
	if err := env.GetJSON(ctx, "https://www.eporner.com/xhr/video/"+id+"?"+q.Encode(), nil, &video); err != nil {
		return nil, ex.Wrap(ieKey, id, err, "unable to download video JSON")
	}
	if avail, ok := ex.Get(video, "available").(bool); ok && !avail {
		return nil, ex.Errorf(ieKey, id, ex.ErrUnavailable, "eporner said: %s", ex.Str(video, "message"))
	}
	info := &media.Info{
		Kind:       media.KindVideo,
		ID:         id,
		Title:      ex.MetaContent(page, "og:title"),
		Thumbnail:  ex.MetaContent(page, "og:image"),
		Duration:   ex.ParseDuration(ex.MetaContent(page, "duration")),
		AgeLimit:   18,
		WebpageURL: resp.URL,
	}
	if info.Title == "" {
		info.Title = strings.TrimSuffix(ex.HTMLTitle(page), " - EPORNER")
	}
	for _, ld := range ex.JSONLD(page) {
		if ex.Str(ld, "@type") == "VideoObject" {
			info.Description = ex.Str(ld, "description")
			info.UploadDate = ex.UnifiedDate(ex.Str(ld, "uploadDate"))
			if info.Duration == 0 {
				info.Duration = ex.ParseDuration(ex.Str(ld, "duration"))
			}
		}
	}
	if mv := reViews.FindStringSubmatch(page); mv != nil {
		info.ViewCount = ex.ToInt(mv[1])
	}
	hasAV1 := strings.Contains(page, "download-av1")
	for kind, list := range ex.Map(video, "sources") {
		for fid, f := range ex.Map(list) {
			src := ex.Str(f, "src")
			if !strings.HasPrefix(src, "http") {
				continue
			}
			if kind == "hls" {
				if body, err := env.GetText(ctx, src, nil); err == nil {
					info.Formats = append(info.Formats, hls.ParseMaster(body, src, "hls")...)
				}
				continue
			}
			h, fps := 0, 0.0
			if mh := reHeight.FindStringSubmatch(fid); mh != nil {
				h, _ = strconv.Atoi(mh[1])
			}
			if mf := reFPS.FindStringSubmatch(fid); mf != nil {
				fps, _ = strconv.ParseFloat(mf[1], 64)
			}
			id := strings.ReplaceAll(strings.ToLower(fid), " ", "_")
			info.Formats = append(info.Formats, &media.Format{FormatID: id, URL: src, Ext: "mp4", Height: h, FPS: fps})
			if hasAV1 {
				info.Formats = append(info.Formats, &media.Format{
					FormatID: "av1-" + id, URL: strings.Replace(src, ".mp4", "-av1.mp4", 1), Ext: "mp4", Height: h, FPS: fps, VCodec: "av01",
				})
			}
		}
	}
	if len(info.Formats) == 0 {
		return nil, ex.Errorf(ieKey, id, ex.ErrNoFormats, "no formats found")
	}
	return info, nil
}
