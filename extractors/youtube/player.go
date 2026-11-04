package youtube

// Расшифровка подписей YouTube: из плеера base.js извлекаются функция
// дешифровки подписи (параметр s → sig) и функция преобразования
// throttling-параметра n; обе исполняются JS-движком goja. Это снимает
// ограничение «клиента без JS» и открывает форматы клиентов web/tv.

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/dop251/goja"

	ex "github.com/Disaldr/go-dlp/extractor"
)

// player — извлечённые из base.js преобразования, привязанные к одному
// движку goja. Движок goja не потокобезопасен, поэтому доступ к нему
// сериализуется мьютексом; за время экстракции вызовов немного.
type player struct {
	mu         sync.Mutex
	vm         *goja.Runtime
	decipher   goja.Callable
	transformN goja.Callable
	hasN       bool
}

var (
	playerCacheMu sync.Mutex
	playerCache   = map[string]*cachedPlayer{}
)

type cachedPlayer struct {
	once sync.Once
	p    *player
	err  error
}

var rePlayerID = regexp.MustCompile(`/player/([0-9a-fA-F]{8,})/`)

// playerURL извлекает URL base.js из ytcfg веб-страницы и нормализует его.
func playerURL(ytcfg any, page string) string {
	js := ex.FirstStr(ytcfg, ex.P("PLAYER_JS_URL"), ex.P("WEB_PLAYER_CONTEXT_CONFIGS", "WEB_PLAYER_CONTEXT_CONFIG_ID_KEVLAR", "jsUrl"))
	if js == "" {
		if m := regexp.MustCompile(`"jsUrl":"([^"]+)"`).FindStringSubmatch(page); m != nil {
			js = strings.ReplaceAll(m[1], `\/`, "/")
		}
	}
	if js == "" {
		return ""
	}
	return ex.URLJoin(baseURL, js)
}

// getPlayer возвращает (кешированные) преобразования для данного base.js.
// Плеер общий для всех видео в рамках запуска, поэтому кешируется по URL.
func getPlayer(ctx context.Context, env *ex.Env, jsURL string) (*player, error) {
	playerCacheMu.Lock()
	entry, ok := playerCache[jsURL]
	if !ok {
		entry = &cachedPlayer{}
		playerCache[jsURL] = entry
	}
	playerCacheMu.Unlock()

	entry.once.Do(func() {
		entry.p, entry.err = loadPlayer(ctx, env, jsURL)
	})
	return entry.p, entry.err
}

func loadPlayer(ctx context.Context, env *ex.Env, jsURL string) (*player, error) {
	code, err := env.GetText(ctx, jsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("download player %s: %w", jsURL, err)
	}
	return newPlayer(code)
}

// newPlayer собирает движок goja с функциями расшифровки из исходника base.js.
func newPlayer(code string) (*player, error) {
	sigSrc, err := extractSigFunction(code)
	if err != nil {
		return nil, err
	}
	vm := goja.New()
	if _, err := vm.RunString(sigSrc + "\nvar __godlp_sig=" + sigEntryName + ";"); err != nil {
		return nil, fmt.Errorf("compile signature function: %w", err)
	}
	decipher, ok := goja.AssertFunction(vm.Get("__godlp_sig"))
	if !ok {
		return nil, fmt.Errorf("signature function is not callable")
	}
	p := &player{vm: vm, decipher: decipher}

	// n-функция необязательна: без неё загрузка работает, но YouTube может
	// резать скорость. Поэтому её отсутствие не делает плеер непригодным.
	if nSrc, name, err := extractNFunction(code); err == nil {
		if _, err := vm.RunString(nSrc + "\nvar __godlp_n=" + name + ";"); err == nil {
			if fn, ok := goja.AssertFunction(vm.Get("__godlp_n")); ok {
				p.transformN, p.hasN = fn, true
			}
		}
	}
	return p, nil
}

const sigEntryName = "__godlp_sig_fn"

// decryptSig расшифровывает подпись s.
func (p *player) decryptSig(s string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, err := p.decipher(goja.Undefined(), p.vm.ToValue(s))
	if err != nil {
		return "", err
	}
	return v.String(), nil
}

