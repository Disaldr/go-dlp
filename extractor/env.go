package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/Disaldr/go-dlp/impersonate"
)

// DefaultUserAgent — UA браузера, которым представляется библиотека.
const DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36"

// MaxBodySize ограничивает размер ответов, читаемых в память.
const MaxBodySize = 64 << 20

// Env — окружение, передаваемое экстракторам: HTTP, настройки, логгер.
type Env struct {
	HTTP          *http.Client
	SocketTimeout time.Duration
	UserAgent     string
	Logger        *slog.Logger
	// Registry — реестр, в котором зарегистрирован экстрактор (для поиска встраиваний).
	Registry *Registry
	// Args — аргументы экстракторов: ie -> ключ -> значения (как --extractor-args).
	Args map[string]map[string][]string
	// NoPlaylist — для URL «видео в плейлисте» качать только видео.
	NoPlaylist bool
	// MaxEntries — подсказка: сколько элементов плейлиста реально нужно
	// (0 — все). Экстракторы с постраничной загрузкой могут остановиться раньше.
	MaxEntries int
	// Impersonate — цель имитации браузера для ВСЕХ запросов ("" — выкл.).
	Impersonate string
	// Impersonator выдаёт клиентов с TLS-отпечатком браузера; nil — создаётся
	// лениво поверх cookie-jar клиента HTTP.
	Impersonator *impersonate.Pool
	// Creds — учётные данные для входа (явные и/или из .netrc). Может быть nil.
	Creds *CredStore
}

// ClientFor возвращает HTTP-клиент для цели имитации ("" — обычный клиент).
func (e *Env) ClientFor(target string) (*http.Client, error) {
	if target == "" {
		if e.HTTP != nil {
			return e.HTTP, nil
		}
		return http.DefaultClient, nil
	}
	if e.Impersonator == nil {
		var jar http.CookieJar
		if e.HTTP != nil {
			jar = e.HTTP.Jar
		}
		e.Impersonator = &impersonate.Pool{Jar: jar}
	}
	client, err := e.Impersonator.Client(target)
	if err != nil {
		return nil, err
	}
	return WithSocketTimeout(client, e.SocketTimeout), nil
}

// ImpersonateTarget — фактическая цель для запроса: явная или глобальная.
func (e *Env) ImpersonateTarget(requested string) string {
	if requested != "" {
		return requested
	}
	return e.Impersonate
}

// NewEnv создаёт окружение с разумными значениями по умолчанию.
func NewEnv() *Env {
	return &Env{
		HTTP:      DefaultHTTPClient(nil),
		UserAgent: DefaultUserAgent,
		Logger:    slog.New(slog.DiscardHandler),
	}
}

// DefaultHTTPClient создаёт HTTP-клиент для экстракции и загрузки
// с cookie-jar (сайты вроде YouTube проверяют куки сессии).
// proxy == nil — используется прокси из окружения.
func DefaultHTTPClient(proxy *url.URL) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 30 * time.Second
	if proxy != nil {
		tr.Proxy = http.ProxyURL(proxy)
	}
	jar, _ := cookiejar.New(nil)
	return &http.Client{Transport: tr, Jar: jar}
}

// Arg возвращает первое значение аргумента экстрактора или def.
func (e *Env) Arg(ie, k, def string) string {
	if v := e.ArgList(ie, k); len(v) > 0 {
		return v[0]
	}
	return def
}

// ArgList возвращает все значения аргумента экстрактора.
func (e *Env) ArgList(ie, k string) []string {
	if e.Args == nil {
		return nil
	}
	return e.Args[strings.ToLower(ie)][k]
}

func (e *Env) log() *slog.Logger {
	if e.Logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return e.Logger
}

// Debug пишет отладочное сообщение.
func (e *Env) Debug(msg string, args ...any) { e.log().Debug(msg, args...) }

// Warn пишет предупреждение.
func (e *Env) Warn(msg string, args ...any) { e.log().Warn(msg, args...) }

