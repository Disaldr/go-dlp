package extractor

import (
	"encoding/json"
	"html"
	"mime"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// SearchRegex возвращает первую группу (или всё совпадение) первого совпавшего шаблона.
func SearchRegex(s string, patterns ...string) string {
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			continue
		}
		m := re.FindStringSubmatch(s)
		if m == nil {
			continue
		}
		if len(m) > 1 {
			for _, g := range m[1:] {
				if g != "" {
					return g
				}
			}
			continue
		}
		return m[0]
	}
	return ""
}

var (
	reMetaTag  = regexp.MustCompile(`(?is)<meta\s[^>]*>`)
	reAttr     = regexp.MustCompile(`(?is)([a-zA-Z_:.-]+)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)
	reTitleTag = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
)

// ParseAttrs разбирает атрибуты HTML-тега.
func ParseAttrs(tag string) map[string]string {
	attrs := map[string]string{}
	// пропускаем имя тега
	if i := strings.IndexAny(tag, " \t\r\n"); i >= 0 {
		tag = tag[i:]
	}
	for _, m := range reAttr.FindAllStringSubmatch(tag, -1) {
		v := m[2] + m[3] + m[4]
		attrs[strings.ToLower(m[1])] = html.UnescapeString(v)
	}
	return attrs
}

// MetaContent возвращает content первого <meta> с property/name/itemprop из names.
func MetaContent(page string, names ...string) string {
	tags := reMetaTag.FindAllString(page, -1)
	for _, name := range names {
		for _, t := range tags {
			a := ParseAttrs(t)
			for _, k := range []string{"property", "name", "itemprop"} {
				if strings.EqualFold(a[k], name) && a["content"] != "" {
					return a["content"]
				}
			}
		}
	}
	return ""
}

// HTMLTitle возвращает содержимое <title>.
func HTMLTitle(page string) string {
	if m := reTitleTag.FindStringSubmatch(page); m != nil {
		return strings.TrimSpace(html.UnescapeString(m[1]))
	}
	return ""
}

// FindTags возвращает все открывающие теги с именем name (например "iframe").
func FindTags(page, name string) []map[string]string {
	re := regexp.MustCompile(`(?is)<` + regexp.QuoteMeta(name) + `\b[^>]*>`)
	var out []map[string]string
	for _, t := range re.FindAllString(page, -1) {
		out = append(out, ParseAttrs(t))
	}
	return out
}

// JSONAfter декодирует JSON-значение, начинающееся сразу после вхождения
// маркера (пропуская пробелы и '=' / '('). Перебираются все вхождения всех
// маркеров по порядку, пока одно не декодируется. Удобно для
// `var ytInitialData = {...};` и `ytcfg.set({...})`.
func JSONAfter(s string, markers ...string) (any, bool) {
	for _, m := range markers {
		for off := 0; off < len(s); {
			idx := strings.Index(s[off:], m)
			if idx < 0 {
				break
			}
			start := off + idx + len(m)
			off = start
			rest := strings.TrimLeft(s[start:], " \t\r\n=(")
			if rest == "" || (rest[0] != '{' && rest[0] != '[') {
				continue
			}
			var v any
			if err := json.NewDecoder(strings.NewReader(rest)).Decode(&v); err == nil {
				return v, true
			}
		}
	}
	return nil, false
}

// JSONLD возвращает все объекты JSON-LD со страницы (с раскрытием @graph и массивов).
func JSONLD(page string) []map[string]any {
	re := regexp.MustCompile(`(?is)<script[^>]+type\s*=\s*["']application/ld\+json["'][^>]*>(.*?)</script>`)
	var out []map[string]any
	var add func(any)
	add = func(v any) {
		switch t := v.(type) {
		case []any:
			for _, x := range t {
				add(x)
			}
		case map[string]any:
			out = append(out, t)
			if g, ok := t["@graph"]; ok {
				add(g)
			}
		}
	}
	for _, m := range re.FindAllStringSubmatch(page, -1) {
		var v any
		if json.Unmarshal([]byte(strings.TrimSpace(m[1])), &v) == nil {
			add(v)
		}
	}
	return out
}

// URLJoin разрешает ref относительно base.
func URLJoin(base, ref string) string {
	if ref == "" {
		return ""
	}
	if strings.HasPrefix(ref, "//") {
		if b, err := url.Parse(base); err == nil && b.Scheme != "" {
			return b.Scheme + ":" + ref
		}
		return "https:" + ref
	}
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}

// URLExt возвращает расширение пути URL без точки (в нижнем регистре).
func URLExt(u string) string {
	pu, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(path.Ext(pu.Path)), ".")
}

// KnownMediaExts — расширения, однозначно указывающие на медиафайл.
var KnownMediaExts = map[string]bool{
	"mp4": true, "m4v": true, "m4a": true, "webm": true, "mkv": true, "mov": true,
	"flv": true, "3gp": true, "mp3": true, "aac": true, "ogg": true, "oga": true, "ogv": true,
	"opus": true, "wav": true, "flac": true, "ts": true, "m3u8": true, "mpd": true,
}

// MimeToExt превращает MIME-тип в расширение.
func MimeToExt(mt string) string {
	mt, _, _ = mime.ParseMediaType(mt)
	switch strings.ToLower(mt) {
	case "video/mp4":
		return "mp4"
	case "audio/mp4", "audio/x-m4a":
		return "m4a"
	case "video/webm", "audio/webm":
		return "webm"
	case "audio/mpeg", "audio/mp3":
		return "mp3"
	case "audio/ogg", "application/ogg":
		return "ogg"
	case "video/ogg":
		return "ogv"
	case "video/x-matroska":
		return "mkv"
	case "video/quicktime":
		return "mov"
	case "video/x-flv":
		return "flv"
	case "audio/wav", "audio/x-wav", "audio/wave":
		return "wav"
	case "audio/flac", "audio/x-flac":
		return "flac"
	case "audio/aac":
		return "aac"
	case "video/mp2t":
		return "ts"
	case "application/vnd.apple.mpegurl", "application/x-mpegurl", "audio/mpegurl", "audio/x-mpegurl":
		return "m3u8"
	case "application/dash+xml":
		return "mpd"
	}
	return ""
}

var reISODur = regexp.MustCompile(`^P(?:(\d+(?:\.\d+)?)D)?(?:T(?:(\d+(?:\.\d+)?)H)?(?:(\d+(?:\.\d+)?)M)?(?:(\d+(?:\.\d+)?)S)?)?$`)

// ParseDuration понимает "1:02:03", "62", "PT1H2M3S", "1h2m3s".
func ParseDuration(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if m := reISODur.FindStringSubmatch(strings.ToUpper(s)); m != nil {
		mult := []float64{86400, 3600, 60, 1}
		var total float64
		for i, g := range m[1:] {
			if g != "" {
				f, _ := strconv.ParseFloat(g, 64)
				total += f * mult[i]
			}
		}
		return total
	}
	if strings.Contains(s, ":") {
		var total float64
		for p := range strings.SplitSeq(s, ":") {
			f, err := strconv.ParseFloat(p, 64)
			if err != nil {
				return 0
			}
			total = total*60 + f
		}
		return total
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d.Seconds()
	}
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

// UnifiedDate приводит дату к YYYYMMDD; пустая строка — если не распознана.
func UnifiedDate(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	layouts := []string{
		time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04:05-0700", "2006-01-02 15:04:05",
		"2006-01-02", "2006/01/02", "20060102", "Jan 2, 2006", "January 2, 2006", "2 Jan 2006",
		time.RFC1123, time.RFC1123Z,
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC().Format("20060102")
		}
	}
	return ""
}

// UnixToDate превращает unix-время в YYYYMMDD.
func UnixToDate(ts int64) string {
	if ts <= 0 {
		return ""
	}
	return time.Unix(ts, 0).UTC().Format("20060102")
}

// CleanHTML грубо удаляет теги и декодирует сущности.
func CleanHTML(s string) string {
	s = regexp.MustCompile(`(?i)<br\s*/?>`).ReplaceAllString(s, "\n")
	s = regexp.MustCompile(`(?s)<[^>]+>`).ReplaceAllString(s, "")
	return strings.TrimSpace(html.UnescapeString(s))
}
