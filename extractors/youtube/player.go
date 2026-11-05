package youtube

// Расшифровка подписей YouTube: из плеера base.js извлекаются функция
// дешифровки подписи (параметр s → sig) и функция преобразования
// throttling-параметра n; обе исполняются JS-движком goja. Это снимает
// ограничение «клиента без JS» и открывает форматы клиентов web/tv.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"

	ex "github.com/Disaldr/go-dlp/extractor"
)

// player — извлечённые из base.js преобразования, привязанные к одному
// движку goja. Движок goja не потокобезопасен, поэтому доступ к нему
// сериализуется мьютексом; за время экстракции вызовов немного.
//
// Поддерживаются две схемы плеера:
//   - классическая: отдельные функции дешифровки подписи и n-параметра
//     (поля decipher/transformN);
//   - монолитная (player_es6, актуальная): единая функция-«подписыватель»
//     URL, через которую проходят и s, и n; для неё в goja исполняется весь
//     код плеера и вызывается обёртка-solver (поле solve, combined=true).
type player struct {
	mu         sync.Mutex
	vm         *goja.Runtime
	decipher   goja.Callable
	transformN goja.Callable
	hasN       bool

	combined bool          // true → используется монолитная схема es6
	solve    goja.Callable // __gdlp_solve(req) -> {sig, n}

	// dead — вызов JS был прерван по таймауту. Состояние движка после
	// прерывания не гарантировано, а зациклившийся код зациклится снова,
	// поэтому дальнейшие вызовы сразу завершаются ошибкой.
	dead bool
}

// Ограничения исполнения JS. Переменные (а не константы) — для тестов.
var (
	// jsLoadTimeout — исполнение кода плеера при сборке движка (es6-плеер
	// исполняется целиком; обычно это доли секунды).
	jsLoadTimeout = 30 * time.Second
	// jsCallTimeout — один вызов расшифровки подписи или n.
	jsCallTimeout = 5 * time.Second
)

// Параметры кеша плееров.
const (
	// maxCachedPlayers — сколько версий base.js держать в памяти. Каждая
	// версия — отдельный движок goja со всем кодом плеера; YouTube регулярно
	// выкатывает новые, а одновременно в ходу обычно одна-две.
	maxCachedPlayers = 4
	// playerErrTTL — сколько помнить неудачную загрузку (сеть, разбор). Это
	// не даёт скачивать base.js заново на каждое видео, но и не делает сбой
	// вечным. Отмена контекста вызывающего не кешируется вовсе.
	playerErrTTL = 5 * time.Minute
)

var (
	playerCacheMu sync.Mutex
	playerCache   = map[string]*cachedPlayer{}
	playerUseSeq  uint64

	// loadPlayerFn — подменяется в тестах.
	loadPlayerFn = loadPlayer
)

type cachedPlayer struct {
	done     chan struct{} // закрывается по окончании загрузки
	p        *player
	err      error
	failedAt time.Time
	// transient — загрузка прервана отменой контекста загружавшего; запись
	// удалена из кеша, ожидающим следует повторить попытку.
	transient bool
	used      uint64 // для вытеснения LRU; под playerCacheMu
}

func (e *cachedPlayer) loaded() bool {
	select {
	case <-e.done:
		return true
	default:
		return false
	}
}

// errJSInterrupted — исполнение JS прервано (таймаут или отмена контекста).
var errJSInterrupted = errors.New("player js interrupted")

// watchdog прерывает исполнение JS в vm по истечении d или по отмене ctx.
// Возвращаемую функцию нужно вызвать после исполнения: она снимает таймер
// и, если прерывание сработало, сбрасывает его и возвращает ошибку.
func watchdog(ctx context.Context, vm *goja.Runtime, d time.Duration) func() error {
	ctx, cancel := context.WithTimeout(ctx, d)
	fired := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		vm.Interrupt(errJSInterrupted)
		close(fired)
	})
	return func() error {
		defer cancel()
		if stop() {
			return nil
		}
		<-fired
		vm.ClearInterrupt()
		return fmt.Errorf("%w: %w", errJSInterrupted, context.Cause(ctx))
	}
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
// Плеер общий для всех видео, поэтому кешируется по URL. Параллельные
// запросы одного URL ждут единственной загрузки; ожидание прерывается
// отменой ctx. Ошибки помнятся playerErrTTL, отмена контекста — не помнится.
// В кеше не больше maxCachedPlayers версий (вытесняются давно не нужные).
func getPlayer(ctx context.Context, env *ex.Env, jsURL string) (*player, error) {
	for {
		playerCacheMu.Lock()
		e, ok := playerCache[jsURL]
		if ok && e.loaded() && e.err != nil && time.Since(e.failedAt) > playerErrTTL {
			delete(playerCache, jsURL)
			ok = false
		}
		playerUseSeq++
		if !ok {
			e = &cachedPlayer{done: make(chan struct{}), used: playerUseSeq}
			playerCache[jsURL] = e
			playerCacheMu.Unlock()
			return loadEntry(ctx, env, jsURL, e)
		}
		e.used = playerUseSeq
		playerCacheMu.Unlock()

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-e.done:
		}
		if e.transient {
			continue // загружавший отменил свой запрос — пробуем сами
		}
		return e.p, e.err
	}
}

