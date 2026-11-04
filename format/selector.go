package format

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/Disaldr/go-dlp/media"
)

// Selection — выбранный набор форматов; больше одного — нужно слияние.
type Selection []*media.Format

// ID — идентификатор вида "137+140".
func (s Selection) ID() string {
	ids := make([]string, len(s))
	for i, f := range s {
		ids[i] = f.FormatID
	}
	return strings.Join(ids, "+")
}

// Selector — скомпилированное выражение выбора форматов.
type Selector struct {
	spec  string
	items []node // через ',' — несколько независимых загрузок
}

// String возвращает исходное выражение.
func (s *Selector) String() string { return s.spec }

// node — узел выражения.
type node interface {
	// eval возвращает выборки-кандидаты от лучшей к худшей.
	eval(formats []*media.Format) []Selection
}

type altNode struct{ alts []node }    // a/b/c
type mergeNode struct{ parts []node } // a+b
type atomNode struct {                // best[height<=720]
	kind    string
	nth     int
	filters []filter
}

// Compile разбирает выражение.
//
// Грамматика (подмножество yt-dlp):
//
//	spec   := alt (',' alt)*
//	alt    := merge ('/' merge)*
//	merge  := single ('+' single)*
//	single := (atom | '(' alt ')') filter*
//	atom   := b|best|w|worst|bv|bestvideo|ba|bestaudio|wv|wa|bv*|ba*|wv*|wa*|all|<ext>|<format_id>  [.N]
//	filter := '[' field op ['?'] value ']'   op: = != < <= > >= ^= $= *= ~=
func Compile(spec string) (*Selector, error) {
	p := &parser{s: strings.TrimSpace(spec)}
	if p.s == "" {
		p.s = "best"
	}
	sel := &Selector{spec: p.s}
	for {
		n, err := p.parseAlt()
		if err != nil {
			return nil, err
		}
		sel.items = append(sel.items, n)
		p.skipSpace()
		if p.eof() {
			break
		}
		if p.peek() != ',' {
			return nil, p.errorf("unexpected %q", p.peek())
		}
		p.pos++
	}
	return sel, nil
}

// MustCompile — Compile с паникой.
func MustCompile(spec string) *Selector {
	s, err := Compile(spec)
	if err != nil {
		panic(err)
	}
	return s
}

// Select выбирает форматы. formats могут быть в любом порядке.
// Для каждой части через ',' возвращается одна выборка; если ничего не подошло — ошибка.
func (s *Selector) Select(formats []*media.Format) ([]Selection, error) {
	sorted := slices.Clone(formats)
	Sort(sorted)
	slices.Reverse(sorted) // лучший — первый
	var out []Selection
	for _, it := range s.items {
		c := it.eval(sorted)
		if len(c) == 0 {
			continue
		}
		out = append(out, c[0])
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("requested format is not available: %s", s.spec)
	}
	return out, nil
}

func (n *altNode) eval(fs []*media.Format) []Selection {
	for _, a := range n.alts {
		if r := a.eval(fs); len(r) > 0 {
			return r
		}
	}
	return nil
}

func (n *mergeNode) eval(fs []*media.Format) []Selection {
	var result Selection
	for _, p := range n.parts {
		r := p.eval(fs)
		if len(r) == 0 {
			return nil
		}
		result = append(result, r[0]...)
	}
	// нельзя сливать два формата с видео без аудио-форматов и т.п. — как yt-dlp,
	// проверяем лишь, что не больше одного видеопотока
	videos := 0
	for _, f := range result {
		if f.HasVideo() {
			videos++
		}
	}
	if videos > 1 {
		return nil
	}
	return []Selection{result}
}

func (n *atomNode) eval(fs []*media.Format) []Selection {
	var cand []*media.Format
	for _, f := range fs {
		if !n.matchKind(f) {
			continue
		}
		ok := true
		for _, flt := range n.filters {
			if !flt.match(f) {
				ok = false
				break
			}
		}
		if ok {
			cand = append(cand, f)
		}
	}
	if len(cand) == 0 && (n.kind == "b" || n.kind == "w") {
		// как в yt-dlp: если нет форматов с видео и аудио сразу (аудио-сайты,
		// немые видео), b/w выбирают среди всех форматов
		hasCombined := false
		for _, f := range fs {
			if f.HasVideo() && f.HasAudio() {
				hasCombined = true
				break
			}
		}
		if !hasCombined {
			fallback := *n
			fallback.kind += "*"
			return fallback.eval(fs)
		}
	}
	if len(cand) == 0 {
		return nil
	}
	worst := strings.HasPrefix(n.kind, "w")
	if worst {
		slices.Reverse(cand)
	}
	if n.kind == "all" {
		return []Selection{cand}
	}
	idx := n.nth - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(cand) {
		return nil
	}
	return []Selection{{cand[idx]}}
}

