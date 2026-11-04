// Package impersonate реализует имитацию браузера на сетевом уровне:
// TLS ClientHello с отпечатком настоящего браузера (uTLS), HTTP/2 и
// набор браузерных заголовков. Аналог обработчика curl_cffi в yt-dlp
// (--impersonate). Нужен сайтам, которые отвергают «нестандартные» клиенты
// по TLS-отпечатку (JA3/JA4).
package impersonate

import (
	"fmt"
	"slices"
	"strings"

	utls "github.com/refraction-networking/utls"
)

// Header — заголовок, добавляемый к запросу, если вызывающий его не задал.
type Header struct{ Key, Value string }

// Target — имитируемый браузер.
type Target struct {
	// Name — каноничное имя: client-version[-os], например "chrome-133-windows".
	Name    string
	Client  string // chrome, firefox, safari, edge
	Version string
	OS      string
	Hello   utls.ClientHelloID
	Headers []Header
	// H2 — HTTP/2-отпечаток (nil — как у Go).
	H2 *H2Profile
}

func (t *Target) String() string { return t.Name }

func chromeHeaders(ver, platform, ua string) []Header {
	return []Header{
		{"sec-ch-ua", fmt.Sprintf(`"Chromium";v="%s", "Not(A:Brand";v="99", "Google Chrome";v="%s"`, ver, ver)},
		{"sec-ch-ua-mobile", "?0"},
		{"sec-ch-ua-platform", `"` + platform + `"`},
		{"Upgrade-Insecure-Requests", "1"},
		{"User-Agent", ua},
		{"Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"},
		{"Sec-Fetch-Site", "none"},
		{"Sec-Fetch-Mode", "navigate"},
		{"Sec-Fetch-User", "?1"},
		{"Sec-Fetch-Dest", "document"},
		{"Accept-Encoding", "gzip, deflate, br, zstd"},
		{"Accept-Language", "en-US,en;q=0.9"},
		{"Priority", "u=0, i"},
	}
}

func chromeUA(ver, os string) string {
	plat := "Windows NT 10.0; Win64; x64"
	if os == "macos" {
		plat = "Macintosh; Intel Mac OS X 10_15_7"
	}
	return fmt.Sprintf("Mozilla/5.0 (%s) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%s.0.0.0 Safari/537.36", plat, ver)
}

func chrome(ver string, hello utls.ClientHelloID, os string) *Target {
	platform := map[string]string{"windows": "Windows", "macos": "macOS"}[os]
	return &Target{
		Name: "chrome-" + ver + "-" + os, Client: "chrome", Version: ver, OS: os, Hello: hello,
		Headers: chromeHeaders(ver, platform, chromeUA(ver, os)),
		H2:      chromeH2,
	}
}

var firefoxHeaders = []Header{
	{"User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:120.0) Gecko/20100101 Firefox/120.0"},
	{"Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"},
	{"Accept-Language", "en-US,en;q=0.5"},
	{"Accept-Encoding", "gzip, deflate, br"},
	{"Upgrade-Insecure-Requests", "1"},
	{"Sec-Fetch-Dest", "document"},
	{"Sec-Fetch-Mode", "navigate"},
	{"Sec-Fetch-Site", "none"},
	{"Sec-Fetch-User", "?1"},
	{"TE", "trailers"},
}

var safariHeaders = []Header{
	{"User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Safari/605.1.15"},
	{"Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"},
	{"Accept-Language", "en-US,en;q=0.9"},
	{"Accept-Encoding", "gzip, deflate, br"},
}

// Targets — поддерживаемые цели в порядке предпочтения (первая — по умолчанию).
var Targets = []*Target{
	chrome("133", utls.HelloChrome_133, "windows"),
	chrome("133", utls.HelloChrome_133, "macos"),
	chrome("131", utls.HelloChrome_131, "windows"),
	chrome("120", utls.HelloChrome_120, "windows"),
	{Name: "safari-16.0-macos", Client: "safari", Version: "16.0", OS: "macos", Hello: utls.HelloSafari_16_0, Headers: safariHeaders, H2: safariH2},
	{Name: "firefox-120-windows", Client: "firefox", Version: "120", OS: "windows", Hello: utls.HelloFirefox_120, Headers: firefoxHeaders, H2: firefoxH2},
}

// Lookup находит цель по спецификации в стиле yt-dlp: "", "any" или "chrome"
// (лучшая подходящая), "chrome-131", "chrome:131", "chrome-133-macos", "safari", "firefox".
func Lookup(spec string) (*Target, error) {
	spec = strings.ToLower(strings.TrimSpace(spec))
	spec = strings.ReplaceAll(spec, ":", "-")
	if spec == "" || spec == "any" || spec == "true" {
		return Targets[0], nil
	}
	parts := strings.SplitN(spec, "-", 3)
	for _, t := range Targets {
		if parts[0] != t.Client {
			continue
		}
		if len(parts) > 1 && parts[1] != "" && parts[1] != t.Version {
			continue
		}
		if len(parts) > 2 && parts[2] != t.OS {
			continue
		}
		return t, nil
	}
	return nil, fmt.Errorf("impersonate: unsupported target %q (available: %s)", spec, strings.Join(Names(), ", "))
}

// Names — список имён целей.
func Names() []string {
	names := make([]string, 0, len(Targets))
	for _, t := range Targets {
		names = append(names, t.Name)
	}
	return slices.Clip(names)
}
