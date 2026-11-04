package impersonate

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestLookup(t *testing.T) {
	for spec, want := range map[string]string{
		"": "chrome-133-windows", "chrome": "chrome-133-windows", "chrome:131": "chrome-131-windows",
		"chrome-133-macos": "chrome-133-macos", "safari": "safari-16.0-macos", "firefox-120": "firefox-120-windows",
	} {
		got, err := Lookup(spec)
		if err != nil || got.Name != want {
			t.Errorf("Lookup(%q) = %v, %v; want %s", spec, got, err, want)
		}
	}
	if _, err := Lookup("netscape"); err == nil {
		t.Error("expected error for unknown target")
	}
}

// TestOnlineFingerprint проверяет отпечаток через сервис, который отражает
// TLS/HTTP2-параметры клиента. GODLP_ONLINE=1 go test ./impersonate -v
func TestOnlineFingerprint(t *testing.T) {
	if os.Getenv("GODLP_ONLINE") != "1" {
		t.Skip("online test")
	}
	for _, spec := range []string{"chrome", "firefox", "safari"} {
		p := &Pool{}
		c, err := p.Client(spec)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.Get("https://tls.peet.ws/api/clean")
		if err != nil {
			t.Fatalf("%s: %v", spec, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Logf("%s: %s %s", spec, resp.Proto, strings.TrimSpace(string(body)))
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d", spec, resp.StatusCode)
		}
		// эталонный HTTP/2-отпечаток Chrome (akamai)
		const chromeAkamai = `"akamai": "1:65536;2:0;4:6291456;6:262144|15663105|0|m,a,s,p"`
		if spec == "chrome" && !strings.Contains(string(body), chromeAkamai) {
			t.Errorf("chrome HTTP/2 fingerprint mismatch, want %s", chromeAkamai)
		}
		if resp.Proto != "HTTP/2.0" {
			t.Errorf("%s: proto %s, want HTTP/2.0", spec, resp.Proto)
		}
	}
}
