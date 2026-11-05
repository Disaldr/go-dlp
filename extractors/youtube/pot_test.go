package youtube

import (
	"net/url"
	"testing"
)

func TestParsePoTokens(t *testing.T) {
	p := parsePoTokens([]string{"web.gvs+AAA,web.player+BBB", " tv.gvs+CCC ", "subs+DDD"})

	if got := p.get("web", "gvs"); got != "AAA" {
		t.Errorf("web.gvs = %q", got)
	}
	if got := p.get("web", "player"); got != "BBB" {
		t.Errorf("web.player = %q", got)
	}
	if got := p.get("tv", "gvs"); got != "CCC" {
		t.Errorf("tv.gvs = %q", got)
	}
	// Ключ без клиента — запасной для любого клиента.
	if got := p.get("web", "subs"); got != "DDD" {
		t.Errorf("web.subs (fallback) = %q", got)
	}
	// Нет токена.
	if got := p.get("web", "unknown"); got != "" {
		t.Errorf("unknown = %q, want empty", got)
	}
	if !p.any("web") || !p.any("tv") {
		t.Error("any() should be true for web and tv")
	}
	if p.empty() {
		t.Error("empty() should be false")
	}
	if !parsePoTokens(nil).empty() {
		t.Error("nil should be empty")
	}
}

func TestWithPot(t *testing.T) {
	// Пустой токен — URL без изменений.
	const base = "https://r1.googlevideo.com/videoplayback?itag=137&sig=abc"
	if got := withPot(base, ""); got != base {
		t.Errorf("withPot empty changed URL: %q", got)
	}
	got := withPot(base, "TOKEN123")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("pot") != "TOKEN123" {
		t.Errorf("pot = %q, want TOKEN123", q.Get("pot"))
	}
	// Исходные параметры сохранены.
	if q.Get("itag") != "137" || q.Get("sig") != "abc" {
		t.Errorf("original params lost: %v", q)
	}
}