func loadEntry(ctx context.Context, env *ex.Env, jsURL string, e *cachedPlayer) (*player, error) {
	p, err := loadPlayerFn(ctx, env, jsURL)

	playerCacheMu.Lock()
	defer playerCacheMu.Unlock()
	e.p, e.err = p, err
	if err != nil {
		e.failedAt = time.Now()
		if ctx.Err() != nil {
			e.transient = true
			if playerCache[jsURL] == e {
				delete(playerCache, jsURL)
			}
		}
	}
	close(e.done)
	evictPlayersLocked()
	return p, err
}

// evictPlayersLocked удаляет давно не использованные загруженные записи,
// пока их больше maxCachedPlayers. Загружающиеся записи не трогаются.
func evictPlayersLocked() {
	for len(playerCache) > maxCachedPlayers {
		var victim string
		var oldest uint64
		for k, e := range playerCache {
			if e.loaded() && (victim == "" || e.used < oldest) {
				victim, oldest = k, e.used
			}
		}
		if victim == "" {
			return
		}
		delete(playerCache, victim)
	}
}

func loadPlayer(ctx context.Context, env *ex.Env, jsURL string) (*player, error) {
	code, err := env.GetText(ctx, jsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("download player %s: %w", jsURL, err)
	}
	p, err := newPlayer(ctx, code)
	if err != nil && ctx.Err() == nil && !errors.Is(err, ex.ErrPlayerUnsupported) {
		// base.js скачан, но разобрать его не удалось: не найдена или не
		// скомпилировалась функция расшифровки подписи — формат плеера
		// изменился. Это сигнал обновить экстрактор (см. ErrPlayerUnsupported).
		err = fmt.Errorf("%w: %w", ex.ErrPlayerUnsupported, err)
	}
	return p, err
}

// newPlayer собирает движок goja с функциями расшифровки из исходника base.js.
// Сначала пробуется монолитная схема es6 (по маркеру функции-подписывателя);
// при её отсутствии — классическая схема с отдельными функциями. Исполнение
// JS ограничено jsLoadTimeout и прерывается отменой ctx.
func newPlayer(ctx context.Context, code string) (*player, error) {
	if name := findSignerName(code); name != "" {
		if p, err := newES6Player(ctx, code, name); err == nil {
			return p, nil
		} else {
			return nil, err
		}
	}
	return newClassicPlayer(ctx, code)
}

