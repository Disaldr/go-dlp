package generic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	ex "github.com/Disaldr/go-dlp/extractor"
)

// При анти-бот-ответе (403) generic повторяет запрос с имитацией браузера и
// проставляет её форматам (чтобы и загрузка шла с имитацией).
func TestGenericAntiBotRetry(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			// Первый заход (без имитации) — заслон.
			http.Error(w, "Attention Required! Cloudflare", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<!doctype html><html><head>
<meta property="og:video" content="https://cdn.example/stream.m3u8">
</head><body>cinema</body></html>`))
	}))
	defer srv.Close()

	env := &ex.Env{HTTP: srv.Client()}
	info, err := New().Extract(context.Background(), env, srv.URL+"/movie/42")
	if err != nil {
		t.Fatalf("Extract: %v (hits=%d)", err, atomic.LoadInt32(&hits))
	}
	if atomic.LoadInt32(&hits) < 2 {
		t.Fatalf("expected a retry, got %d requests", hits)
	}
	if len(info.Formats) == 0 {
		t.Fatal("no formats extracted after anti-bot retry")
	}
	for _, f := range info.Formats {
		if f.Impersonate != "any" {
			t.Errorf("format %s: Impersonate = %q, want any", f.FormatID, f.Impersonate)
		}
	}
}

// Без заслона имитация не включается (обычные сайты не трогаем).
func TestGenericNoImpersonateWhenOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<meta property="og:video" content="https://cdn.example/v.m3u8">`))
	}))
	defer srv.Close()
	env := &ex.Env{HTTP: srv.Client()}
	info, err := New().Extract(context.Background(), env, srv.URL+"/v/1")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range info.Formats {
		if f.Impersonate != "" {
			t.Errorf("format %s unexpectedly impersonated: %q", f.FormatID, f.Impersonate)
		}
	}
}