func (n *atomNode) matchKind(f *media.Format) bool {
	switch n.kind {
	case "b", "w":
		return f.HasVideo() && f.HasAudio()
	case "bv", "wv":
		return f.IsVideoOnly()
	case "ba", "wa":
		return f.IsAudioOnly()
	case "bv*", "wv*":
		return f.HasVideo()
	case "ba*", "wa*":
		return f.HasAudio()
	case "all", "b*", "w*":
		return true
	}
	return true
}

var kindAliases = map[string]string{
	"best": "b", "b": "b", "worst": "w", "w": "w",
	"bestvideo": "bv", "bv": "bv", "worstvideo": "wv", "wv": "wv",
	"bestaudio": "ba", "ba": "ba", "worstaudio": "wa", "wa": "wa",
	"bestvideo*": "bv*", "bv*": "bv*", "worstvideo*": "wv*", "wv*": "wv*",
	"bestaudio*": "ba*", "ba*": "ba*", "worstaudio*": "wa*", "wa*": "wa*",
	"best*": "b*", "b*": "b*", "worst*": "w*", "w*": "w*",
	"all": "all",
}

var knownExts = map[string]bool{
	"mp4": true, "webm": true, "m4a": true, "mp3": true, "ogg": true, "flv": true,
	"3gp": true, "aac": true, "wav": true, "opus": true, "mkv": true, "mov": true,
}

// --- фильтры ---

type filter struct {
	field    string
	op       string
	optional bool
	value    string
	neg      bool
}

var numericFields = map[string]bool{
	"height": true, "width": true, "fps": true, "tbr": true, "abr": true, "vbr": true,
	"asr": true, "filesize": true, "filesize_approx": true, "fs": true, "audio_channels": true,
	"quality": true, "preference": true,
}

func numField(f *media.Format, name string) (float64, bool) {
	var v float64
	switch name {
	case "height":
		v = float64(f.Height)
	case "width":
		v = float64(f.Width)
	case "fps":
		v = f.FPS
	case "tbr":
		v = f.TBR
	case "abr":
		v = f.ABR
	case "vbr":
		v = f.VBR
	case "asr":
		v = float64(f.ASR)
	case "filesize":
		v = float64(f.Filesize)
	case "filesize_approx":
		v = float64(f.FilesizeApprox)
	case "fs":
		v = float64(f.Size())
	case "audio_channels":
		v = float64(f.AudioChannels)
	case "quality":
		return f.Quality, true
	case "preference":
		return float64(f.Preference), true
	}
	return v, v != 0
}

func strField(f *media.Format, name string) (string, bool) {
	var v string
	switch name {
	case "ext":
		v = f.Ext
	case "vcodec":
		v = f.VCodec
	case "acodec":
		v = f.ACodec
	case "format_id", "id":
		v = f.FormatID
	case "protocol", "proto":
		v = f.Protocol
	case "language", "lang":
		v = f.Language
	case "container":
		v = f.Container
	case "dynamic_range":
		v = f.DynamicRange
	case "format_note":
		v = f.FormatNote
	case "url":
		v = f.URL
	}
	return v, v != ""
}

