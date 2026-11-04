package extractor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ex "github.com/Disaldr/go-dlp/extractor"
)

func TestLoadCookies(t *testing.T) {
	env := ex.NewEnv()
	data := "# Netscape HTTP Cookie File\n" +
		".vimeo.com\tTRUE\t/\tTRUE\t4102444800\tvimeo\tsecret\n" +
		"#HttpOnly_www.example.com\tFALSE\t/\tFALSE\t0\tsid\tabc\n" +
		".old.com\tTRUE\t/\tFALSE\t1000\texpired\tx\n"
	n, err := ex.LoadCookies(env.HTTP, strings.NewReader(data))
	if err != nil || n != 2 {
		t.Fatalf("LoadCookies = %d, %v", n, err)
	}
	if v := env.Cookie("https://player.vimeo.com/video/1", "vimeo"); v != "secret" {
		t.Errorf("domain cookie not visible on subdomain: %q", v)
	}
	if v := env.Cookie("https://www.example.com/", "sid"); v != "abc" {
		t.Errorf("host cookie = %q", v)
	}
	if _, err := ex.LoadCookies(env.HTTP, strings.NewReader("bad line\n")); err == nil {
		t.Error("expected parse error")
	}
}

func TestLoadCookiesJSON(t *testing.T) {
	env := ex.NewEnv()
	path := filepath.Join(t.TempDir(), "cookies.json")
	data := `[
	 {"domain": ".youtube.com", "name": "SID", "value": "abc", "path": "/", "secure": true, "httpOnly": true, "hostOnly": false, "session": false, "expirationDate": 4102444800.5},
	 {"domain": "www.example.com", "name": "host", "value": "1", "path": "/", "hostOnly": true, "session": true},
	 {"domain": ".old.com", "name": "expired", "value": "x", "expirationDate": 1000}
	]`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	n, err := ex.LoadCookiesFile(env.HTTP, path)
	if err != nil || n != 2 {
		t.Fatalf("LoadCookiesFile(json) = %d, %v", n, err)
	}
	if v := env.Cookie("https://m.youtube.com/watch", "SID"); v != "abc" {
		t.Errorf("domain cookie not visible on subdomain: %q", v)
	}
	if v := env.Cookie("https://www.example.com/", "host"); v != "1" {
		t.Errorf("host-only cookie = %q", v)
	}
	if v := env.Cookie("https://sub.www.example.com/", "host"); v != "" {
		t.Errorf("host-only cookie leaked to subdomain: %q", v)
	}
}
