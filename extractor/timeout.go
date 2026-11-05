package extractor

import (
	"context"
	"io"
	"net/http"
	"time"
)

// WithSocketTimeout ограничивает ожидание заголовков и каждого чтения тела,
// сохраняя возможность скачивать файл дольше timeout при поступлении данных.
func WithSocketTimeout(client *http.Client, timeout time.Duration) *http.Client {
	if timeout <= 0 {
		return client
	}
	cp := *client
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	cp.Transport = &timeoutTransport{base: transport, timeout: timeout}
	return &cp
}

type timeoutTransport struct {
	base    http.RoundTripper
	timeout time.Duration
}

func (t *timeoutTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(req.Context())
	timer := time.AfterFunc(t.timeout, cancel)
	resp, err := t.base.RoundTrip(req.Clone(ctx))
	timer.Stop()
	if err != nil {
		cancel()
		if req.Context().Err() == nil && ctx.Err() != nil {
			return nil, &socketTimeoutError{}
		}
		return nil, err
	}
	resp.Body = &timeoutBody{ReadCloser: resp.Body, timeout: t.timeout, cancel: cancel, ctx: ctx, parent: req.Context()}
	return resp, nil
}

type timeoutBody struct {
	io.ReadCloser
	timeout time.Duration
	cancel  context.CancelFunc
	ctx     context.Context
	parent  context.Context
}

func (b *timeoutBody) Read(p []byte) (int, error) {
	timer := time.AfterFunc(b.timeout, b.cancel)
	n, err := b.ReadCloser.Read(p)
	timer.Stop()
	if err != nil && b.parent.Err() == nil && b.ctx.Err() != nil {
		return n, &socketTimeoutError{}
	}
	return n, err
}

func (b *timeoutBody) Close() error {
	b.cancel()
	return b.ReadCloser.Close()
}

type socketTimeoutError struct{}

func (*socketTimeoutError) Error() string   { return "socket timeout waiting for response data" }
func (*socketTimeoutError) Timeout() bool   { return true }
func (*socketTimeoutError) Temporary() bool { return true }
