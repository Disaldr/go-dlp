// Package xvideos — экстракторы XVideos и XNXX (общий движок html5player).
package xvideos

import (
	"context"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/hls"
	"github.com/Disaldr/go-dlp/media"
)

var (
	xvMatcher = ex.MustMatcher(
		`^https?://(?:[^/]+\.)?xvideos2?\.com/video\.?(?P<id>[0-9a-z]+)`,
		`^https?://(?:www\.)?xvideos\.es/video\.?(?P<id>[0-9a-z]+)`,
		`^https?://(?:www|flashservice)\.xvideos\.com/embedframe/(?P<id>[0-9a-z]+)`,
	)
	xnxxMatcher = ex.MustMatcher(`^https?://(?:video|www)\.xnxx3?\.com/video-?(?P<id>[0-9a-z]+)/`)
)

// Site — XVideos или XNXX.
type Site struct {
	key, desc string
	m         ex.Matcher
}

// NewXVideos возвращает экстрактор XVideos.
func NewXVideos() *Site { return &Site{key: "xvideos", desc: "XVideos (18+)", m: xvMatcher} }

// NewXNXX возвращает экстрактор XNXX.
func NewXNXX() *Site { return &Site{key: "xnxx", desc: "XNXX (18+)", m: xnxxMatcher} }

func (s *Site) Name() string           { return s.key }
func (s *Site) Description() string    { return s.desc }
func (s *Site) Suitable(u string) bool { return s.m.Match(u) }

var (
	reInlineError = regexp.MustCompile(`(?s)<h1 class="inlineError">(.+?)</h1>`)
	reSetter      = regexp.MustCompile(`set(Video(?:UrlLow|UrlHigh|HLS|Title)|ThumbUrl169|ThumbUrl)\s*\(\s*(["'])(.+?)["']\s*\)`)
	reFLV         = regexp.MustCompile(`flv_url=(.+?)&`)
	reViews       = regexp.MustCompile(`id=["']nb-views-number[^>]+>([\d,.]+)`)
	reDurSpan     = regexp.MustCompile(`(?s)<span[^>]+class=["']duration["'][^>]*>.*?(\d[^<]+)`)
)

func (s *Site) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := s.m.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	id := g["id"]
	page, err := env.GetText(ctx, rawURL, nil)
	if err != nil {
		return nil, ex.Wrap(s.key, id, err, "unable to download page")
	}
	if m := reInlineError.FindStringSubmatch(page); m != nil {
		return nil, ex.Errorf(s.key, id, ex.ErrUnavailable, "%s said: %s", s.key, ex.CleanHTML(m[1]))
	}
	vals := map[string]string{}
	for _, m := range reSetter.FindAllStringSubmatch(page, -1) {
		if _, dup := vals[m[1]]; !dup {
			vals[m[1]] = html.UnescapeString(m[3])
		}
	}
	info := &media.Info{
		Kind:       media.KindVideo,
		ID:         id,
		Title:      ex.MetaContent(page, "og:title"),
		AgeLimit:   18,
		WebpageURL: rawURL,
	}
	if t := vals["VideoTitle"]; t != "" && s.key == "xvideos" {
		info.Title = t
	}
	if t := strings.TrimSuffix(ex.HTMLTitle(page), " - XVIDEOS.COM"); info.Title == "" {
		info.Title = t
	}
	for _, t := range []string{ex.MetaContent(page, "og:image"), vals["ThumbUrl169"], vals["ThumbUrl"]} {
		if t != "" {
			info.Thumbnail = t
			break
		}
	}
	if d, err := strconv.ParseFloat(ex.MetaContent(page, "og:duration"), 64); err == nil {
		info.Duration = d
	} else if m := reDurSpan.FindStringSubmatch(page); m != nil {
		info.Duration = parseHumanDuration(m[1])
	}
	if m := reViews.FindStringSubmatch(page); m != nil {
		info.ViewCount = ex.ToInt(m[1])
	}

	add := func(id, u string, quality float64) {
		if strings.HasPrefix(u, "//") {
			u = "https:" + u
		}
		info.Formats = append(info.Formats, &media.Format{FormatID: id, URL: u, Ext: "mp4", Quality: quality})
	}
	if u := vals["VideoUrlLow"]; u != "" {
		add("mp4-low", u, -2)
	}
	if u := vals["VideoUrlHigh"]; u != "" {
		add("mp4-high", u, 0)
	}
	if m := reFLV.FindStringSubmatch(page); m != nil && len(info.Formats) == 0 {
		if u, err := url.QueryUnescape(m[1]); err == nil {
			info.Formats = append(info.Formats, &media.Format{FormatID: "flv", URL: u, Ext: "flv"})
		}
	}
	if u := vals["VideoHLS"]; u != "" {
		if strings.HasPrefix(u, "//") {
			u = "https:" + u
		}
		if body, err := env.GetText(ctx, u, nil); err == nil {
			fs := hls.ParseMaster(body, u, "hls")
			for _, f := range fs {
				f.Quality = 1
			}
			info.Formats = append(info.Formats, fs...)
		} else {
			env.Warn(s.key+": HLS manifest unavailable", "err", err)
		}
	}
	if len(info.Formats) == 0 {
		return nil, ex.Errorf(s.key, id, ex.ErrNoFormats, "no formats found")
	}
	return info, nil
}

// parseHumanDuration понимает "10 min", "1 h 5 min", "45 sec".
func parseHumanDuration(s string) float64 {
	s = strings.ToLower(strings.TrimSpace(s))
	if d := ex.ParseDuration(s); d > 0 {
		return d
	}
	var total float64
	fields := strings.Fields(s)
	for i := 0; i+1 < len(fields); i += 2 {
		n, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			continue
		}
		switch {
		case strings.HasPrefix(fields[i+1], "h"):
			total += n * 3600
		case strings.HasPrefix(fields[i+1], "m"):
			total += n * 60
		case strings.HasPrefix(fields[i+1], "s"):
			total += n
		}
	}
	return total
}
