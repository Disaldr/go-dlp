package impersonate

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTransportLocal(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Proto", r.Proto)
		w.Header().Set("X-UA", r.Header.Get("User-Agent"))
		w.Header().Set("X-Ch-Ua", r.Header.Get("sec-ch-ua"))
		w.Header().Set("X-Custom", r.Header.Get("X-Custom"))
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		io.WriteString(gz, "hello")
		gz.Close()
	})
	for _, h2 := range []bool{true, false} {
		srv := httptest.NewUnstartedServer(handler)
		srv.EnableHTTP2 = h2
		srv.StartTLS()
		rootCAs = srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs

		target, _ := Lookup("chrome")
		c := &http.Client{Transport: NewTransport(target, nil)}
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		req.Header.Set("X-Custom", "kept")
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("h2=%v: %v", h2, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		wantProto := "HTTP/1.1"
		if h2 {
			wantProto = "HTTP/2.0"
		}
		if got := resp.Header.Get("X-Proto"); got != wantProto {
			t.Errorf("h2=%v: server saw %s, want %s", h2, got, wantProto)
		}
		if string(body) != "hello" {
			t.Errorf("h2=%v: body %q (gzip not decoded?)", h2, body)
		}
		if resp.Header.Get("X-UA") != chromeUA("133", "windows") || resp.Header.Get("X-Ch-Ua") == "" {
			t.Errorf("h2=%v: browser headers missing: ua=%q", h2, resp.Header.Get("X-UA"))
		}
		if resp.Header.Get("X-Custom") != "kept" {
			t.Errorf("h2=%v: caller header overwritten", h2)
		}
		srv.Close()
	}
	rootCAs = nil
}
