package script

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/media"
)

func env() *ex.Env { return &ex.Env{HTTP: http.DefaultClient} }

func compileOne(t *testing.T, src string) *Extractor {
	t.Helper()
	exts, err := Compile("t.js", src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(exts) != 1 {
		t.Fatalf("got %d extractors", len(exts))
	}
	return exts[0]
}

func TestExtractHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			fmt.Fprintf(w, `<script>window.__DATA__ = {"title":"Hello","q":%q,"ua":%q};</script>`, r.URL.Query().Get("x"), r.Header.Get("X-Test"))
		case "/api":
			var in map[string]any
			json.NewDecoder(r.Body).Decode(&in)
			json.NewEncoder(w).Encode(map[string]any{"echo": in["a"]})
		default:
			http.Error(w, "nope", 404)
		}
	}))
	defer srv.Close()

	e := compileOne(t, `
extractor({
  name: "demo",
  match: [String.raw`+"`"+`^https?://demo\.test/v/(\d+)`+"`"+`],
  extract(url, m) {
    const page = http.get(`+"`"+`${arg("base","")}/page`+"`"+`, {query: {x: "1"}, headers: {"X-Test": "yes"}}).text;
    const d = jsonAfter(page, "__DATA__");
    const api = http.post(arg("base","") + "/api", {json: {a: m[1]}}).json();
    const miss = http.get(arg("base","") + "/missing", {allowErrors: true});
    return {
      id: m[1], title: d.title + " " + d.q + " " + d.ua + " " + api.echo + " " + miss.status,
      duration: parseDuration("PT1M2S") + parseDuration("1:00"),
      formats: [{url: urlJoin("https://cdn.test/a/b.mp4", "c.mp4"), format_id: hash.md5("x").slice(0, 4)}],
    };
  },
});`)
	if !e.Suitable("https://demo.test/v/42") || e.Suitable("https://other.test/v/42") {
		t.Fatal("Suitable mismatch")
	}
	en := env()
	en.Args = map[string]map[string][]string{"demo": {"base": {srv.URL}}}
	info, err := e.Extract(context.Background(), en, "https://demo.test/v/42")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if info.ID != "42" || info.Title != "Hello 1 yes 42 404" || info.Duration != 122 {
		t.Fatalf("info = %+v", info)
	}
	if len(info.Formats) != 1 || info.Formats[0].URL != "https://cdn.test/a/c.mp4" || info.Formats[0].FormatID != "9dd4" {
		t.Fatalf("formats = %+v", info.Formats[0])
	}
	if info.WebpageURL != "https://demo.test/v/42" {
		t.Fatalf("webpage_url = %q", info.WebpageURL)
	}
}

