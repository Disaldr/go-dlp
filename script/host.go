package script

// API хоста, доступное скриптам (HostAPI = 1):
//
//	http.get(url, opts?) / http.post(url, opts?) / http.request({url, method, ...})
//	    opts: headers{}, query{}, json, body (строка), form{}, impersonate,
//	          noCookies, allowErrors (не бросать исключение на статус >= 400)
//	    → {status, url, headers{имя в нижнем регистре: значение}, text, json()}
//	hls(url, {id, headers}?)  → formats[] (master- или media-плейлист)
//	dash(url, {id}?)          → formats[]
//	urlResult(url, ieKey?)    → {_type:"url", url, ie_key} — передать URL другому экстрактору
//	jsonAfter(text, ...markers) → значение JSON сразу после первого найденного маркера или null
//	urlJoin(base, ref), unescapeHTML(s), parseDuration("PT1M2S" | "1:02") → секунды
//	cookie(url, name), arg(key, default) — --extractor-args <name>:key=value
//	base64.encode/decode/decodeURL, hash.md5/sha1/sha256 (hex)
//	log.debug(msg, ...kv), log.warn(msg, ...kv)
//	fail.unsupported/unavailable/login/geo/noFormats(msg) — бросить ошибку-категорию
//
// Файловой системы и запуска процессов нет. Скрипты из сетевых обновлений
// исполняются в песочнице (см. host.sandboxed): cookie(url,name) доступен
// только для доменов экстрактора, и на прочие хосты cookies не подмешиваются
// (ни из jar, ни из заголовка Cookie) — скрипт не может прочитать и переслать
// cookies чужого домена. Разрешён только регистрируемый домен (eTLD+1)
// извлекаемого URL; объявленные скриптом hosts/match его НЕ расширяют (иначе
// недоверенный скрипт открыл бы себе доступ к cookies любого домена сам).

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"strconv"
	"strings"

	"github.com/dop251/goja"

	"github.com/Disaldr/go-dlp/dash"
	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/hls"
	"github.com/Disaldr/go-dlp/media"
)

type host struct {
	ctx     context.Context
	env     *ex.Env
	vm      *goja.Runtime
	ie      string
	offline bool // загрузка скрипта: сеть запрещена
	parse   goja.Callable

	// sandboxed — скрипт из недоверенного источника (сетевое обновление):
	// cookie() доступен только для доменов из allowed, а на прочие хосты не
	// подмешиваются cookies (ни из jar, ни из заголовка Cookie). Это не даёт
	// скрипту прочитать и отправить cookies чужих доменов.
	sandboxed bool
	allowed   []string // суффиксы разрешённых хостов (напр. "example.com")
}

// allowHost сообщает, разрешён ли хост скрипту (точное совпадение или
// поддомен разрешённого суффикса). Для несендбоксных — всегда да.
func (h *host) allowHost(host string) bool {
	if !h.sandboxed {
		return true
	}
	host = strings.ToLower(host)
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	for _, s := range h.allowed {
		if host == s || strings.HasSuffix(host, "."+s) {
			return true
		}
	}
	return false
}

func hostOf(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil {
		return u.Hostname()
	}
	return ""
}

// scopeCookies для сендбоксного скрипта на запросе к неразрешённому хосту
// убирает cookies: ставит NoCookies и вычищает заголовок Cookie, чтобы
// значения cookie своего домена нельзя было переслать на чужой хост.
func (h *host) scopeCookies(req *ex.Request) {
	if !h.sandboxed || h.allowHost(hostOf(req.URL)) {
		return
	}
	req.NoCookies = true
	for k := range req.Headers {
		if strings.EqualFold(k, "cookie") {
			delete(req.Headers, k)
		}
	}
	h.env.Debug("sandboxed script: cookies withheld from third-party host", "extractor", h.ie, "host", hostOf(req.URL))
}

var errOffline = errors.New("network is not available while loading a script (call it inside extract)")

