package extractor

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"golang.org/x/crypto/pbkdf2"
)

func TestParseBrowserSpec(t *testing.T) {
	cases := []struct {
		spec, browser, profile string
		wantErr                bool
	}{
		{"chrome", "chrome", "", false},
		{"Chrome", "chrome", "", false},
		{"  firefox ", "firefox", "", false},
		{"chrome:Profile 1", "chrome", "Profile 1", false},
		{"firefox:/path/to/profile", "firefox", "/path/to/profile", false},
		{"safari", "safari", "", false},
		{"chrome:", "", "", true},
		{"netscape", "", "", true},
		{"", "", "", true},
	}
	for _, c := range cases {
		browser, profile, err := parseBrowserSpec(c.spec)
		if (err != nil) != c.wantErr {
			t.Errorf("parseBrowserSpec(%q) err = %v, wantErr %v", c.spec, err, c.wantErr)
			continue
		}
		if err == nil && (browser != c.browser || profile != c.profile) {
			t.Errorf("parseBrowserSpec(%q) = %q, %q; want %q, %q", c.spec, browser, profile, c.browser, c.profile)
		}
	}
}

func TestBrowserRoots(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	// Chrome on macOS points at the Application Support directory.
	roots := browserRoots("chrome", "darwin", "/home/u", env(nil))
	if len(roots) != 1 || roots[0] != "/home/u/Library/Application Support/Google/Chrome" {
		t.Errorf("chrome darwin roots = %v", roots)
	}
	// Firefox on Linux honours XDG_CONFIG_HOME and lists fallbacks.
	roots = browserRoots("firefox", "linux", "/home/u", env(map[string]string{"XDG_CONFIG_HOME": "/cfg"}))
	if len(roots) == 0 || roots[0] != "/cfg/mozilla/firefox" {
		t.Errorf("firefox linux roots = %v", roots)
	}
	// Safari addresses a file, not a directory.
	roots = browserRoots("safari", "darwin", "/home/u", env(nil))
	if len(roots) == 0 || filepath.Base(roots[0]) != "Cookies.binarycookies" {
		t.Errorf("safari roots = %v", roots)
	}
	// Chrome on Windows uses LOCALAPPDATA.
	roots = browserRoots("chrome", "windows", `C:\u`, env(map[string]string{"LOCALAPPDATA": `C:\u\AppData\Local`}))
	if len(roots) != 1 || !bytes.Contains([]byte(roots[0]), []byte("Google")) {
		t.Errorf("chrome windows roots = %v", roots)
	}
}

