// Package script — экстракторы на JavaScript, исполняемые движком goja.
//
// Скриптовые экстракторы можно добавлять и исправлять без пересборки
// приложения: они поставляются подписанным пакетом (см. Pack, Updater) и
// подключаются в реестр на лету (Manager). Скрипт регистрирует один или
// несколько экстракторов вызовом extractor({...}):
//
//	extractor({
//	  name: "example",
//	  match: [String.raw`^https?://(?:www\.)?example\.com/v/(\d+)`],
//	  extract(url, m) {
//	    const r = http.get(url);
//	    return { id: m[1], title: "...", formats: [{ url: "..." }] };
//	  },
//	});
//
// match — регулярные выражения в синтаксисе Go (RE2); литерал /.../ тоже
// допустим, если его source совместим с RE2. extract получает URL и
// подгруппы первого совпавшего выражения и возвращает объект в формате
// media.Info (поля как в JSON yt-dlp: id, title, formats, entries, ...).
// API хоста описан в host.go; файловой системы и запуска процессов нет.
package script

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"golang.org/x/net/publicsuffix"

	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/media"
)

// HostAPI — версия API хоста. Пакет со скриптами, требующими большей версии,
// отвергается: старое приложение не загрузит то, что не сможет исполнить.
const HostAPI = 1

// ExtractTimeout — предельное время одного вызова extract (вместе с сетью).
var ExtractTimeout = 2 * time.Minute

// Extractor — экстрактор, определённый скриптом.
type Extractor struct {
	name        string
	description string
	priority    int
	patterns    []*regexp.Regexp
	hosts       []string // подсказка от скрипта (в песочнице НЕ расширяет allowlist)
	sandboxed   bool     // источник недоверенный (сетевое обновление)

	file    string
	program *goja.Program

	// fallback — встроенный экстрактор с тем же именем, который подменён
	// скриптом. Используется, если скрипт упал с ошибкой, не являющейся
	// категорией (то есть, скорее всего, ошибкой самого скрипта).
	fallback ex.Extractor
}

func (e *Extractor) Name() string        { return e.name }
func (e *Extractor) Description() string { return e.description }
func (e *Extractor) Priority() int       { return e.priority }

// Source — имя файла скрипта.
func (e *Extractor) Source() string { return e.file }

// Fallback — подменённый встроенный экстрактор (или nil).
func (e *Extractor) Fallback() ex.Extractor { return e.fallback }

// allowedHosts — allowlist хостов для сендбоксного скрипта. ВАЖНО: берётся
// только из извлекаемого URL — его регистрируемого домена (eTLD+1), — а НЕ из
// объявленных скриптом hosts или match-шаблонов. Иначе недоверенный скрипт мог
// бы сам расширить себе allowlist (напр. hosts:["accounts.google.com"]) и так
// прочитать/переслать cookies чужого домена, обойдя песочницу. В результате
// cookies доступны скрипту лишь в пределах регистрируемого домена того сайта,
// URL которого обрабатывается.
func (e *Extractor) allowedHosts(rawURL string) []string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return nil
	}
	out := []string{host}
	if reg, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil && reg != "" && reg != host {
		out = append(out, reg)
	}
	return out
}

func (e *Extractor) match(rawURL string) []string {
	for _, re := range e.patterns {
		if m := re.FindStringSubmatch(rawURL); m != nil {
			return m
		}
	}
	return nil
}

func (e *Extractor) Suitable(rawURL string) bool {
	if e.match(rawURL) != nil {
		return true
	}
	return e.fallback != nil && e.fallback.Suitable(rawURL)
}

// Extract исполняет скрипт в свежем движке goja.
func (e *Extractor) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	m := e.match(rawURL)
	if m == nil {
		if e.fallback != nil {
			return e.fallback.Extract(ctx, env, rawURL)
		}
		return nil, ex.Errorf(e.name, "", ex.ErrUnsupportedURL, "%s", rawURL)
	}
	info, err := e.run(ctx, env, rawURL, m)
	if err == nil || e.fallback == nil || isCategory(err) || ctx.Err() != nil {
		return info, err
	}
	env.Warn("script extractor failed, using built-in", "extractor", e.name, "script", e.file, "err", err)
	return e.fallback.Extract(ctx, env, rawURL)
}

func isCategory(err error) bool {
	for _, k := range []error{ex.ErrUnsupportedURL, ex.ErrUnavailable, ex.ErrLoginRequired, ex.ErrGeoRestricted, ex.ErrNoFormats} {
		if errors.Is(err, k) {
			return true
		}
	}
	return false
}

func (e *Extractor) run(ctx context.Context, env *ex.Env, rawURL string, m []string) (*media.Info, error) {
	ctx, cancel := context.WithTimeout(ctx, ExtractTimeout)
	defer cancel()

	vm := goja.New()
	vm.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))
	h := &host{ctx: ctx, env: env, vm: vm, ie: e.name, sandboxed: e.sandboxed}
	if e.sandboxed {
		h.allowed = e.allowedHosts(rawURL)
	}
	h.install()
	defs := &collector{}
	vm.Set("extractor", defs.add(vm))

	stop := context.AfterFunc(ctx, func() { vm.Interrupt(ctx.Err()) })
	defer stop()

	if _, err := vm.RunProgram(e.program); err != nil {
		return nil, h.wrapErr(err)
	}
	def := defs.byName[strings.ToLower(e.name)]
	if def == nil || def.extract == nil {
		return nil, fmt.Errorf("script %s: extractor %q not registered", e.file, e.name)
	}
	groups := make([]any, len(m))
	for i, s := range m {
		groups[i] = s
	}
	v, err := def.extract(goja.Undefined(), vm.ToValue(rawURL), vm.ToValue(groups))
	if err != nil {
		return nil, h.wrapErr(err)
	}
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil, ex.Errorf(e.name, "", ex.ErrNoFormats, "script returned nothing")
	}
	// Через JSON: поля результата совпадают с тегами media.Info.
	b, err := json.Marshal(v.Export())
	if err != nil {
		return nil, fmt.Errorf("script %s: result: %w", e.file, err)
	}
	info := &media.Info{}
	if err := json.Unmarshal(b, info); err != nil {
		return nil, fmt.Errorf("script %s: result: %w", e.file, err)
	}
	if info.WebpageURL == "" {
		info.WebpageURL = rawURL
	}
	return info, nil
}

