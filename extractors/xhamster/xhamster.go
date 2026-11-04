// Package xhamster — экстрактор xHamster (18+). Ссылки на потоки в плеере
// обфусцированы (XOR с гаммой одного из 7 ГПСЧ) — расшифровка как в yt-dlp.
// Страница загружается с имитацией браузера.
package xhamster

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/hls"
	"github.com/Disaldr/go-dlp/media"
)

const ieKey = "xhamster"

const domains = `(?:xhamster\.(?:com|one|desi)|xhms\.pro|xhamster\d+\.(?:com|desi)|xhday\.com|xhvid\.com)`

var matcher = ex.MustMatcher(
	`^https?://(?:[^/?#]+\.)?`+domains+`/movies/(?P<id>[\dA-Za-z]+)/(?P<display>[^/]*)\.html`,
	`^https?://(?:[^/?#]+\.)?`+domains+`/videos/(?P<display>[^/]*)-(?P<id>[\dA-Za-z]+)`,
)

// XHamster — экстрактор xHamster.
type XHamster struct{}

// New возвращает экстрактор xHamster.
func New() *XHamster { return &XHamster{} }

func (*XHamster) Name() string           { return ieKey }
func (*XHamster) Description() string    { return "xHamster (18+)" }
func (*XHamster) Suitable(u string) bool { return matcher.Match(u) }

// --- расшифровка ---

// algorithms — генераторы гаммы; состояние — uint32 (арифметика по модулю 2^32).
var algorithms = map[byte]func(s *uint32) uint32{
	1: func(s *uint32) uint32 { // LCG
		*s = *s*1664525 + 1013904223
		return *s
	},
	2: func(s *uint32) uint32 { // xorshift32
		x := *s
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		*s = x
		return x
	},
	3: func(s *uint32) uint32 { // Weyl + fmix32
		*s += 0x9e3779b9
		x := *s
		x ^= x >> 16
		x *= 0x85ebca77
		x ^= x >> 13
		x *= 0xc2b2ae3d
		return x ^ x>>16
	},
	4: func(s *uint32) uint32 { // ROL-скремблер
		*s += 0x6d2b79f5
		x := *s
		x = x<<7 | x>>25
		x += 0x9e3779b9
		x ^= x >> 11
		return x * 0x27d4eb2d
	},
	5: func(s *uint32) uint32 { // xorshift + сложение
		x := *s
		x ^= x << 7
		x ^= x >> 9
		x ^= x << 8
		x += 0xa5a5a5a5
		*s = x
		return x
	},
	6: func(s *uint32) uint32 { // LCG + переменный сдвиг
		*s = *s*0x2c9277b5 + 0xac564b05
		x := *s
		s2 := x ^ x>>18
		return s2 >> (x >> 27 & 31)
	},
	7: func(s *uint32) uint32 { // Weyl + multiply-xor-shift
		*s += 0x9e3779b9
		e := *s ^ *s<<5
		e *= 0x7feb352d
		e ^= e >> 15
		return e * 0x846ca68b
	},
}

// Decipher расшифровывает hex-строку: [алгоритм:1][seed:4 LE][шифротекст].
func Decipher(hexStr string) (string, error) {
	data, err := hex.DecodeString(hexStr)
	if err != nil || len(data) < 6 {
		return "", fmt.Errorf("invalid ciphertext")
	}
	gen, ok := algorithms[data[0]]
	if !ok {
		return "", fmt.Errorf("unknown algorithm ID %d", data[0])
	}
	state := binary.LittleEndian.Uint32(data[1:5])
	out := make([]byte, len(data)-5)
	for i, b := range data[5:] {
		out[i] = b ^ byte(gen(&state))
	}
	// latin-1: каждый байт — отдельный символ
	runes := make([]rune, len(out))
	for i, b := range out {
		runes[i] = rune(b)
	}
	return string(runes), nil
}

var (
	reHex     = regexp.MustCompile(`^[0-9a-fA-F]{12,}$`)
	reShortHx = regexp.MustCompile(`^[0-9a-fA-F]+$`)
	reHexPath = regexp.MustCompile(`^/([0-9a-fA-F]{12,})([/,].+)$`)
)

// decipherURL: значение — либо hex целиком, либо URL с hex-сегментом в пути.
func decipherURL(v string) (string, error) {
	if reHex.MatchString(v) {
		return Decipher(v)
	}
	if !strings.HasPrefix(v, "http") {
		if reShortHx.MatchString(v) {
			return "", nil // «пустышка», так и задумано
		}
		return "", fmt.Errorf("invalid ciphertext")
	}
	pu, err := url.Parse(v)
	if err != nil {
		return "", err
	}
	m := reHexPath.FindStringSubmatch(pu.Path)
	if m == nil {
		// нешифрованная ссылка
		return v, nil
	}
	plain, err := Decipher(m[1])
	if err != nil {
		return "", err
	}
	pu.Path = "/" + plain + m[2]
	pu.RawPath = ""
	return pu.String(), nil
}

// --- экстракция ---

var (
	reClosed = regexp.MustCompile(`(?s)<div[^>]+id=["']videoClosed["'][^>]*>(.+?)</div>`)
	reInit   = regexp.MustCompile(`window\.initials\s*=\s*\{`)
	reHeight = regexp.MustCompile(`^(\d+)[pP]`)
)