func TestFindBrowserCookies(t *testing.T) {
	dir := t.TempDir()
	// A bare profile name resolves under the browser data directory;
	// the newer "Network/Cookies" location wins when it is more recent.
	root := filepath.Join(dir, "data")
	prof := filepath.Join(root, "Default")
	net := filepath.Join(prof, "Network")
	if err := os.MkdirAll(net, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(prof, "Cookies")
	newer := filepath.Join(net, "Cookies")
	mustWrite(t, old, "x")
	mustWrite(t, newer, "y")
	os.Chtimes(old, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour))

	got, err := findBrowserCookies("chrome", "Default", []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if abs, _ := filepath.Abs(newer); got != abs {
		t.Errorf("findBrowserCookies = %q, want %q", got, newer)
	}

	// A WAL file newer than the database bumps the selection timestamp.
	mustWrite(t, newer+"-wal", "z")
	// Missing profile yields a helpful error instead of a panic.
	if _, err := findBrowserCookies("chrome", "Nope", []string{root}); err == nil {
		t.Error("expected error for missing profile")
	}

	// An explicit path addresses a profile directory directly.
	got, err = findBrowserCookies("chrome", prof, nil)
	if err != nil {
		t.Fatal(err)
	}
	if abs, _ := filepath.Abs(newer); got != abs {
		t.Errorf("explicit path = %q, want %q", got, newer)
	}
}

func TestReadFirefoxCookies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(q string, args ...any) {
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	exec("PRAGMA user_version = 16")
	exec(`CREATE TABLE moz_cookies (host TEXT, name TEXT, value TEXT, path TEXT,
		expiry INTEGER, isSecure INTEGER, isHttpOnly INTEGER, originAttributes TEXT)`)
	future := (time.Now().Unix() + 86400) * 1000 // schema 16 stores milliseconds
	exec(`INSERT INTO moz_cookies VALUES (?,?,?,?,?,?,?,?)`, ".youtube.com", "SID", "abc", "/", future, 1, 1, "")
	// A cookie in a container (non-empty originAttributes) must be ignored.
	exec(`INSERT INTO moz_cookies VALUES (?,?,?,?,?,?,?,?)`, ".youtube.com", "SID", "wrong", "/", future, 1, 1, "^userContextId=2")
	// An expired cookie is dropped.
	exec(`INSERT INTO moz_cookies VALUES (?,?,?,?,?,?,?,?)`, ".old.com", "x", "y", "/", 1000, 0, 0, "")

	byHost := readWithTx(t, path, func(ctx context.Context, tx *sql.Tx) (map[string][]*http.Cookie, error) {
		return readFirefoxCookies(ctx, tx)
	})
	if got := cookieValue(byHost, "youtube.com", "SID"); got != "abc" {
		t.Errorf("firefox SID = %q, want abc", got)
	}
	if _, ok := byHost["old.com"]; ok {
		t.Error("expired cookie was imported")
	}
}

func TestReadChromiumCookies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Cookies")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(q string, args ...any) {
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	exec(`CREATE TABLE meta (key TEXT, value TEXT)`)
	exec(`INSERT INTO meta VALUES ('version', '24')`)
	exec(`CREATE TABLE cookies (host_key TEXT, name TEXT, value TEXT, encrypted_value BLOB,
		path TEXT, expires_utc INTEGER, is_secure INTEGER, is_httponly INTEGER)`)
	// Chromium microseconds since 1601-01-01 for a time one day from now.
	future := (time.Now().Unix() + 86400 + 11644473600) * 1_000_000

	// v10 Linux encryption with the fixed "peanuts" password, plus the v24
	// SHA-256(host) prefix, so the decryptor path is exercised end to end.
	key := pbkdf2.Key([]byte("peanuts"), []byte("saltysalt"), 1, 16, sha1.New)
	enc := encryptChromiumV10(t, key, "youtube.com", "secret", true)
	exec(`INSERT INTO cookies VALUES (?,?,?,?,?,?,?,?)`, "youtube.com", "SID", "", enc, "/", future, 1, 1)
	// A plaintext cookie (value already set) passes through unchanged.
	exec(`INSERT INTO cookies VALUES (?,?,?,?,?,?,?,?)`, ".example.com", "plain", "v", nil, "/", future, 0, 0)
	// An expired cookie is skipped before decryption is attempted.
	exec(`INSERT INTO cookies VALUES (?,?,?,?,?,?,?,?)`, ".old.com", "x", "y", nil, "/", 13000000000000000, 0, 0)

	byHost := readWithTx(t, path, func(ctx context.Context, tx *sql.Tx) (map[string][]*http.Cookie, error) {
		d := &chromiumDecryptor{platform: "linux"}
		return readChromiumCookies(ctx, tx, d)
	})
	if got := cookieValue(byHost, "youtube.com", "SID"); got != "secret" {
		t.Errorf("chromium decrypted SID = %q, want secret", got)
	}
	if got := cookieValue(byHost, "example.com", "plain"); got != "v" {
		t.Errorf("chromium plaintext = %q, want v", got)
	}
	if _, ok := byHost["old.com"]; ok {
		t.Error("expired chromium cookie was imported")
	}
}

func TestChromiumDecryptorRejectsAppBound(t *testing.T) {
	d := &chromiumDecryptor{platform: "windows"}
	_, err := d.decrypt([]byte("v20abcdefghij"), "youtube.com", 24)
	if err == nil {
		t.Fatal("expected App-Bound Encryption (v20) to be rejected")
	}
}

func TestSafariCookiesRoundTrip(t *testing.T) {
	future := float64(time.Now().Unix()+86400) - 978307200 // seconds since 2001-01-01
	data := buildSafariCookies(t, []safariRecord{
		{host: ".youtube.com", name: "SID", path: "/", value: "abc", secure: true, httpOnly: true, expires: future},
		{host: "www.example.com", name: "host", path: "/", value: "1", expires: future},
	})
	byHost, err := parseSafariCookies(data)
	if err != nil {
		t.Fatalf("parseSafariCookies: %v", err)
	}
	if got := cookieValue(byHost, "youtube.com", "SID"); got != "abc" {
		t.Errorf("safari SID = %q, want abc", got)
	}
	if got := cookieValue(byHost, "www.example.com", "host"); got != "1" {
		t.Errorf("safari host cookie = %q, want 1", got)
	}
	// A truncated database is rejected rather than parsed into garbage.
	if _, err := parseSafariCookies(data[:len(data)-10]); err == nil {
		t.Error("expected error for truncated Safari database")
	}
	if _, err := parseSafariCookies([]byte("nope")); err == nil {
		t.Error("expected error for bad magic")
	}
}