func parseNumber(s string) (float64, error) {
	s = strings.TrimSpace(s)
	mult := 1.0
	upper := strings.ToUpper(s)
	for _, suf := range []struct {
		s string
		m float64
	}{{"KIB", 1024}, {"MIB", 1 << 20}, {"GIB", 1 << 30}, {"KB", 1000}, {"MB", 1e6}, {"GB", 1e9}, {"K", 1024}, {"M", 1 << 20}, {"G", 1 << 30}} {
		if strings.HasSuffix(upper, suf.s) {
			mult = suf.m
			s = s[:len(s)-len(suf.s)]
			break
		}
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f * mult, err
}

func (fl filter) match(f *media.Format) bool {
	if numericFields[fl.field] {
		v, present := numField(f, fl.field)
		if !present {
			return fl.optional
		}
		want, err := parseNumber(fl.value)
		if err != nil {
			return false
		}
		switch fl.op {
		case "=":
			return v == want
		case "!=":
			return v != want
		case "<":
			return v < want
		case "<=":
			return v <= want
		case ">":
			return v > want
		case ">=":
			return v >= want
		}
		return false
	}
	v, present := strField(f, fl.field)
	if !present {
		return fl.optional
	}
	var r bool
	switch fl.op {
	case "=":
		r = v == fl.value
	case "^=":
		r = strings.HasPrefix(v, fl.value)
	case "$=":
		r = strings.HasSuffix(v, fl.value)
	case "*=", "~=":
		r = strings.Contains(v, fl.value)
	}
	if fl.neg {
		return !r
	}
	return r
}

// --- парсер ---

type parser struct {
	s   string
	pos int
}

func (p *parser) eof() bool  { return p.pos >= len(p.s) }
func (p *parser) peek() byte { return p.s[p.pos] }
func (p *parser) skipSpace() {
	for !p.eof() && p.s[p.pos] == ' ' {
		p.pos++
	}
}
func (p *parser) errorf(f string, a ...any) error {
	return fmt.Errorf("invalid format spec %q at %d: %s", p.s, p.pos, fmt.Sprintf(f, a...))
}

func (p *parser) parseAlt() (node, error) {
	var alts []node
	for {
		n, err := p.parseMerge()
		if err != nil {
			return nil, err
		}
		alts = append(alts, n)
		p.skipSpace()
		if p.eof() || p.peek() != '/' {
			break
		}
		p.pos++
	}
	if len(alts) == 1 {
		return alts[0], nil
	}
	return &altNode{alts}, nil
}

func (p *parser) parseMerge() (node, error) {
	var parts []node
	for {
		n, err := p.parseSingle()
		if err != nil {
			return nil, err
		}
		parts = append(parts, n)
		p.skipSpace()
		if p.eof() || p.peek() != '+' {
			break
		}
		p.pos++
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	return &mergeNode{parts}, nil
}

func (p *parser) parseSingle() (node, error) {
	p.skipSpace()
	if p.eof() {
		return nil, p.errorf("unexpected end")
	}
	if p.peek() == '(' {
		p.pos++
		n, err := p.parseAlt()
		if err != nil {
			return nil, err
		}
		p.skipSpace()
		if p.eof() || p.peek() != ')' {
			return nil, p.errorf("missing ')'")
		}
		p.pos++
		filters, err := p.parseFilters()
		if err != nil {
			return nil, err
		}
		if len(filters) > 0 {
			return &filteredNode{inner: n, filters: filters}, nil
		}
		return n, nil
	}
	start := p.pos
	for !p.eof() && !strings.ContainsRune("/+,[]() ", rune(p.peek())) {
		p.pos++
	}
	word := p.s[start:p.pos]
	a := &atomNode{nth: 1}
	if word == "" {
		a.kind = "b*"
	} else {
		base, nth := word, 1
		if i := strings.LastIndexByte(word, '.'); i > 0 {
			if n, err := strconv.Atoi(word[i+1:]); err == nil && n > 0 {
				if _, ok := kindAliases[word[:i]]; ok {
					base, nth = word[:i], n
				}
			}
		}
		a.nth = nth
		switch {
		case kindAliases[base] != "":
			a.kind = kindAliases[base]
		case knownExts[base]:
			a.kind = "ext"
			a.filters = append(a.filters, filter{field: "ext", op: "=", value: base})
		default:
			a.kind = "id"
			a.filters = append(a.filters, filter{field: "format_id", op: "=", value: base})
		}
	}
	filters, err := p.parseFilters()
	if err != nil {
		return nil, err
	}
	a.filters = append(a.filters, filters...)
	return a, nil
}

func (p *parser) parseFilters() ([]filter, error) {
	var out []filter
	for !p.eof() && p.peek() == '[' {
		end := strings.IndexByte(p.s[p.pos:], ']')
		if end < 0 {
			return nil, p.errorf("missing ']'")
		}
		body := p.s[p.pos+1 : p.pos+end]
		p.pos += end + 1
		f, err := parseFilter(body)
		if err != nil {
			return nil, p.errorf("%v", err)
		}
		out = append(out, f)
	}
	return out, nil
}

func parseFilter(body string) (filter, error) {
	i := 0
	for i < len(body) && (unicode.IsLetter(rune(body[i])) || body[i] == '_') {
		i++
	}
	f := filter{field: strings.ToLower(body[:i])}
	if f.field == "" {
		return f, fmt.Errorf("bad filter %q", body)
	}
	rest := strings.TrimSpace(body[i:])
	if strings.HasPrefix(rest, "!") && len(rest) > 1 && rest[1] != '=' {
		f.neg = true
		rest = rest[1:]
	}
	for _, op := range []string{"<=", ">=", "!=", "^=", "$=", "*=", "~=", "=", "<", ">"} {
		if strings.HasPrefix(rest, op) {
			f.op = op
			rest = rest[len(op):]
			break
		}
	}
	if f.op == "" {
		return f, fmt.Errorf("bad filter operator in %q", body)
	}
	if f.op == "!=" && !numericFields[f.field] {
		f.op, f.neg = "=", true
	}
	if strings.HasPrefix(rest, "?") {
		f.optional = true
		rest = rest[1:]
	}
	f.value = strings.Trim(strings.TrimSpace(rest), `"'`)
	return f, nil
}

type filteredNode struct {
	inner   node
	filters []filter
}

func (n *filteredNode) eval(fs []*media.Format) []Selection {
	var keep []*media.Format
	for _, f := range fs {
		ok := true
		for _, fl := range n.filters {
			if !fl.match(f) {
				ok = false
				break
			}
		}
		if ok {
			keep = append(keep, f)
		}
	}
	return n.inner.eval(keep)
}