// --- регистрация ---

type definition struct {
	name, description string
	priority          int
	patterns          []*regexp.Regexp
	hosts             []string
	extract           goja.Callable
}

type collector struct {
	mu     sync.Mutex
	order  []*definition
	byName map[string]*definition
	err    error
}

func (c *collector) add(vm *goja.Runtime) func(goja.Value) error {
	return func(v goja.Value) error {
		d, err := parseDefinition(vm, v)
		if err != nil {
			return err
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.byName == nil {
			c.byName = map[string]*definition{}
		}
		k := strings.ToLower(d.name)
		if _, dup := c.byName[k]; dup {
			return fmt.Errorf("extractor %q registered twice", d.name)
		}
		c.byName[k] = d
		c.order = append(c.order, d)
		return nil
	}
}

func parseDefinition(vm *goja.Runtime, v goja.Value) (*definition, error) {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil, errors.New("extractor(): definition object required")
	}
	o := v.ToObject(vm)
	d := &definition{name: str(o.Get("name")), description: str(o.Get("description"))}
	if d.name == "" {
		return nil, errors.New("extractor(): name required")
	}
	if p := o.Get("priority"); p != nil && !goja.IsUndefined(p) {
		d.priority = int(p.ToInteger())
	}
	fn, ok := goja.AssertFunction(o.Get("extract"))
	if !ok {
		return nil, fmt.Errorf("extractor %q: extract must be a function", d.name)
	}
	d.extract = fn
	mv := o.Get("match")
	if mv == nil || goja.IsUndefined(mv) {
		return nil, fmt.Errorf("extractor %q: match required", d.name)
	}
	var pats []goja.Value
	if arr, ok := mv.Export().([]any); ok {
		mo := mv.ToObject(vm)
		for i := range arr {
			pats = append(pats, mo.Get(fmt.Sprint(i)))
		}
	} else {
		pats = []goja.Value{mv}
	}
	for _, pv := range pats {
		src := pv.String()
		if po, ok := pv.(*goja.Object); ok && po.ClassName() == "RegExp" {
			src = po.Get("source").String()
		}
		re, err := regexp.Compile(src)
		if err != nil {
			return nil, fmt.Errorf("extractor %q: match %q: %w (RE2 syntax required)", d.name, src, err)
		}
		d.patterns = append(d.patterns, re)
	}
	if len(d.patterns) == 0 {
		return nil, fmt.Errorf("extractor %q: match is empty", d.name)
	}
	// hosts — необязательная подсказка о доменах. В песочнице НЕ расширяет
	// allowlist (его задаёт только домен извлекаемого URL — см. allowedHosts),
	// чтобы недоверенный скрипт не мог сам себе открыть cookies чужих доменов.
	if hv := o.Get("hosts"); hv != nil && !goja.IsUndefined(hv) && !goja.IsNull(hv) {
		if arr, ok := hv.Export().([]any); ok {
			for _, h := range arr {
				if s, ok := h.(string); ok && s != "" {
					d.hosts = append(d.hosts, s)
				}
			}
		}
	}
	return d, nil
}

func str(v goja.Value) string {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return ""
	}
	return v.String()
}

// LoadTimeout — предельное время исполнения верхнего уровня скрипта при загрузке.
var LoadTimeout = 5 * time.Second

// Compile компилирует скрипт и возвращает определённые в нём экстракторы.
// Верхний уровень скрипта исполняется один раз без доступа к сети — только
// для сбора определений; extract вызывается позже, в свежем движке.
func Compile(file, src string) ([]*Extractor, error) {
	prog, err := goja.Compile(file, src, true)
	if err != nil {
		return nil, fmt.Errorf("script %s: %w", file, err)
	}
	vm := goja.New()
	h := &host{ctx: context.Background(), env: &ex.Env{}, vm: vm, offline: true}
	h.install()
	defs := &collector{}
	vm.Set("extractor", defs.add(vm))
	timer := time.AfterFunc(LoadTimeout, func() { vm.Interrupt("load timeout") })
	_, err = vm.RunProgram(prog)
	timer.Stop()
	if err != nil {
		return nil, fmt.Errorf("script %s: %w", file, unwrapJS(err))
	}
	if len(defs.order) == 0 {
		return nil, fmt.Errorf("script %s: no extractor() calls", file)
	}
	out := make([]*Extractor, 0, len(defs.order))
	for _, d := range defs.order {
		out = append(out, &Extractor{
			name: d.name, description: d.description, priority: d.priority,
			patterns: d.patterns, hosts: d.hosts, file: file, program: prog,
		})
	}
	return out, nil
}

// unwrapJS извлекает Go-ошибку из исключения goja (если она там есть).
func unwrapJS(err error) error {
	var exc *goja.Exception
	if errors.As(err, &exc) {
		if inner := exc.Unwrap(); inner != nil {
			return inner
		}
		if e, ok := exc.Value().Export().(error); ok {
			return e
		}
	}
	var ie *goja.InterruptedError
	if errors.As(err, &ie) {
		if e, ok := ie.Value().(error); ok {
			return e
		}
	}
	return err
}
