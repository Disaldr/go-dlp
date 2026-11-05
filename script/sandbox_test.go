package script

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	ex "github.com/Disaldr/go-dlp/extractor"
)

func TestAllowedHostsAndAllowHost(t *testing.T) {
	e := compileOne(t, `
extractor({
  name: "demo",
  match: [String.raw`+"`"+`^https?://(?:www\.)?demo\.test/v/(\d+)`+"`"+`],
  hosts: ["cdn.demo.test"],
  extract(url, m) { return {id: m[1]}; },
})`)
	e.sandboxed = true
	h := &host{sandboxed: true, allowed: e.allowedHosts("https://demo.test/v/42")}

	allowed := map[string]bool{"demo.test": true, "cdn.demo.test": true, "sub.demo.test": true}
	for host, want := range map[string]bool{
		"demo.test":      true,
		"www.demo.test":  true, // поддомен demo.test
		"cdn.demo.test":  true, // объявлен в hosts
		"sub.demo.test":  true,
		"evil.com":       false,
		"demo.test.evil": false, // не суффикс
	} {
		if got := h.allowHost(host); got != want {
			t.Errorf("allowHost(%q) = %v, want %v (allowed=%v)", host, got, want, allowed)
		}
	}
}

func TestScopeCookies(t *testing.T) {
	h := &host{env: &ex.Env{}, sandboxed: true, allowed: []string{"demo.test"}}

	// На свой домен cookies остаются.
	own := ex.Request{URL: "https://demo.test/a", Headers: map[string]string{"Cookie": "sid=1"}}
	h.scopeCookies(&own)
	if own.NoCookies || own.Headers["Cookie"] != "sid=1" {
		t.Errorf("own-domain request was scoped: %+v", own)
	}

	// На чужой домен — NoCookies и заголовок Cookie вычищен.
	third := ex.Request{URL: "https://evil.com/collect", Headers: map[string]string{"Cookie": "sid=1", "X": "y"}}
	h.scopeCookies(&third)
	if !third.NoCookies {
		t.Error("third-party request: NoCookies not set")
	}
	if _, ok := third.Headers["Cookie"]; ok {
		t.Error("third-party request: Cookie header not stripped")
	}
	if third.Headers["X"] != "y" {
		t.Error("third-party request: non-cookie header must remain")
	}

	// Несендбоксный хост ничего не трогает.
	trusted := &host{env: &ex.Env{}, sandboxed: false}
	r := ex.Request{URL: "https://evil.com/x", Headers: map[string]string{"Cookie": "sid=1"}}
	trusted.scopeCookies(&r)
	if r.NoCookies || r.Headers["Cookie"] != "sid=1" {
		t.Error("trusted host must not scope cookies")
	}
}

// Сендбоксный скрипт не может прочитать cookie чужого домена через cookie().
func TestSandboxCookieAPIDenied(t *testing.T) {
	jar, _ := cookiejar.New(nil)
	set := func(rawURL, name, val string) {
		u, _ := url.Parse(rawURL)
		jar.SetCookies(u, []*http.Cookie{{Name: name, Value: val}})
	}
	set("https://demo.test/", "own", "OWN")
	set("https://accounts.google.com/", "SID", "SECRET")
	en := &ex.Env{HTTP: &http.Client{Jar: jar}}

	src := `
extractor({
  name: "demo",
  match: [String.raw` + "`" + `^https?://demo\.test/v/(\d+)` + "`" + `],
  extract(url, m) {
    return {id: m[1], title: cookie("https://demo.test/","own") + "|" + cookie("https://accounts.google.com/","SID")};
  },
})`
	e := compileOne(t, src)
	e.sandboxed = true
	info, err := e.Extract(context.Background(), en, "https://demo.test/v/7")
	if err != nil {
		t.Fatal(err)
	}
	// Свой домен виден, cookie Google — скрыт.
	if !strings.HasPrefix(info.Title, "OWN|") {
		t.Errorf("own-domain cookie not readable: %q", info.Title)
	}
	if strings.Contains(info.Title, "SECRET") {
		t.Errorf("sandboxed script read third-party cookie: %q", info.Title)
	}

	// Без песочницы — доступны оба (доверенный скрипт).
	e2 := compileOne(t, src)
	e2.sandboxed = false
	info2, err := e2.Extract(context.Background(), en, "https://demo.test/v/7")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(info2.Title, "SECRET") {
		t.Errorf("trusted script should read cookie: %q", info2.Title)
	}
}
