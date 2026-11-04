package impersonate

import (
	"bufio"
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

// ProxyFunc выбирает прокси для запроса (как http.Transport.Proxy).
type ProxyFunc func(*http.Request) (*url.URL, error)

// Transport — http.RoundTripper с TLS-отпечатком браузера.
// HTTPS идёт через HTTP/2, если сервер его согласовал, иначе — HTTP/1.1
// (результат запоминается для хоста). Обычный HTTP проксируется стандартным
// транспортом. Ответы со сжатием gzip/deflate/br/zstd распаковываются.
type Transport struct {
	target *Target
	proxy  ProxyFunc
	dialer *net.Dialer

	h1    *http.Transport
	h2    *http2.Transport
	plain *http.Transport

	mu      sync.Mutex
	h1Only  map[string]bool
	h2Conns map[string][]*http2.ClientConn
}

var errNotH2 = errors.New("impersonate: server did not negotiate h2")

type ctxKey struct{}

// rootCAs переопределяет доверенные CA (только для тестов).
var rootCAs *x509.CertPool

// NewTransport создаёт транспорт для цели t. proxy == nil — прокси из окружения.
func NewTransport(t *Target, proxyFn ProxyFunc) *Transport {
	if proxyFn == nil {
		proxyFn = http.ProxyFromEnvironment
	}
	tr := &Transport{
		target:  t,
		proxy:   proxyFn,
		dialer:  &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second},
		h1Only:  map[string]bool{},
		h2Conns: map[string][]*http2.ClientConn{},
	}
	tr.h1 = &http.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return tr.dialTLS(ctx, addr, true)
		},
		ForceAttemptHTTP2:     false,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		DisableCompression:    true,
	}
	tr.h2 = newH2Transport(t.H2)
	tr.plain = &http.Transport{Proxy: proxyFn, DisableCompression: true, ResponseHeaderTimeout: 30 * time.Second}
	return tr
}

// Target возвращает имитируемый браузер.
func (t *Transport) Target() *Target { return t.target }

// RoundTrip реализует http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = t.prepare(req)
	var resp *http.Response
	var err error
	if req.URL.Scheme != "https" {
		resp, err = t.plain.RoundTrip(req)
	} else {
		resp, err = t.roundTripTLS(req)
	}
	if err != nil {
		return nil, err
	}
	return decompress(resp)
}

func (t *Transport) roundTripTLS(req *http.Request) (*http.Response, error) {
	p, err := t.proxy(req)
	if err != nil {
		return nil, err
	}
	key := req.URL.Host
	if p != nil {
		key += "|" + p.String()
	}
	req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, p))

	t.mu.Lock()
	h1 := t.h1Only[key]
	t.mu.Unlock()
	if !h1 {
		resp, err := t.h2RoundTrip(req, key)
		if !errors.Is(err, errNotH2) {
			return resp, err
		}
		t.mu.Lock()
		t.h1Only[key] = true
		t.mu.Unlock()
		if req.GetBody != nil {
			body, gerr := req.GetBody()
			if gerr != nil {
				return nil, gerr
			}
			req.Body = body
		}
	}
	return t.h1.RoundTrip(req)
}

// prepare добавляет браузерные заголовки, которых нет в запросе.
func (t *Transport) prepare(req *http.Request) *http.Request {
	r := req.Clone(req.Context())
	for _, h := range t.target.Headers {
		if r.Header.Get(h.Key) == "" {
			r.Header.Set(h.Key, h.Value)
		}
	}
	// для XHR/API-запросов навигационные заголовки только мешают
	if accept := r.Header.Get("Accept"); strings.Contains(accept, "json") {
		r.Header.Set("Sec-Fetch-Mode", "cors")
		r.Header.Set("Sec-Fetch-Dest", "empty")
		r.Header.Del("Sec-Fetch-User")
		r.Header.Del("Upgrade-Insecure-Requests")
	}
	return r
}