func newClassicPlayer(ctx context.Context, code string) (p *player, err error) {
	sigSrc, err := extractSigFunction(code)
	if err != nil {
		return nil, err
	}
	vm := goja.New()
	release := watchdog(ctx, vm, jsLoadTimeout)
	defer func() {
		if werr := release(); werr != nil {
			p, err = nil, werr
		}
	}()
	if _, err := vm.RunString(sigSrc + "\nvar __godlp_sig=" + sigEntryName + ";"); err != nil {
		return nil, fmt.Errorf("compile signature function: %w", err)
	}
	decipher, ok := goja.AssertFunction(vm.Get("__godlp_sig"))
	if !ok {
		return nil, fmt.Errorf("signature function is not callable")
	}
	p = &player{vm: vm, decipher: decipher}

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

// --- монолитная схема es6 ---
//
// Актуальный player_es6 не содержит классических функций: s и n проходят
// через единую функцию-«подписыватель» URL (строит объект-URL, применяет
// преобразования при чтении/вызове метода). yt-dlp (ejs-solver) находит эту
// функцию по маркеру — телу с инструкцией вида `obj.method("alr","yes")` —
// и исполняет весь код плеера в JS-движке. Здесь то же делается в goja:
// разворачивается IIFE плеера, весь код исполняется в глобальной области, а
// затем вызывается обёртка-solver, повторяющая createSolver из ejs.

// envStub — минимальное окружение браузера, которого достаточно плееру для
// исполнения вне DOM (повторяет setup из ejs-solver). Также заводит глобаль
// g — объект, который в оригинале передаётся в IIFE как параметр.
const envStub = `
if (typeof globalThis.XMLHttpRequest === "undefined") { globalThis.XMLHttpRequest = { prototype: {} }; }
globalThis.location = { hash:"", host:"www.youtube.com", hostname:"www.youtube.com", href:"https://www.youtube.com/watch?v=yt-dlp-wins", origin:"https://www.youtube.com", password:"", pathname:"/watch", port:"", protocol:"https:", search:"?v=yt-dlp-wins", username:"" };
if (typeof globalThis.document === "undefined") { globalThis.document = Object.create(null); }
if (typeof globalThis.navigator === "undefined") { globalThis.navigator = Object.create(null); }
if (typeof globalThis.self === "undefined") { globalThis.self = globalThis; }
if (typeof globalThis.window === "undefined") { globalThis.window = globalThis; }
var g = {};
var _yt_player = g;
`

// solverTmpl — обёртка над функцией-подписывателем (%s — её имя). Повторяет
// логику createSolver из ejs: строит URL, прогоняет через него s и n и
// считывает результаты. Вызов первого «непривилегированного» метода прототипа
// URL триггерит преобразование n.
const solverTmpl = `
function __gdlp_solve(req) {
  var fn = %s;
  var sig = req.sig, n = req.n;
  var url = fn("https://youtube.com/watch?v=yt-dlp-wins", "s", sig ? encodeURIComponent(sig) : undefined);
  url.set("n", n);
  var proto = Object.getPrototypeOf(url);
  var keys = Object.keys(proto).concat(Object.getOwnPropertyNames(proto));
  for (var i = 0; i < keys.length; i++) {
    var key = keys[i];
    if (["constructor","set","get","clone"].indexOf(key) === -1) { url[key](); break; }
  }
  var s = url.get("s");
  var nn = url.get("n");
  return { sig: s == null ? null : decodeURIComponent(s), n: nn == null ? null : nn };
}
`

// newES6Player разворачивает плеер и готовит обёртку-solver в goja.
func newES6Player(ctx context.Context, code, signer string) (p *player, err error) {
	body, err := unwrapPlayerBody(code)
	if err != nil {
		return nil, err
	}
	vm := goja.New()
	release := watchdog(ctx, vm, jsLoadTimeout)
	defer func() {
		if werr := release(); werr != nil {
			p, err = nil, werr
		}
	}()
	if _, err := vm.RunString(envStub); err != nil {
		return nil, fmt.Errorf("es6 env stub: %w", err)
	}
	// Весь код плеера исполняется в глобальной области: объявления var и
	// function становятся глобальными, как в оригинальном замыкании. Ошибки
	// побочных эффектов не фатальны, если функция-подписыватель всё же задана.
	_, runErr := vm.RunString(body)
	if fn := vm.Get(signer); fn == nil || goja.IsUndefined(fn) || goja.IsNull(fn) {
		if runErr != nil {
			return nil, fmt.Errorf("run es6 player: %w", runErr)
		}
		return nil, fmt.Errorf("es6 signer %q is undefined after run", signer)
	}
	if _, err := vm.RunString(fmt.Sprintf(solverTmpl, signer)); err != nil {
		return nil, fmt.Errorf("es6 solver: %w", err)
	}
	solve, ok := goja.AssertFunction(vm.Get("__gdlp_solve"))
	if !ok {
		return nil, fmt.Errorf("es6 solver is not callable")
	}
	p = &player{vm: vm, combined: true, solve: solve, hasN: true}
	// Контроль работоспособности: подпись должна преобразовываться.
	probe := "0123456789ABCDEFGHIJ0123456789ABCDEFGHIJ0123456789ABCDEFGHIJ0123456789ABCDEFGHIJ"
	out, err := p.solveField("sig", probe)
	if err != nil || out == "" {
		return nil, fmt.Errorf("es6 signer %q failed probe: %v", signer, err)
	}
	return p, nil
}

// solveField вызывает solver и возвращает поле sig или n. Вызывающий держит mu
// (кроме newES6Player, где движок ещё не опубликован).
func (p *player) solveField(field, in string) (string, error) {
	r, err := p.solve(goja.Undefined(), p.vm.ToValue(map[string]any{field: in}))
	if err != nil {
		return "", err
	}
	v := r.ToObject(p.vm).Get(field)
	if v == nil || goja.IsNull(v) || goja.IsUndefined(v) {
		return "", fmt.Errorf("solver returned null %s", field)
	}
	return v.String(), nil
}

// reSignerMarker — маркер функции-подписывателя: инструкция-выражение вида
// `obj.method("alr","yes")` (obj — простой идентификатор), стоящая отдельным
// оператором. Это фингерпринт из ejs-solver (структура asdasd).
var reSignerMarker = regexp.MustCompile(`[;{]\s*[A-Za-z0-9_$]+\.[A-Za-z0-9_$]+\("alr","yes"\)`)

// reSignerHeader — заголовки функций в одной из форм: `function N(`,
// `N=function(`, `var N=function(`.
var reSignerHeader = regexp.MustCompile(`(?:function\s+([A-Za-z0-9_$]+)|(?:var\s+)?([A-Za-z0-9_$]+)\s*=\s*function)\s*\(`)

// findSignerName возвращает имя функции-подписывателя URL в es6-плеере или ""
// (тогда используется классическая схема). Ищется первая функция указанных
// форм, в теле которой присутствует маркер; синхронные (не async) функции.
func findSignerName(code string) string {
	for _, loc := range reSignerHeader.FindAllStringSubmatchIndex(code, -1) {
		name := ""
		if loc[2] >= 0 {
			name = code[loc[2]:loc[3]]
		} else if loc[4] >= 0 {
			name = code[loc[4]:loc[5]]
		}
		if name == "" {
			continue
		}
		if strings.HasSuffix(code[:loc[0]], "async ") {
			continue
		}
		open := strings.IndexByte(code[loc[1]-1:], '{')
		if open < 0 {
			continue
		}
		open += loc[1] - 1
		end, ok := matchBrace(code, open)
		if !ok {
			continue
		}
		if reSignerMarker.MatchString(code[open : end+1]) {
			return name
		}
	}
	return ""
}

// unwrapPlayerBody извлекает тело IIFE плеера:
// `var _yt_player={};(function(g){var window=this;<BODY>})(_yt_player);`.
// IIFE — последний оператор файла, поэтому его закрытие надёжно находится по
// последнему `})(`. Сканировать баланс скобок по всему плееру нельзя: в коде
// есть regex-литералы с несбалансированными `{`/`}`.
func unwrapPlayerBody(code string) (string, error) {
	i := strings.Index(code, "(function(")
	if i < 0 {
		return "", fmt.Errorf("player IIFE not found")
	}
	open := strings.IndexByte(code[i:], '{')
	if open < 0 {
		return "", fmt.Errorf("player IIFE body not found")
	}
	open += i
	end := strings.LastIndex(code, "})(")
	if end <= open {
		return "", fmt.Errorf("player IIFE close not found")
	}
	body := strings.TrimSpace(code[open+1 : end])
	body = strings.TrimPrefix(body, "var window=this;")
	return body, nil
}

// call исполняет fn над движком под мьютексом с таймаутом jsCallTimeout.
// После прерывания плеер помечается мёртвым.
func (p *player) call(fn func() (string, error)) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dead {
		return "", fmt.Errorf("%w earlier; player disabled", errJSInterrupted)
	}
	release := watchdog(context.Background(), p.vm, jsCallTimeout)
	out, err := fn()
	if werr := release(); werr != nil {
		p.dead = true
		return "", werr
	}
	return out, err
}

// decryptSig расшифровывает подпись s.
func (p *player) decryptSig(s string) (string, error) {
	return p.call(func() (string, error) {
		if p.combined {
			return p.solveField("sig", s)
		}
		v, err := p.decipher(goja.Undefined(), p.vm.ToValue(s))
		if err != nil {
			return "", err
		}
		return v.String(), nil
	})
}

// decryptN преобразует throttling-параметр n (если функция найдена).
func (p *player) decryptN(n string) (string, bool) {
	if !p.combined && !p.hasN {
		return "", false
	}
	out, err := p.call(func() (string, error) {
		if p.combined {
			return p.solveField("n", n)
		}
		v, err := p.transformN(goja.Undefined(), p.vm.ToValue(n))
		if err != nil {
			return "", err
		}
		return v.String(), nil
	})
	if err != nil {
		return "", false
	}
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