func TestLoadCookiesFromBrowserErrors(t *testing.T) {
	if _, err := LoadCookiesFromBrowser(nil, "chrome"); err == nil {
		t.Error("expected error for nil client")
	}
	client := &http.Client{}
	if _, err := LoadCookiesFromBrowser(client, "chrome"); err == nil {
		t.Error("expected error for client without jar")
	}
	if runtime.GOOS != "darwin" {
		// Safari is macOS-only; elsewhere the path resolution never finds it.
		if _, err := LoadCookiesFromBrowser(DefaultHTTPClient(nil), "safari"); err == nil {
			t.Error("expected error loading Safari off macOS")
		}
	}
}

// --- helpers ---

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func cookieValue(byHost map[string][]*http.Cookie, host, name string) string {
	for _, c := range byHost[host] {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

func readWithTx(t *testing.T, path string, fn func(context.Context, *sql.Tx) (map[string][]*http.Cookie, error)) map[string][]*http.Cookie {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	byHost, err := fn(context.Background(), tx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return byHost
}

func encryptChromiumV10(t *testing.T, key []byte, host, value string, v24 bool) []byte {
	t.Helper()
	plain := []byte(value)
	if v24 {
		hash := sha256.Sum256([]byte(host))
		plain = append(hash[:], plain...)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	plain = append(plain, bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, bytes.Repeat([]byte(" "), aes.BlockSize)).CryptBlocks(out, plain)
	return append([]byte("v10"), out...)
}

type safariRecord struct {
	host, name, path, value string
	secure, httpOnly        bool
	expires                 float64
}

// buildSafariCookies writes a single-page Cookies.binarycookies database.
func buildSafariCookies(t *testing.T, recs []safariRecord) []byte {
	t.Helper()
	var records [][]byte
	for _, r := range recs {
		records = append(records, buildSafariRecord(r))
	}
	// Page: 4-byte header, count, per-record offsets, a trailing 0, records.
	headerLen := 4 + 4 + len(records)*4 + 4
	offsets := make([]uint32, len(records))
	off := headerLen
	for i, rec := range records {
		offsets[i] = uint32(off)
		off += len(rec)
	}
	page := &bytes.Buffer{}
	page.Write([]byte{0, 0, 1, 0})
	binary.Write(page, binary.LittleEndian, uint32(len(records)))
	for _, o := range offsets {
		binary.Write(page, binary.LittleEndian, o)
	}
	binary.Write(page, binary.LittleEndian, uint32(0))
	for _, rec := range records {
		page.Write(rec)
	}

	out := &bytes.Buffer{}
	out.WriteString("cook")
	binary.Write(out, binary.BigEndian, uint32(1)) // one page
	binary.Write(out, binary.BigEndian, uint32(page.Len()))
	out.Write(page.Bytes())
	binary.Write(out, binary.BigEndian, uint64(0)) // trailing checksum/footer
	return out.Bytes()
}

func buildSafariRecord(r safariRecord) []byte {
	// Fixed 56-byte header then NUL-terminated domain, name, path, value.
	const header = 56
	domain := append([]byte(r.host), 0)
	name := append([]byte(r.name), 0)
	path := append([]byte(r.path), 0)
	value := append([]byte(r.value), 0)
	domOff := uint32(header)
	nameOff := domOff + uint32(len(domain))
	pathOff := nameOff + uint32(len(name))
	valOff := pathOff + uint32(len(path))
	total := valOff + uint32(len(value))

	buf := make([]byte, total)
	binary.LittleEndian.PutUint32(buf[0:], total)
	var flags uint32
	if r.secure {
		flags |= 1
	}
	if r.httpOnly {
		flags |= 4
	}
	binary.LittleEndian.PutUint32(buf[8:], flags)
	binary.LittleEndian.PutUint32(buf[16:], domOff)
	binary.LittleEndian.PutUint32(buf[20:], nameOff)
	binary.LittleEndian.PutUint32(buf[24:], pathOff)
	binary.LittleEndian.PutUint32(buf[28:], valOff)
	binary.LittleEndian.PutUint64(buf[40:], math.Float64bits(r.expires))
	copy(buf[domOff:], domain)
	copy(buf[nameOff:], name)
	copy(buf[pathOff:], path)
	copy(buf[valOff:], value)
	return buf
}

var _ = fmt.Sprintf