func (t *Transport) dialTLS(ctx context.Context, addr string, h1only bool) (net.Conn, error) {
	p, _ := ctx.Value(ctxKey{}).(*url.URL)
	raw, err := t.dialRaw(ctx, addr, p)
	if err != nil {
		return nil, err
	}
	host, _, _ := net.SplitHostPort(addr)
	uc := utls.UClient(raw, &utls.Config{ServerName: host, RootCAs: rootCAs}, utls.HelloCustom)
	spec, err := utls.UTLSIdToSpec(t.target.Hello)
	if err != nil {
		raw.Close()
		return nil, fmt.Errorf("impersonate: %s: %w", t.target.Name, err)
	}
	if h1only {
		forceHTTP1(&spec)
	}
	if err := uc.ApplyPreset(&spec); err != nil {
		raw.Close()
		return nil, fmt.Errorf("impersonate: apply %s: %w", t.target.Name, err)
	}
	if err := uc.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, fmt.Errorf("impersonate: TLS handshake with %s: %w", host, err)
	}
	if !h1only && uc.ConnectionState().NegotiatedProtocol != "h2" {
		uc.Close()
		return nil, errNotH2
	}
	return uc, nil
}

// forceHTTP1 оставляет в ALPN/ALPS только http/1.1.
func forceHTTP1(spec *utls.ClientHelloSpec) {
	for _, ext := range spec.Extensions {
		switch e := ext.(type) {
		case *utls.ALPNExtension:
			e.AlpnProtocols = []string{"http/1.1"}
		case *utls.ApplicationSettingsExtension:
			e.SupportedProtocols = []string{"http/1.1"}
		case *utls.ApplicationSettingsExtensionNew:
			e.SupportedProtocols = []string{"http/1.1"}
		}
	}
}

// dialRaw устанавливает TCP-соединение напрямую или через прокси.
func (t *Transport) dialRaw(ctx context.Context, addr string, p *url.URL) (net.Conn, error) {
	if p == nil {
		return t.dialer.DialContext(ctx, "tcp", addr)
	}
	switch p.Scheme {
	case "socks5", "socks5h":
		d, err := proxy.FromURL(p, t.dialer)
		if err != nil {
			return nil, err
		}
		if cd, ok := d.(proxy.ContextDialer); ok {
			return cd.DialContext(ctx, "tcp", addr)
		}
		return d.Dial("tcp", addr)
	case "http", "https":
		return t.dialConnect(ctx, addr, p)
	}
	return nil, fmt.Errorf("impersonate: unsupported proxy scheme %q", p.Scheme)
}

func (t *Transport) dialConnect(ctx context.Context, addr string, p *url.URL) (net.Conn, error) {
	paddr := p.Host
	if p.Port() == "" {
		if p.Scheme == "https" {
			paddr = net.JoinHostPort(p.Hostname(), "443")
		} else {
			paddr = net.JoinHostPort(p.Hostname(), "80")
		}
	}
	conn, err := t.dialer.DialContext(ctx, "tcp", paddr)
	if err != nil {
		return nil, err
	}
	if p.Scheme == "https" {
		tc := tls.Client(conn, &tls.Config{ServerName: p.Hostname()})
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, err
		}
		conn = tc
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: addr}, Host: addr, Header: http.Header{}}
	if u := p.User; u != nil {
		pw, _ := u.Password()
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(u.Username()+":"+pw)))
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
		defer conn.SetDeadline(time.Time{})
	}
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		conn.Close()
		return nil, err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("impersonate: proxy CONNECT %s: %s", addr, resp.Status)
	}
	return conn, nil
}

// h2RoundTrip выполняет запрос по HTTP/2 через собственный пул соединений
// (ClientConn создаются через h2.NewClientConn, чтобы применялась настройка окон).
func (t *Transport) h2RoundTrip(req *http.Request, key string) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		cc, err := t.h2Conn(req.Context(), key, hostPort(req.URL))
		if err != nil {
			return nil, err
		}
		resp, err := cc.RoundTrip(req)
		if err == nil || attempt > 0 || cc.CanTakeNewRequest() {
			return resp, err
		}
		// соединение умерло (GOAWAY/закрыто) — один повтор на новом
		if req.GetBody != nil {
			body, gerr := req.GetBody()
			if gerr != nil {
				return nil, err
			}
			req.Body = body
		} else if req.Body != nil && req.Body != http.NoBody {
			return nil, err
		}
	}
}