// decryptN преобразует throttling-параметр n (если функция найдена).
func (p *player) decryptN(n string) (string, bool) {
	if !p.hasN {
		return "", false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	v, err := p.transformN(goja.Undefined(), p.vm.ToValue(n))
	if err != nil {
		return "", false
	}
	out := v.String()
	// Признак провала: функция вернула исходное значение или начало с "enhanced_except".
	if out == "" || out == n || strings.HasPrefix(out, "enhanced_except") {
		return "", false
	}
	return out, true
}

// --- извлечение функций из base.js ---

var sigNamePatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b[a-zA-Z0-9$]+&&\([a-zA-Z0-9$]+=([a-zA-Z0-9$]{2,})\(decodeURIComponent\(`),
	regexp.MustCompile(`([a-zA-Z0-9$]+)\s*=\s*function\(\s*[a-zA-Z0-9$]+\s*\)\s*{\s*[a-zA-Z0-9$]+\s*=\s*[a-zA-Z0-9$]+\.split\(\s*""\s*\)\s*;`),
	regexp.MustCompile(`(?:\b|[^a-zA-Z0-9$])([a-zA-Z0-9$]{2,})\s*=\s*function\(\s*a\s*\)\s*{\s*a\s*=\s*a\.split\(\s*""\s*\)`),
	regexp.MustCompile(`\bm=([a-zA-Z0-9$]{2,})\(decodeURIComponent\(h\.s\)\)`),
	regexp.MustCompile(`\bc&&\(c=([a-zA-Z0-9$]{2,})\(decodeURIComponent\(c\)\)`),
	regexp.MustCompile(`(?:\.sig\|\||[a-zA-Z0-9$]+\.set\([^,]+,\s*(?:encodeURIComponent\s*\()?\s*)([a-zA-Z0-9$]{2,})\(`),
	regexp.MustCompile(`["']signature["']\s*,\s*([a-zA-Z0-9$]+)\(`),
}

// extractSigFunction возвращает самодостаточный JS: объект-помощник +
// функция дешифровки под именем sigEntryName.
func extractSigFunction(code string) (string, error) {
	var name string
	for _, re := range sigNamePatterns {
		if m := re.FindStringSubmatch(code); m != nil {
			name = m[len(m)-1] // последняя группа — имя функции
			break
		}
	}
	if name == "" {
		return "", fmt.Errorf("could not locate signature function in player")
	}
	body, args, err := extractFunction(code, name)
	if err != nil {
		return "", fmt.Errorf("signature function %q: %w", name, err)
	}
	helper, err := extractHelperObject(code, body)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s\nfunction %s(%s){%s}", helper, sigEntryName, args, body), nil
}

var nNamePatterns = []*regexp.Regexp{
	regexp.MustCompile(`\.get\("n"\)\)&&\([a-zA-Z0-9$]=([a-zA-Z0-9$]+)(?:\[(\d+)\])?\(`),
	regexp.MustCompile(`&&\([a-zA-Z]="nn"\[\+?[a-zA-Z0-9$]+\],[a-zA-Z0-9$]+=[a-zA-Z0-9$]+\.get\([a-zA-Z]\)\)&&\([a-zA-Z0-9$]+=([a-zA-Z0-9$]+)(?:\[(\d+)\])?\(`),
	regexp.MustCompile(`\b([a-zA-Z0-9$]+)\s*=\s*function\(\s*[a-zA-Z0-9$]+\s*\)\s*{\s*var\s+[a-zA-Z0-9$]+\s*=\s*[a-zA-Z0-9$]+\.split\(\s*""\s*\)\s*,`),
	regexp.MustCompile(`\bc\s*=\s*([a-zA-Z0-9$]+)(?:\[(\d+)\])?\(c\)`),
}

// extractNFunction возвращает JS с функцией преобразования n и её имя.
func extractNFunction(code string) (src, name string, err error) {
	for _, re := range nNamePatterns {
		m := re.FindStringSubmatch(code)
		if m == nil {
			continue
		}
		cand := m[1]
		idx := ""
		if len(m) > 2 {
			idx = m[2]
		}
		// Имя может быть элементом массива: var X=[realName]; X[0](n).
		if idx != "" {
			if real, ok := arrayElement(code, cand, idx); ok {
				cand = real
			} else {
				continue
			}
		}
		body, args, ferr := extractFunction(code, cand)
		if ferr != nil {
			continue
		}
		return fmt.Sprintf("function %s(%s){%s}", cand, args, body), cand, nil
	}
	return "", "", fmt.Errorf("could not locate n-transform function in player")
}

// arrayElement разрешает var name=[a,b,c]; → name[idx].
func arrayElement(code, name, idx string) (string, bool) {
	re := regexp.MustCompile(`(?:var\s+)?` + regexp.QuoteMeta(name) + `\s*=\s*\[([^\]]*)\]`)
	m := re.FindStringSubmatch(code)
	if m == nil {
		return "", false
	}
	parts := strings.Split(m[1], ",")
	i, err := strconv.Atoi(idx)
	if err != nil || i < 0 || i >= len(parts) {
		return "", false
	}
	return strings.TrimSpace(parts[i]), true
}

// extractFunction находит определение функции name и возвращает её тело
// (без обрамляющих скобок) и список аргументов. Поддерживаются формы
// `name=function(a){...}`, `function name(a){...}`, `var name=function...`
// и метод объекта `name:function(a){...}`.
func extractFunction(code, name string) (body, args string, err error) {
	q := regexp.QuoteMeta(name)
	headerRe := regexp.MustCompile(`(?:function\s+` + q + `|(?:var\s+|,\s*)?` + q + `\s*=\s*function|` + q + `\s*:\s*function)\s*\(([^)]*)\)\s*{`)
	loc := headerRe.FindStringSubmatchIndex(code)
	if loc == nil {
		return "", "", fmt.Errorf("definition not found")
	}
	args = code[loc[2]:loc[3]]
	open := loc[1] - 1 // позиция '{'
	end, ok := matchBrace(code, open)
	if !ok {
		return "", "", fmt.Errorf("unbalanced braces")
	}
	return code[open+1 : end], args, nil
}

var reObjRef = regexp.MustCompile(`([a-zA-Z_$][a-zA-Z0-9_$]*)\.[a-zA-Z0-9_$]+\(`)

// extractHelperObject находит объект-помощник, на методы которого ссылается
// тело функции дешифровки, и возвращает его определение `var Obj={...};`.
func extractHelperObject(code, sigBody string) (string, error) {
	names := map[string]bool{}
	for _, m := range reObjRef.FindAllStringSubmatch(sigBody, -1) {
		names[m[1]] = true
	}
	for name := range names {
		re := regexp.MustCompile(`(?:var\s+)?` + regexp.QuoteMeta(name) + `\s*=\s*{`)
		loc := re.FindStringIndex(code)
		if loc == nil {
			continue
		}
		open := loc[1] - 1
		end, ok := matchBrace(code, open)
		if !ok {
			continue
		}
		return fmt.Sprintf("var %s=%s;", name, code[open:end+1]), nil
	}
	return "", nil // функция может не использовать объект-помощник
}

// matchBrace возвращает индекс '}', парного '{' в позиции open, учитывая
// строки, шаблонные литералы, регэкспы и комментарии.
func matchBrace(s string, open int) (int, bool) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, true
			}
		case '"', '\'':
			if j, ok := skipString(s, i, s[i]); ok {
				i = j
			} else {
				return 0, false
			}
		case '`':
			if j, ok := skipTemplate(s, i); ok {
				i = j
			} else {
				return 0, false
			}
		case '/':
			if i+1 < len(s) && s[i+1] == '/' {
				i = skipTo(s, i+2, '\n')
			} else if i+1 < len(s) && s[i+1] == '*' {
				i = skipBlockComment(s, i+2)
			}
		}
	}
	return 0, false
}

func skipString(s string, i int, quote byte) (int, bool) {
	for i++; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case quote:
			return i, true
		case '\n':
			return 0, false
		}
	}
	return 0, false
}

func skipTemplate(s string, i int) (int, bool) {
	for i++; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '`':
			return i, true
		}
	}
	return 0, false
}

func skipTo(s string, i int, b byte) int {
	for ; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return len(s) - 1
}

func skipBlockComment(s string, i int) int {
	for ; i+1 < len(s); i++ {
		if s[i] == '*' && s[i+1] == '/' {
			return i + 1
		}
	}
	return len(s) - 1
}