func TestErrors(t *testing.T) {
	e := compileOne(t, `extractor({name: "e", match: ["^x://(\\w+)"], extract(url, m) {
		if (m[1] === "geo") fail.geo("blocked here");
		if (m[1] === "bug") return null.foo;
		if (m[1] === "loop") for (;;) {}
		return {id: "1"};
	}});`)
	_, err := e.Extract(context.Background(), env(), "x://geo")
	if !errors.Is(err, ex.ErrGeoRestricted) {
		t.Fatalf("geo: %v", err)
	}
	_, err = e.Extract(context.Background(), env(), "x://bug")
	if err == nil || !strings.Contains(err.Error(), "script error") {
		t.Fatalf("bug: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = e.Extract(ctx, env(), "x://loop")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatalf("loop: %v after %v", err, time.Since(start))
	}
}

func TestCompileErrors(t *testing.T) {
	for name, src := range map[string]string{
		"syntax":    `extractor({`,
		"no-name":   `extractor({match: ["x"], extract(){}})`,
		"lookahead": `extractor({name: "a", match: [/(?=x)/], extract(){}})`,
		"no-calls":  `var x = 1;`,
		"network":   `http.get("https://example.com/")`,
		"dup":       `extractor({name:"a",match:["x"],extract(){}}); extractor({name:"A",match:["y"],extract(){}})`,
	} {
		if _, err := Compile(name+".js", src); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

type fakeExt struct{ name, title string }

func (f *fakeExt) Name() string           { return f.name }
func (f *fakeExt) Suitable(u string) bool { return strings.HasPrefix(u, "fake://") }
func (f *fakeExt) Extract(context.Context, *ex.Env, string) (*media.Info, error) {
	return &media.Info{ID: "builtin", Title: f.title}, nil
}

func pack(t *testing.T, version int64, scripts map[string]string) *Pack {
	t.Helper()
	exts, err := compileAll(scripts)
	if err != nil {
		t.Fatal(err)
	}
	return &Pack{Version: version, Extractors: exts}
}

func TestManagerShadowAndRestore(t *testing.T) {
	builtin := &fakeExt{name: "site", title: "go"}
	reg := ex.NewRegistry(builtin)
	m := NewManager(reg)

	// v1 подменяет встроенный «site»; ошибка скрипта → запасной встроенный.
	if ok, err := m.Install(pack(t, 1, map[string]string{
		"site.js":  `extractor({name:"site", match:["^fake://(\\w+)"], extract(u, m){ if (m[1]==="bug") throw new Error("x"); return {id:"script"}; }})`,
		"extra.js": `extractor({name:"extra", match:["^extra://"], extract(){ return {id:"e"}; }})`,
	})); !ok || err != nil {
		t.Fatalf("install v1: %v %v", ok, err)
	}
	got, _ := reg.Get("site")
	info, err := got.Extract(context.Background(), env(), "fake://ok")
	if err != nil || info.ID != "script" {
		t.Fatalf("script: %+v %v", info, err)
	}
	info, err = got.Extract(context.Background(), env(), "fake://bug")
	if err != nil || info.ID != "builtin" {
		t.Fatalf("fallback: %+v %v", info, err)
	}

	// Не новее — игнорируется.
	if ok, _ := m.Install(pack(t, 1, map[string]string{})); ok {
		t.Fatal("same version installed")
	}
	// v2 без site и extra: встроенный возвращается, extra удаляется.
	if ok, err := m.Install(pack(t, 2, map[string]string{
		"other.js": `extractor({name:"other", match:["^o://"], extract(){ return {id:"o"}; }})`,
	})); !ok || err != nil {
		t.Fatalf("install v2: %v %v", ok, err)
	}
	if got, _ := reg.Get("site"); got != builtin {
		t.Fatalf("builtin not restored: %T", got)
	}
	if _, ok := reg.Get("extra"); ok {
		t.Fatal("extra not removed")
	}
	if _, ok := reg.Get("other"); !ok || m.Version() != 2 {
		t.Fatal("other not installed")
	}
}

func signedPack(t *testing.T, priv ed25519.PrivateKey, version int64, body string) ([]byte, []byte) {
	t.Helper()
	data, _, err := BuildPack(fstest.MapFS{
		"VERSION": {Data: []byte(fmt.Sprint(version))},
		"a.js":    {Data: []byte(`extractor({name:"a", match:["^a://"], extract(){ return {id:"` + body + `"}; }})`)},
	}, HostAPI)
	if err != nil {
		t.Fatal(err)
	}
	return data, SignPack(data, priv)
}

func TestParsePackSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	data, sig := signedPack(t, priv, 3, "x")
	p, err := ParsePack(data, sig, pub)
	if err != nil || p.Version != 3 || len(p.Extractors) != 1 {
		t.Fatalf("ParsePack: %+v %v", p, err)
	}
	tampered := []byte(strings.Replace(string(data), `\"x\"`, `\"y\"`, 1))
	if _, err := ParsePack(tampered, sig, pub); !errors.Is(err, ErrSignature) {
		t.Fatalf("tampered: %v", err)
	}
	otherPub, _, _ := ed25519.GenerateKey(nil)
	if _, err := ParsePack(data, sig, otherPub); !errors.Is(err, ErrSignature) {
		t.Fatalf("other key: %v", err)
	}
	future := []byte(strings.Replace(string(data), `"host_api": 1`, `"host_api": 99`, 1))
	if _, err := ParsePack(future, SignPack(future, priv), pub); err == nil || !strings.Contains(err.Error(), "host API") {
		t.Fatalf("future host api: %v", err)
	}
}

func TestUpdater(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	var cur atomic.Pointer[[2][]byte]
	set := func(v int64, body string) {
		d, s := signedPack(t, priv, v, body)
		cur.Store(&[2][]byte{d, s})
	}
	set(5, "v5")
	var packGets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := cur.Load()
		etag := fmt.Sprintf(`"%x"`, len(c[0]))
		switch r.URL.Path {
		case "/pack.json":
			packGets.Add(1)
			if r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", etag)
			w.Write(c[0])
		case "/pack.json.sig":
			w.Write(c[1])
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	reg := ex.NewRegistry()
	m := NewManager(reg)
	u := &Updater{URL: srv.URL + "/pack.json", PublicKey: pub, CacheDir: dir}
	if ok, err := u.Check(context.Background(), m); !ok || err != nil {
		t.Fatalf("check: %v %v", ok, err)
	}
	if ok, err := u.Check(context.Background(), m); ok || err != nil {
		t.Fatalf("not modified: %v %v", ok, err)
	}
	// Старый пакет (откат) не ставится.
	set(4, "v4")
	if ok, err := u.Check(context.Background(), m); ok || err != nil || m.Version() != 5 {
		t.Fatalf("downgrade: %v %v v=%d", ok, err, m.Version())
	}

	// Новый процесс: пакет поднимается из кеша без сети.
	m2 := NewManager(ex.NewRegistry())
	u2 := &Updater{URL: "http://127.0.0.1:1/none", PublicKey: pub, CacheDir: dir}
	if ok, err := u2.LoadCache(m2); !ok || err != nil || m2.Version() != 5 {
		t.Fatalf("cache: %v %v v=%d", ok, err, m2.Version())
	}
	// Подменённый кеш отвергается.
	if err := writeFile(dir+"/pack.json", []byte(`{"format":1,"version":99,"scripts":{}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := u2.LoadCache(NewManager(ex.NewRegistry())); !errors.Is(err, ErrSignature) {
		t.Fatalf("tampered cache: %v", err)
	}
}

func TestBuiltin(t *testing.T) {
	p, err := Builtin()
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}
	if p.Version < 1 || len(p.Extractors) == 0 {
		t.Fatalf("builtin pack: v%d, %d extractors", p.Version, len(p.Extractors))
	}
	for _, c := range []struct{ name, url string }{
		{"twitter", "https://x.com/user/status/1585341984679469056"},
		{"twitter", "https://twitter.com/i/web/status/1585341984679469056"},
		{"tiktok", "https://www.tiktok.com/@scout2015/video/6718335390845095173"},
		{"tiktok", "https://vm.tiktok.com/ZMabcdef/"},
	} {
		found := false
		for _, e := range p.Extractors {
			if e.Name() == c.name && e.Suitable(c.url) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s does not match %s", c.name, c.url)
		}
	}
}

func writeFile(path string, data []byte) error { return os.WriteFile(path, data, 0o644) }