func (h *host) install() {
	vm := h.vm
	h.parse, _ = goja.AssertFunction(vm.Get("JSON").ToObject(vm).Get("parse"))

	httpObj := vm.NewObject()
	httpObj.Set("request", func(opts goja.Value) (goja.Value, error) {
		o := exportMap(opts)
		u, _ := o["url"].(string)
		return h.request(u, o)
	})
	httpObj.Set("get", func(u string, opts goja.Value) (goja.Value, error) {
		o := exportMap(opts)
		if _, ok := o["method"]; !ok {
			o["method"] = "GET"
		}
		return h.request(u, o)
	})
	httpObj.Set("post", func(u string, opts goja.Value) (goja.Value, error) {
		o := exportMap(opts)
		o["method"] = "POST"
		return h.request(u, o)
	})
	vm.Set("http", httpObj)

	vm.Set("hls", func(u string, opts goja.Value) (goja.Value, error) { return h.hls(u, exportMap(opts)) })
	vm.Set("dash", func(u string, opts goja.Value) (goja.Value, error) { return h.dash(u, exportMap(opts)) })
	vm.Set("urlResult", func(u, ie string) map[string]any {
		r := map[string]any{"_type": "url", "url": u}
		if ie != "" {
			r["ie_key"] = ie
		}
		return r
	})
	vm.Set("jsonAfter", func(call goja.FunctionCall) goja.Value {
		text := call.Argument(0).String()
		var markers []string
		for _, a := range call.Arguments[1:] {
			markers = append(markers, a.String())
		}
		v, ok := ex.JSONAfter(text, markers...)
		if !ok || v == nil {
			return goja.Null()
		}
		return h.toJS(v)
	})
	vm.Set("urlJoin", ex.URLJoin)
	vm.Set("unescapeHTML", html.UnescapeString)
	vm.Set("parseDuration", parseDuration)
	vm.Set("cookie", func(u, name string) string {
		if !h.allowHost(hostOf(u)) {
			h.env.Debug("sandboxed script: cookie read denied for third-party host", "extractor", h.ie, "host", hostOf(u))
			return ""
		}
		return h.env.Cookie(u, name)
	})
	vm.Set("arg", func(k, def string) string { return h.env.Arg(h.ie, k, def) })

	b64 := vm.NewObject()
	b64.Set("encode", func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) })
	b64.Set("decode", func(s string) (string, error) {
		b, err := base64.StdEncoding.DecodeString(s)
		return string(b), err
	})
	b64.Set("decodeURL", func(s string) (string, error) {
		b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
		return string(b), err
	})
	vm.Set("base64", b64)
	hash := vm.NewObject()
	hash.Set("md5", func(s string) string { x := md5.Sum([]byte(s)); return hex.EncodeToString(x[:]) })
	hash.Set("sha1", func(s string) string { x := sha1.Sum([]byte(s)); return hex.EncodeToString(x[:]) })
	hash.Set("sha256", func(s string) string { x := sha256.Sum256([]byte(s)); return hex.EncodeToString(x[:]) })
	vm.Set("hash", hash)

	logObj := vm.NewObject()
	logObj.Set("debug", func(call goja.FunctionCall) goja.Value {
		h.env.Debug(h.logArgs(call))
		return goja.Undefined()
	})
	logObj.Set("warn", func(call goja.FunctionCall) goja.Value {
		h.env.Warn(h.logArgs(call))
		return goja.Undefined()
	})
	vm.Set("log", logObj)

	fail := vm.NewObject()
	for name, kind := range map[string]error{
		"unsupported": ex.ErrUnsupportedURL, "unavailable": ex.ErrUnavailable,
		"login": ex.ErrLoginRequired, "geo": ex.ErrGeoRestricted, "noFormats": ex.ErrNoFormats,
	} {
		fail.Set(name, func(msg, id string) error { return ex.Errorf(h.ie, id, kind, "%s", msg) })
	}
	vm.Set("fail", fail)
}

func (h *host) logArgs(call goja.FunctionCall) string {
	parts := make([]string, 0, len(call.Arguments))
	parts = append(parts, h.ie+":")
	for _, a := range call.Arguments {
		parts = append(parts, a.String())
	}
	return strings.Join(parts, " ")
}

