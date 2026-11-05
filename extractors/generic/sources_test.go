package generic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ex "github.com/Disaldr/go-dlp/extractor"
)

func TestPlayerSourceURLs(t *testing.T) {
	page := `
<script>
 var player = new Playerjs({id:"p", file:"https://cdn.example/a/master.m3u8?t=1"});
 window.__NEXT_DATA__ = {"props":{"video":{"hls":"https:\/\/cdn.example\/b\/index.mpd"}}};
 const cfg = {"sources":[{"src":"https://cdn.example/c/movie.mp4"}]};
 thumbnail = "https://cdn.example/poster.jpg";
</script>`
	got := playerSourceURLs(page)
	want := map[string]bool{
		"https://cdn.example/a/master.m3u8?t=1": true,
		"https://cdn.example/b/index.mpd":       true,
		"https://cdn.example/c/movie.mp4":       true,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %d urls", got, len(want))
	}
	for _, u := range got {
		if !want[u] {
			t.Errorf("unexpected url %q", u)
		}
	}
}

// Онлайн-кинотеатр: плеер отдаёт HLS в JSON-конфиге, iframe нет.
func TestGenericFromPlayerConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<!doctype html><html><head><title>Фильм</title></head><body>
<div id="player"></div>
<script>new Playerjs({id:"player", file:"` + "https://cdn.example/hls/master.m3u8" + `"});</script>
</body></html>`))
	}))
	defer srv.Close()
	env := &ex.Env{HTTP: srv.Client()}
	info, err := New().Extract(context.Background(), env, srv.URL+"/watch/film-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Formats) != 1 || !strings.HasSuffix(info.Formats[0].URL, "master.m3u8") {
		t.Fatalf("formats = %+v", info.Formats)
	}
	if info.Title != "Фильм" {
		t.Errorf("title = %q", info.Title)
	}
}
