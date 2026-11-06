package dlp

import (
	"net/http"
	"net/url"
	"testing"
)

func TestCookieHeader(t *testing.T) {
	c, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse("https://example.com/video")
	c.Env().HTTP.Jar.SetCookies(u, []*http.Cookie{
		{Name: "sid", Value: "abc"},
		{Name: "pref", Value: "1"},
	})
	got := c.CookieHeader("https://example.com/video")
	if got != "sid=abc; pref=1" {
		t.Errorf("CookieHeader = %q, want %q", got, "sid=abc; pref=1")
	}
	if other := c.CookieHeader("https://other.test/x"); other != "" {
		t.Errorf("CookieHeader for unrelated host = %q, want empty", other)
	}
}

func TestCloneReusesScriptsIndependentJar(t *testing.T) {
	c, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	c2, err := c.Clone(CloneOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Дорогая часть (скомпилированные экстракторы) переиспользуется.
	if c2.ExtractorsVersion() != c.ExtractorsVersion() {
		t.Errorf("clone extractors version = %d, want %d", c2.ExtractorsVersion(), c.ExtractorsVersion())
	}
	if c2.Scripts() != c.Scripts() {
		t.Error("clone should reuse the same script manager")
	}
	// Но cookie-jar у клона собственный.
	if c2.Env().HTTP == c.Env().HTTP {
		t.Error("clone must have its own HTTP client")
	}
	u, _ := url.Parse("https://example.com/")
	c.Env().HTTP.Jar.SetCookies(u, []*http.Cookie{{Name: "sid", Value: "abc"}})
	if got := c2.CookieHeader("https://example.com/"); got != "" {
		t.Errorf("clone jar leaked cookies from origin: %q", got)
	}
}

func TestCloneAppliesUserAgent(t *testing.T) {
	c, err := New(Options{UserAgent: "orig/1"})
	if err != nil {
		t.Fatal(err)
	}
	c2, err := c.Clone(CloneOptions{UserAgent: "clone/2"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Env().UserAgent != "orig/1" {
		t.Errorf("origin UA changed to %q", c.Env().UserAgent)
	}
	if c2.Env().UserAgent != "clone/2" {
		t.Errorf("clone UA = %q, want clone/2", c2.Env().UserAgent)
	}
}

func TestExtractWithNoPlaylistOverride(t *testing.T) {
	c, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	env := c.envFor(Request{NoPlaylist: &yes})
	if !env.NoPlaylist {
		t.Error("envFor should set NoPlaylist=true from request")
	}
	if c.env.NoPlaylist {
		t.Error("base env NoPlaylist must stay false (no mutation)")
	}
	if env == c.env {
		t.Error("envFor with override must return a copy, not the base env")
	}
	if base := c.envFor(Request{}); base != c.env {
		t.Error("envFor without override should return the base env")
	}
}
