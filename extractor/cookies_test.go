package extractor_test

import (
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
