package downloader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Disaldr/go-dlp/media"
)

// Master-плейлист, ссылающийся сам на себя, не должен приводить к бесконечной рекурсии.
func TestHLSMasterLoop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\n/master.m3u8\n"))
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := NewHLS(Options{Retries: 1}).Download(ctx, &media.Format{URL: srv.URL + "/master.m3u8"}, nil, filepath.Join(t.TempDir(), "x.ts"))
	if err == nil || ctx.Err() != nil {
		t.Fatalf("expected parse error without looping, got %v (ctx %v)", err, ctx.Err())
	}
}

// Ответ сверх лимита отвергается, а не читается целиком в память.
func TestHLSKeySizeLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("k", maxKeySize+10)))
	}))
	defer srv.Close()
	h := NewHLS(Options{Retries: 1})
	if _, err := h.get(context.Background(), srv.URL, nil, 0, 0, maxKeySize); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected size limit error, got %v", err)
	}
}
