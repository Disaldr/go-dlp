package extractor

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite" // Pure Go: browser cookies do not require CGO or sqlite3 in PATH.
)

const browserNames = "chrome, chromium, firefox, brave, edge, opera, vivaldi, whale, safari"

// LoadCookiesFromBrowser читает cookies из локального профиля браузера в jar
// клиента. spec имеет формат BROWSER[:PROFILE], где PROFILE — имя каталога
// профиля или путь к нему (для Safari — путь к Cookies.binarycookies).
// Без PROFILE выбирается наиболее недавно изменённая база cookies.
// Поддерживаются Firefox, Chromium-браузеры и Safari (только macOS).
// Базы SQLite открываются только для чтения, включая актуальные данные из WAL.
// Если чтение или расшифровка не удались, jar остаётся без изменений.
func LoadCookiesFromBrowser(client *http.Client, spec string) (int, error) {
	if client == nil || client.Jar == nil {
		return 0, fmt.Errorf("cookies: HTTP client has no cookie jar")
	}
	browser, profile, err := parseBrowserSpec(spec)
	if err != nil {
		return 0, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return 0, err
	}
	path, err := findBrowserCookies(browser, profile, browserRoots(browser, runtime.GOOS, home, os.Getenv))
	if err != nil {
		return 0, err
	}
	var cookies map[string][]*http.Cookie
	if browser == "safari" {
		if runtime.GOOS != "darwin" {
			return 0, fmt.Errorf("cookies: Safari is only supported on macOS")
		}
		cookies, err = readSafariCookies(path)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cookies, err = readBrowserCookies(ctx, browser, path)
	}
	if err != nil {
		return 0, fmt.Errorf("cookies: read %s: %w", browser, err)
	}
	return setCookies(client, cookies), nil
}

func parseBrowserSpec(spec string) (browser, profile string, err error) {
	browser, profile, hasProfile := strings.Cut(spec, ":")
	browser = strings.ToLower(strings.TrimSpace(browser))
	switch browser {
	case "chrome", "chromium", "firefox", "brave", "edge", "opera", "vivaldi", "whale", "safari":
	default:
		return "", "", fmt.Errorf("cookies: unsupported browser %q (supported: %s)", browser, browserNames)
	}
	if hasProfile && strings.TrimSpace(profile) == "" {
		return "", "", fmt.Errorf("cookies: empty browser profile (expected BROWSER[:PROFILE])")
	}
	return browser, profile, nil
}