func (t *Transport) h2Conn(ctx context.Context, key, addr string) (*http2.ClientConn, error) {
	t.mu.Lock()
	live := t.h2Conns[key][:0]
	var found *http2.ClientConn
	for _, cc := range t.h2Conns[key] {
		if cc.State().Closed {
			continue
		}
		live = append(live, cc)
		if found == nil && cc.CanTakeNewRequest() {
			found = cc
		}
	}
	t.h2Conns[key] = live
	t.mu.Unlock()
	if found != nil {
		return found, nil
	}
	conn, err := t.dialTLS(ctx, addr, false)
	if err != nil {
		return nil, err
	}
	if t.target.H2 != nil {
		conn = newFPConn(conn, t.target.H2)
	}
	cc, err := t.h2.NewClientConn(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	t.mu.Lock()
	t.h2Conns[key] = append(t.h2Conns[key], cc)
	t.mu.Unlock()
	return cc, nil
}

func hostPort(u *url.URL) string {
	if u.Port() != "" {
		return u.Host
	}
	return net.JoinHostPort(u.Hostname(), "443")
}

// CloseIdleConnections закрывает простаивающие соединения.
func (t *Transport) CloseIdleConnections() {
	t.h1.CloseIdleConnections()
	t.plain.CloseIdleConnections()
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, conns := range t.h2Conns {
		for _, cc := range conns {
			if cc.State().StreamsActive == 0 {
				cc.Close()
			}
		}
		delete(t.h2Conns, k)
	}
}

func decompress(resp *http.Response) (*http.Response, error) {
	enc := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	var r io.Reader
	var closer func()
	switch enc {
	case "", "identity":
		return resp, nil
	case "gzip", "x-gzip":
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			resp.Body.Close()
			return nil, err
		}
		r = gz
	case "deflate":
		r = flate.NewReader(resp.Body)
	case "br":
		r = brotli.NewReader(resp.Body)
	case "zstd":
		zr, err := zstd.NewReader(resp.Body)
		if err != nil {
			resp.Body.Close()
			return nil, err
		}
		r, closer = zr, zr.Close
	default:
		return resp, nil
	}
	resp.Body = &decodedBody{r: r, c: resp.Body, extra: closer}
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("Content-Length")
	resp.ContentLength = -1
	resp.Uncompressed = true
	return resp, nil
}

type decodedBody struct {
	r     io.Reader
	c     io.Closer
	extra func()
}

func (d *decodedBody) Read(p []byte) (int, error) { return d.r.Read(p) }
func (d *decodedBody) Close() error {
	if d.extra != nil {
		d.extra()
	}
	return d.c.Close()
}

// Pool хранит по клиенту на цель; все клиенты делят cookie-jar и прокси.
type Pool struct {
	Jar   http.CookieJar
	Proxy ProxyFunc

	mu      sync.Mutex
	clients map[string]*http.Client
}

// Client возвращает http.Client, имитирующий браузер spec (см. Lookup).
func (p *Pool) Client(spec string) (*http.Client, error) {
	t, err := Lookup(spec)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.clients == nil {
		p.clients = map[string]*http.Client{}
	}
	if c, ok := p.clients[t.Name]; ok {
		return c, nil
	}
	c := &http.Client{Transport: NewTransport(t, p.Proxy), Jar: p.Jar}
	p.clients[t.Name] = c
	return c, nil
}

// newH2Transport настраивает x/net/http2 так, чтобы его состояние совпадало
// с профилем: окна flow control (они определяют INITIAL_WINDOW_SIZE и
// начальный WINDOW_UPDATE) и размеры HPACK-таблиц. Порядок и состав SETTINGS
// затем переписывает fpConn.
func newH2Transport(p *H2Profile) *http2.Transport {
	t1 := &http.Transport{}
	if p != nil {
		t1.HTTP2 = &http.HTTP2Config{
			MaxReceiveBufferPerStream:     int(p.initialWindow()),
			MaxReceiveBufferPerConnection: int(p.ConnWindowIncrement), // x/net шлёт WINDOW_UPDATE ровно на это значение
		}
	}
	h2, err := http2.ConfigureTransports(t1)
	if err != nil {
		h2 = &http2.Transport{}
	}
	h2.DisableCompression = true
	h2.ReadIdleTimeout = 60 * time.Second
	h2.MaxHeaderListSize = 262144
	h2.MaxDecoderHeaderTableSize = 4096
	if p != nil {
		if v, ok := p.setting(http2.SettingHeaderTableSize); ok {
			h2.MaxDecoderHeaderTableSize = v
		}
		if v, ok := p.setting(http2.SettingMaxHeaderListSize); ok {
			h2.MaxHeaderListSize = v
		}
	}
	return h2
}