// Request — описание HTTP-запроса.
type Request struct {
	Method  string
	URL     string
	Query   url.Values
	Headers map[string]string
	// Body — тело запроса; JSON — значение для сериализации в JSON (приоритетнее Body).
	Body io.Reader
	JSON any
	// Impersonate — имитировать браузер: "any"/"chrome"/"firefox-120"/...
	// ("" — по глобальной настройке Env.Impersonate).
	Impersonate string
	// NoCookies — не отправлять и не сохранять cookies (некоторые CDN
	// выдают cookies, ломающие последующие прямые загрузки).
	NoCookies bool
}

// Response — прочитанный HTTP-ответ.
type Response struct {
	StatusCode int
	Header     http.Header
	URL        string // финальный URL после редиректов
	Body       []byte
}

// Text возвращает тело как строку.
func (r *Response) Text() string { return string(r.Body) }

// HTTPError — ответ с неуспешным статусом.
type HTTPError struct {
	StatusCode int
	URL        string
	Body       []byte
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d %s: %s", e.StatusCode, http.StatusText(e.StatusCode), e.URL)
}

// Fetch выполняет запрос; статусы >= 400 возвращаются как *HTTPError.
func (e *Env) Fetch(ctx context.Context, req Request) (*Response, error) {
	resp, err := e.Open(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodySize))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, &HTTPError{StatusCode: resp.StatusCode, URL: req.URL, Body: body}
	}
	return &Response{StatusCode: resp.StatusCode, Header: resp.Header, URL: resp.Request.URL.String(), Body: body}, nil
}

// Open выполняет запрос и возвращает необработанный ответ (тело закрывает вызывающий).
func (e *Env) Open(ctx context.Context, req Request) (*http.Response, error) {
	method := req.Method
	body := req.Body
	if req.JSON != nil {
		b, err := json.Marshal(req.JSON)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
		if method == "" {
			method = http.MethodPost
		}
	}
	if method == "" {
		method = http.MethodGet
	}
	u := req.URL
	if len(req.Query) > 0 {
		pu, err := url.Parse(u)
		if err != nil {
			return nil, err
		}
		q := pu.Query()
		maps.Copy(q, req.Query)
		pu.RawQuery = q.Encode()
		u = pu.String()
	}
	hr, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	target := e.ImpersonateTarget(req.Impersonate)
	if target == "" {
		// при имитации UA и прочие заголовки подставляет транспорт браузера
		ua := e.UserAgent
		if ua == "" {
			ua = DefaultUserAgent
		}
		hr.Header.Set("User-Agent", ua)
		hr.Header.Set("Accept-Language", "en-US,en;q=0.9")
	}
	if req.JSON != nil {
		hr.Header.Set("Content-Type", "application/json")
	}
	for k, v := range req.Headers {
		hr.Header.Set(k, v)
	}
	client, err := e.ClientFor(target)
	if err != nil {
		return nil, err
	}
	if req.NoCookies && client.Jar != nil {
		c := *client
		c.Jar = nil
		client = &c
	}
	e.Debug("http request", "method", method, "url", u, "impersonate", target)
	return client.Do(hr)
}

// GetText загружает страницу как текст.
func (e *Env) GetText(ctx context.Context, u string, headers map[string]string) (string, error) {
	r, err := e.Fetch(ctx, Request{URL: u, Headers: headers})
	if err != nil {
		return "", err
	}
	return r.Text(), nil
}

// GetJSON загружает JSON в out (если out == nil — возвращает any).
func (e *Env) GetJSON(ctx context.Context, u string, headers map[string]string, out any) error {
	r, err := e.Fetch(ctx, Request{URL: u, Headers: headers})
	if err != nil {
		return err
	}
	return json.Unmarshal(r.Body, out)
}

// PostJSON отправляет JSON и разбирает JSON-ответ в out.
func (e *Env) PostJSON(ctx context.Context, u string, headers map[string]string, in, out any) error {
	r, err := e.Fetch(ctx, Request{Method: http.MethodPost, URL: u, Headers: headers, JSON: in})
	if err != nil {
		return err
	}
	return json.Unmarshal(r.Body, out)
}

// FindEmbedded ищет экстрактор (кроме перечисленных) для URL встраивания.
func (e *Env) FindEmbedded(u string, skip ...string) (Extractor, bool) {
	if e.Registry == nil {
		return nil, false
	}
	ex, err := e.Registry.FindExcept(u, skip...)
	if err != nil || PriorityOf(ex) <= PriorityFallback {
		return nil, false
	}
	return ex, true
}
