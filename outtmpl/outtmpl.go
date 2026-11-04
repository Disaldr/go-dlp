// Package outtmpl реализует шаблоны имён файлов в стиле yt-dlp:
// "%(title)s [%(id)s].%(ext)s", "%(playlist_index)03d - %(title).80s",
// "%(uploader,channel|Unknown)s".
package outtmpl

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/Disaldr/go-dlp/media"
)

// Default — шаблон по умолчанию.
const Default = "%(title)s [%(id)s].%(ext)s"

var reField = regexp.MustCompile(`%\(([^)]*)\)([-#0+ ]*\d*(?:\.\d+)?)([sdifrqj])`)

// Fields превращает Info в словарь полей для шаблона.
func Fields(info *media.Info) map[string]any {
	b, _ := json.Marshal(info)
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	delete(m, "formats")
	delete(m, "entries")
	if len(info.RequestedFormats) > 0 {
		f := info.RequestedFormats[0]
		for _, rf := range info.RequestedFormats {
			if rf.HasVideo() {
				f = rf
			}
		}
		setDefault(m, "height", f.Height)
		setDefault(m, "width", f.Width)
		setDefault(m, "fps", f.FPS)
		setDefault(m, "resolution", f.Resolution())
		setDefault(m, "vcodec", f.VCodec)
		setDefault(m, "format_note", f.FormatNote)
	}
	return m
}

func setDefault(m map[string]any, k string, v any) {
	if _, ok := m[k]; ok {
		return
	}
	switch x := v.(type) {
	case int:
		if x == 0 {
			return
		}
	case float64:
		if x == 0 {
			return
		}
	case string:
		if x == "" {
			return
		}
	}
	m[k] = v
}

// Evaluate подставляет поля в шаблон. Каждое подставленное значение
// санитизируется (в нём не может появиться разделитель пути).
func Evaluate(tmpl string, fields map[string]any, restrict bool) string {
	const pct = "\x00PCT\x00"
	tmpl = strings.ReplaceAll(tmpl, "%%", pct)
	out := reField.ReplaceAllStringFunc(tmpl, func(m string) string {
		sm := reField.FindStringSubmatch(m)
		expr, flags, conv := sm[1], sm[2], sm[3]
		def := "NA"
		if i := strings.IndexByte(expr, '|'); i >= 0 {
			expr, def = expr[:i], expr[i+1:]
		}
		var val any
		for _, name := range strings.Split(expr, ",") {
			if v, ok := lookup(fields, strings.TrimSpace(name)); ok {
				val = v
				break
			}
		}
		var s string
		if val == nil {
			s = def
		} else {
			s = format(val, flags, conv)
		}
		return Sanitize(s, restrict)
	})
	return strings.ReplaceAll(out, pct, "%")
}

func lookup(fields map[string]any, name string) (any, bool) {
	var cur any = fields
	for _, part := range strings.Split(name, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[part]
		if !ok || cur == nil {
			return nil, false
		}
	}
	if s, ok := cur.(string); ok && s == "" {
		return nil, false
	}
	return cur, true
}

func format(v any, flags, conv string) string {
	switch conv {
	case "d", "i":
		var n int64
		switch x := v.(type) {
		case float64:
			n = int64(x)
		case int:
			n = int64(x)
		case int64:
			n = x
		case string:
			n, _ = strconv.ParseInt(x, 10, 64)
		}
		return fmt.Sprintf("%"+flags+"d", n)
	case "f":
		f, _ := v.(float64)
		return fmt.Sprintf("%"+flags+"f", f)
	case "j":
		b, _ := json.Marshal(v)
		return string(b)
	case "q":
		return strconv.Quote(fmt.Sprint(v))
	}
	var s string
	switch x := v.(type) {
	case float64:
		s = strconv.FormatFloat(x, 'f', -1, 64)
	case []any:
		parts := make([]string, len(x))
		for i, p := range x {
			parts[i] = fmt.Sprint(p)
		}
		s = strings.Join(parts, ", ")
	default:
		s = fmt.Sprint(x)
	}
	// точность .N для строк считаем в рунах
	if i := strings.IndexByte(flags, '.'); i >= 0 {
		if n, err := strconv.Atoi(flags[i+1:]); err == nil {
			r := []rune(s)
			if len(r) > n {
				s = string(r[:n])
			}
		}
		flags = flags[:i]
	}
	return fmt.Sprintf("%"+flags+"s", s)
}

var fullwidth = map[rune]rune{
	'/': '⧸', '\\': '⧹', ':': '：', '*': '＊', '?': '？', '"': '＂', '<': '＜', '>': '＞', '|': '｜',
}

// Sanitize делает строку безопасной для имени файла. restrict — только ASCII,
// без пробелов (как --restrict-filenames).
func Sanitize(s string, restrict bool) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 32 || r == 127:
			continue
		case restrict && (r > 127 || r == ' '):
			b.WriteRune('_')
		case restrict && strings.ContainsRune(`/\:*?"<>|&'!#$;`+"`", r):
			b.WriteRune('_')
		default:
			if fw, ok := fullwidth[r]; ok {
				b.WriteRune(fw)
			} else {
				b.WriteRune(r)
			}
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "." || out == ".." {
		out = "_"
	}
	return out
}

// Filename строит путь: dir + шаблон, длина каждого компонента ограничена.
func Filename(dir, tmpl string, info *media.Info, restrict bool) string {
	if tmpl == "" {
		tmpl = Default
	}
	name := Evaluate(tmpl, Fields(info), restrict)
	parts := strings.Split(filepath.ToSlash(name), "/")
	for i, p := range parts {
		parts[i] = truncateBytes(p, 240)
	}
	name = filepath.FromSlash(strings.Join(parts, "/"))
	if dir != "" && !filepath.IsAbs(name) {
		name = filepath.Join(dir, name)
	}
	return name
}

// truncateBytes обрезает имя до max байт, сохраняя расширение.
func truncateBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	ext := filepath.Ext(s)
	if len(ext) > 16 {
		ext = ""
	}
	base := s[:len(s)-len(ext)]
	limit := max - len(ext)
	for len(base) > limit {
		r := []rune(base)
		base = string(r[:len(r)-1])
	}
	return base + ext
}
