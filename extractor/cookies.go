package extractor

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// LoadCookiesFile загружает cookies в формате Netscape (cookies.txt,
// как --cookies в yt-dlp) в jar клиента. Возвращает число загруженных cookies.
func LoadCookiesFile(client *http.Client, path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return LoadCookies(client, f)
}

// LoadCookies читает cookies в формате Netscape из r.
func LoadCookies(client *http.Client, r io.Reader) (int, error) {
	if client == nil || client.Jar == nil {
		return 0, fmt.Errorf("cookies: HTTP client has no cookie jar")
	}
	byHost := map[string][]*http.Cookie{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimRight(sc.Text(), "\r")
		httpOnly := false
		if rest, ok := strings.CutPrefix(line, "#HttpOnly_"); ok {
			line, httpOnly = rest, true
		}
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 7 {
			return 0, fmt.Errorf("cookies: line %d: expected 7 tab-separated fields, got %d", lineNo, len(fields))
		}
		domain, _, cpath, secure, expires, name, value := fields[0], fields[1], fields[2], fields[3], fields[4], fields[5], fields[6]
		c := &http.Cookie{
			Name:     name,
			Value:    value,
			Path:     cpath,
			Secure:   strings.EqualFold(secure, "TRUE"),
			HttpOnly: httpOnly,
		}
		if strings.HasPrefix(domain, ".") {
			c.Domain = domain
		}
		if exp, err := strconv.ParseInt(expires, 10, 64); err == nil && exp > 0 {
			c.Expires = time.Unix(exp, 0)
			if c.Expires.Before(time.Now()) {
				continue
			}
		}
		host := strings.TrimPrefix(domain, ".")
		byHost[host] = append(byHost[host], c)
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	n := 0
	for host, cs := range byHost {
		u := &url.URL{Scheme: "https", Host: host, Path: "/"}
		client.Jar.SetCookies(u, cs)
		n += len(cs)
	}
	return n, nil
}

// Cookie возвращает значение cookie для URL из jar окружения ("" если нет).
func (e *Env) Cookie(rawURL, name string) string {
	if e.HTTP == nil || e.HTTP.Jar == nil {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	for _, c := range e.HTTP.Jar.Cookies(u) {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}