func browserRoots(browser, platform, home string, env func(string) string) []string {
	if browser == "safari" {
		return []string{
			filepath.Join(home, "Library/Cookies/Cookies.binarycookies"),
			filepath.Join(home, "Library/Containers/com.apple.Safari/Data/Library/Cookies/Cookies.binarycookies"),
		}
	}
	if browser == "firefox" {
		switch platform {
		case "darwin":
			return []string{filepath.Join(home, "Library/Application Support/Firefox/Profiles")}
		case "windows":
			return []string{
				filepath.Join(env("APPDATA"), "Mozilla/Firefox/Profiles"),
				filepath.Join(env("LOCALAPPDATA"), "Packages/Mozilla.Firefox_n80bbvh6b1yt2/LocalCache/Roaming/Mozilla/Firefox/Profiles"),
			}
		default:
			config := env("XDG_CONFIG_HOME")
			if config == "" {
				config = filepath.Join(home, ".config")
			}
			return []string{
				filepath.Join(config, "mozilla/firefox"),
				filepath.Join(home, ".mozilla/firefox"),
				filepath.Join(home, ".var/app/org.mozilla.firefox/config/mozilla/firefox"),
				filepath.Join(home, ".var/app/org.mozilla.firefox/.mozilla/firefox"),
				filepath.Join(home, "snap/firefox/common/.mozilla/firefox"),
			}
		}
	}
	switch platform {
	case "darwin":
		dir := map[string]string{
			"chrome": "Google/Chrome", "chromium": "Chromium", "brave": "BraveSoftware/Brave-Browser",
			"edge": "Microsoft Edge", "opera": "com.operasoftware.Opera", "vivaldi": "Vivaldi", "whale": "Naver/Whale",
		}[browser]
		return []string{filepath.Join(home, "Library/Application Support", dir)}
	case "windows":
		if browser == "opera" {
			return []string{filepath.Join(env("APPDATA"), "Opera Software/Opera Stable")}
		}
		dir := map[string]string{
			"chrome": "Google/Chrome/User Data", "chromium": "Chromium/User Data", "brave": "BraveSoftware/Brave-Browser/User Data",
			"edge": "Microsoft/Edge/User Data", "vivaldi": "Vivaldi/User Data", "whale": "Naver/Naver Whale/User Data",
		}[browser]
		return []string{filepath.Join(env("LOCALAPPDATA"), dir)}
	default:
		config := env("XDG_CONFIG_HOME")
		if config == "" {
			config = filepath.Join(home, ".config")
		}
		dir := map[string]string{
			"chrome": "google-chrome", "chromium": "chromium", "brave": "BraveSoftware/Brave-Browser",
			"edge": "microsoft-edge", "opera": "opera", "vivaldi": "vivaldi", "whale": "naver-whale",
		}[browser]
		roots := []string{filepath.Join(config, dir)}
		if browser == "chromium" {
			roots = append(roots, filepath.Join(home, "snap/chromium/common/chromium"))
		}
		flatpak := map[string]string{"chrome": "com.google.Chrome", "chromium": "org.chromium.Chromium", "brave": "com.brave.Browser", "edge": "com.microsoft.Edge"}[browser]
		if flatpak != "" {
			roots = append(roots, filepath.Join(home, ".var/app", flatpak, "config", dir))
		}
		return roots
	}
}