func height(q string) int {
	if m := reHeight.FindStringSubmatch(q); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

func (x *XHamster) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := matcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	id := g["id"]
	desktop := regexp.MustCompile(`^(https?://(?:.+?\.)?)m\.`).ReplaceAllString(rawURL, "$1")
	imp := env.Impersonate
	if imp == "" {
		imp = "chrome"
	}
	resp, err := env.Fetch(ctx, ex.Request{URL: desktop, Impersonate: imp})
	if err != nil {
		return nil, ex.Wrap(ieKey, id, err, "unable to download page")
	}
	page := resp.Text()
	if m := reClosed.FindStringSubmatch(page); m != nil {
		return nil, ex.Errorf(ieKey, id, ex.ErrUnavailable, "%s", ex.CleanHTML(m[1]))
	}
	loc := reInit.FindStringIndex(page)
	if loc == nil {
		return nil, ex.Errorf(ieKey, id, ex.ErrUnavailable, "no player data on page")
	}
	initials := decodeAt(page, loc[1]-1)
	video := ex.Map(initials, "videoModel")
	if video == nil {
		return nil, ex.Errorf(ieKey, id, ex.ErrUnavailable, "no video model on page")
	}
	referer := map[string]string{"Referer": resp.URL}
	info := &media.Info{
		Kind:        media.KindVideo,
		ID:          id,
		Title:       ex.Str(video, "title"),
		Description: ex.Str(video, "description"),
		Timestamp:   ex.Int(video, "created"),
		Uploader:    ex.Str(video, "author", "name"),
		UploaderURL: ex.Str(video, "author", "pageURL"),
		Thumbnail:   ex.Str(video, "thumbURL"),
		Duration:    ex.Float(video, "duration"),
		ViewCount:   ex.Int(video, "views"),
		LikeCount:   ex.Int(video, "rating", "likes"),
		AgeLimit:    18,
		WebpageURL:  resp.URL,
	}
	info.UploadDate = ex.UnixToDate(info.Timestamp)
	if u := info.UploaderURL; u != "" {
		info.UploaderID = u[strings.LastIndexByte(u, '/')+1:]
	}
	for _, c := range ex.Slice(video, "categories") {
		if n := ex.Str(c, "name"); n != "" {
			info.Categories = append(info.Categories, n)
		}
	}

	seen := map[string]bool{}
	sizes := map[string]int64{}
	for q, d := range ex.Map(video, "sources", "download") {
		sizes[q] = int64(ex.Float(d, "size"))
	}
	addHLS := func(u string) {
		if body, err := env.GetText(ctx, u, referer); err == nil {
			for _, f := range hls.ParseMaster(body, u, "hls") {
				f.HTTPHeaders = referer
				info.Formats = append(info.Formats, f)
			}
		} else {
			env.Warn("xhamster: HLS manifest", "err", err)
		}
	}
	// нешифрованные источники videoModel.sources (кроме download — ссылка готовится долго)
	for kind, list := range ex.Map(video, "sources") {
		if kind == "download" {
			continue
		}
		for q, raw := range ex.Map(list) {
			u, _ := raw.(string)
			if !strings.HasPrefix(u, "http") || seen[u] {
				continue
			}
			seen[u] = true
			info.Formats = append(info.Formats, &media.Format{
				FormatID: kind + "-" + q, URL: u, Ext: extOr(u), Height: height(q), Filesize: sizes[q], HTTPHeaders: referer,
			})
		}
	}
	xs := ex.Map(initials, "xplayerSettings", "sources")
	for _, k := range []string{"url", "fallback"} {
		v := ex.Str(xs, "hls", k)
		if v == "" {
			continue
		}
		u, err := decipherURL(v)
		if err != nil {
			env.Warn("xhamster: skipping format", "format", "hls-"+k, "err", err)
			continue
		}
		if u != "" && !seen[u] {
			seen[u] = true
			addHLS(u)
		}
	}
	for ident := range ex.Map(xs, "standard") {
		for _, sf := range ex.Slice(xs, "standard", ident) {
			q := ex.FirstStr(sf, ex.P("quality"), ex.P("label"))
			for _, k := range []string{"url", "fallback"} {
				v := ex.Str(sf, k)
				if v == "" {
					continue
				}
				u, err := decipherURL(v)
				if err != nil {
					env.Warn("xhamster: skipping format", "format", ident+"-"+q, "err", err)
					continue
				}
				if u == "" || seen[u] {
					continue
				}
				seen[u] = true
				if ex.URLExt(u) == "m3u8" {
					addHLS(u)
					continue
				}
				fid := ident
				if q != "" {
					fid += "-" + q
				}
				info.Formats = append(info.Formats, &media.Format{
					FormatID: fid, URL: u, Ext: extOr(u), Height: height(q), Filesize: sizes[q], HTTPHeaders: referer,
					// как в yt-dlp: прямые HTTP-ссылки сайт иногда отвергает («Wrong key») — HLS приоритетнее
					Preference: -1,
				})
			}
		}
	}
	for _, f := range info.Formats {
		if f.VCodec == "" {
			for _, vc := range []string{"av1", "h264"} {
				if strings.Contains(f.URL, "."+vc+".") || strings.Contains(f.ManifestURL, "."+vc+".") {
					f.VCodec = vc
				}
			}
		}
	}
	if len(info.Formats) == 0 {
		return nil, ex.Errorf(ieKey, id, ex.ErrNoFormats, "no formats found")
	}
	return info, nil
}

func extOr(u string) string {
	if e := ex.URLExt(u); e != "" {
		return e
	}
	return "mp4"
}