func exportMap(v goja.Value) map[string]any {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return map[string]any{}
	}
	if m, ok := v.Export().(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func strMap(v any) map[string]string {
	m, _ := v.(map[string]any)
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, x := range m {
		out[k] = fmt.Sprint(x)
	}
	return out
}

// toJS превращает Go-значение в «родной» объект JS (через JSON.parse), чтобы
// скрипту были доступны обычные методы массивов и объектов.
func (h *host) toJS(v any) goja.Value {
	b, err := json.Marshal(v)
	if err != nil {
		return goja.Null()
	}
	r, err := h.parse(goja.Undefined(), h.vm.ToValue(string(b)))
	if err != nil {
		return goja.Null()
	}
	return r
}

func (h *host) request(u string, o map[string]any) (goja.Value, error) {
	if h.offline {
		return nil, errOffline
	}
	if u == "" {
		return nil, errors.New("http: url required")
	}
	req := ex.Request{URL: u, Headers: strMap(o["headers"])}
	req.Method, _ = o["method"].(string)
	req.Impersonate, _ = o["impersonate"].(string)
	req.NoCookies, _ = o["noCookies"].(bool)
	if q := strMap(o["query"]); q != nil {
		req.Query = url.Values{}
		for k, v := range q {
			req.Query.Set(k, v)
		}
	}
	if j, ok := o["json"]; ok {
		req.JSON = j
	}
	if b, ok := o["body"].(string); ok {
		req.Body = strings.NewReader(b)
	}
	if f := strMap(o["form"]); f != nil {
		vals := url.Values{}
		for k, v := range f {
			vals.Set(k, v)
		}
		req.Body = strings.NewReader(vals.Encode())
		if req.Headers == nil {
			req.Headers = map[string]string{}
		}
		if _, ok := req.Headers["Content-Type"]; !ok {
			req.Headers["Content-Type"] = "application/x-www-form-urlencoded"
		}
	}
	allowErrors, _ := o["allowErrors"].(bool)
	h.scopeCookies(&req)

	resp, err := h.env.Fetch(h.ctx, req)
	var status int
	var finalURL string
	var headers map[string]string
	var body []byte
	switch {
	case err == nil:
		status, finalURL, body = resp.StatusCode, resp.URL, resp.Body
		headers = make(map[string]string, len(resp.Header))
		for k, v := range resp.Header {
			if len(v) > 0 {
				headers[strings.ToLower(k)] = v[0]
			}
		}
	case allowErrors:
		var he *ex.HTTPError
		if !errors.As(err, &he) {
			return nil, err
		}
		status, finalURL, body = he.StatusCode, he.URL, he.Body
	default:
		return nil, err
	}

	r := h.vm.NewObject()
	r.Set("status", status)
	r.Set("url", finalURL)
	r.Set("headers", headers)
	text := string(body)
	r.Set("text", text)
	r.Set("json", func() (goja.Value, error) {
		return h.parse(goja.Undefined(), h.vm.ToValue(text))
	})
	return r, nil
}

func (h *host) hls(u string, o map[string]any) (goja.Value, error) {
	if h.offline {
		return nil, errOffline
	}
	id, _ := o["id"].(string)
	if id == "" {
		id = "hls"
	}
	headers := strMap(o["headers"])
	hreq := ex.Request{URL: u, Headers: headers}
	h.scopeCookies(&hreq)
	r, err := h.env.Fetch(h.ctx, hreq)
	if err != nil {
		return nil, err
	}
	var fs []*media.Format
	if hls.IsMaster(r.Text()) {
		fs = hls.ParseMaster(r.Text(), r.URL, id)
	} else {
		fs = []*media.Format{{FormatID: id, URL: r.URL, Protocol: media.ProtoHLS, Ext: "mp4"}}
	}
	for _, f := range fs {
		if headers != nil && f.HTTPHeaders == nil {
			f.HTTPHeaders = headers
		}
	}
	return h.toJS(fs), nil
}

func (h *host) dash(u string, o map[string]any) (goja.Value, error) {
	if h.offline {
		return nil, errOffline
	}
	id, _ := o["id"].(string)
	if id == "" {
		id = "dash"
	}
	dreq := ex.Request{URL: u, Headers: strMap(o["headers"])}
	h.scopeCookies(&dreq)
	r, err := h.env.Fetch(h.ctx, dreq)
	if err != nil {
		return nil, err
	}
	fs, err := dash.Parse(r.Body, r.URL, id)
	if err != nil {
		return nil, err
	}
	return h.toJS(fs), nil
}

// wrapErr превращает исключение скрипта в ошибку Go. Ошибки хоста
// (категории, HTTP) возвращаются как есть; ошибки самого скрипта
// снабжаются именем экстрактора и стеком JS.
func (h *host) wrapErr(err error) error {
	if ctxErr := h.ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	inner := unwrapJS(err)
	if inner != err {
		return inner
	}
	return fmt.Errorf("[%s] script error: %w", h.ie, err)
}

// parseDuration: ISO 8601 (PT1H2M3S) или [[чч:]мм:]сс; 0 при ошибке.
func parseDuration(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if strings.HasPrefix(s, "P") {
		return dash.ParseDuration(s)
	}
	var total float64
	for _, p := range strings.Split(s, ":") {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return 0
		}
		total = total*60 + v
	}
	return total
}