func findBrowserCookies(browser, profile string, roots []string) (string, error) {
	if strings.HasPrefix(profile, "~/") || strings.HasPrefix(profile, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		profile = filepath.Join(home, profile[2:])
	}
	explicit := profile != ""
	if explicit {
		// A bare name is relative to the browser's data directory. Paths,
		// including relative ./profile paths, address the filesystem directly.
		if filepath.IsAbs(profile) || strings.ContainsAny(profile, `/\`) {
			roots = []string{profile}
		} else {
			for i := range roots {
				roots[i] = filepath.Join(roots[i], profile)
			}
		}
	}
	var newest string
	var modified time.Time
	var accessErr error
	consider := func(path string) {
		st, err := os.Stat(path)
		if err != nil {
			if !os.IsNotExist(err) {
				accessErr = err
			}
			return
		}
		if !st.Mode().IsRegular() {
			return
		}
		mtime := st.ModTime()
		// Writes may still be in WAL while the main database is unchanged.
		if wal, err := os.Stat(path + "-wal"); err == nil && wal.ModTime().After(mtime) {
			mtime = wal.ModTime()
		}
		if newest == "" || mtime.After(modified) {
			newest, modified = path, mtime
		}
	}
	for _, root := range roots {
		if browser == "safari" {
			consider(root)
			continue
		}
		filename := "Cookies"
		if browser == "firefox" {
			filename = "cookies.sqlite"
		}
		consider(filepath.Join(root, filename))
		if browser != "firefox" {
			consider(filepath.Join(root, "Network", filename))
		}
		if explicit {
			continue
		}
		entries, err := os.ReadDir(root)
		if err != nil && !os.IsNotExist(err) {
			accessErr = err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				consider(filepath.Join(root, entry.Name(), filename))
				if browser != "firefox" {
					consider(filepath.Join(root, entry.Name(), "Network", filename))
				}
			}
		}
	}
	if newest == "" {
		if accessErr != nil {
			return "", fmt.Errorf("cookies: cannot access %s profile: %w", browser, accessErr)
		}
		return "", fmt.Errorf("cookies: could not find %s cookies database; specify %s:/path/to/profile", browser, browser)
	}
	return filepath.Abs(newest)
}

func readBrowserCookies(ctx context.Context, browser, path string) (map[string][]*http.Cookie, error) {
	// URI escaping matters for spaces, '?' and '#' in user-supplied paths.
	u := &url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	if runtime.GOOS == "windows" {
		u.Path = "/" + u.Path
	}
	u.RawQuery = "mode=ro&_pragma=busy_timeout(1000)"
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	// One transaction keeps schema metadata and cookie rows in one snapshot.
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("open database (if locked, close the browser and retry): %w", err)
	}
	defer tx.Rollback()
	if browser == "firefox" {
		return readFirefoxCookies(ctx, tx)
	}
	d := &chromiumDecryptor{platform: runtime.GOOS}
	d.password = func() ([]byte, error) { return chromiumPassword(ctx, browser) }
	d.windowsKey = func() ([]byte, error) { return chromiumWindowsKey(path) }
	return readChromiumCookies(ctx, tx, d)
}

func readFirefoxCookies(ctx context.Context, tx *sql.Tx) (map[string][]*http.Cookie, error) {
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return nil, err
	}
	// Keep container identities separate: only the normal browsing context is
	// imported. Combining containers can silently select the wrong account.
	rows, err := tx.QueryContext(ctx, `SELECT host, name, value, path, expiry, isSecure, isHttpOnly
		FROM moz_cookies WHERE originAttributes = ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byHost := map[string][]*http.Cookie{}
	for rows.Next() {
		var host string
		var expires int64
		c := &http.Cookie{}
		if err := rows.Scan(&host, &c.Name, &c.Value, &c.Path, &expires, &c.Secure, &c.HttpOnly); err != nil {
			return nil, err
		}
		// Firefox 142 (schema 16) changed expiry from seconds to milliseconds.
		if version >= 16 {
			expires /= 1000
		}
		if expires > 0 {
			c.Expires = time.Unix(expires, 0)
		}
		appendBrowserCookie(byHost, host, c)
	}
	return byHost, rows.Err()
}

func readChromiumCookies(ctx context.Context, tx *sql.Tx, decryptor *chromiumDecryptor) (map[string][]*http.Cookie, error) {
	var version int
	if err := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = 'version'").Scan(&version); err != nil {
		return nil, fmt.Errorf("read Chromium database version: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT host_key, name, value, encrypted_value, path, expires_utc, is_secure, is_httponly FROM cookies`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byHost := map[string][]*http.Cookie{}
	for rows.Next() {
		var host string
		var encrypted []byte
		var expires int64
		c := &http.Cookie{}
		if err := rows.Scan(&host, &c.Name, &c.Value, &encrypted, &c.Path, &expires, &c.Secure, &c.HttpOnly); err != nil {
			return nil, err
		}
		if expires > 0 {
			// Chromium stores microseconds since 1601-01-01 UTC.
			c.Expires = time.Unix(expires/1_000_000-11644473600, (expires%1_000_000)*1000)
			if !c.Expires.After(time.Now()) {
				continue
			}
		}
		if len(encrypted) > 0 && c.Value == "" {
			value, err := decryptor.decrypt(encrypted, host, version)
			if err != nil {
				return nil, fmt.Errorf("decrypt Chromium cookies: %w", err)
			}
			c.Value = string(value)
		}
		appendBrowserCookie(byHost, host, c)
	}
	return byHost, rows.Err()
}

func appendBrowserCookie(byHost map[string][]*http.Cookie, domain string, c *http.Cookie) {
	host := strings.TrimPrefix(domain, ".")
	if host == "" || c.Name == "" || (!c.Expires.IsZero() && !c.Expires.After(time.Now())) {
		return
	}
	if strings.HasPrefix(domain, ".") {
		c.Domain = domain
	}
	if c.Path == "" {
		c.Path = "/"
	}
	byHost[host] = append(byHost[host], c)
}
