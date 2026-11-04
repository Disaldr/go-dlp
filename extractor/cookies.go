package extractor

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// LoadCookiesFile загружает cookies в jar клиента. Поддерживаются формат
// Netscape (cookies.txt, как --cookies в yt-dlp) и JSON-экспорт расширений
// браузера (Cookie-Editor, EditThisCookie и аналоги): массив объектов
// {domain, name, value, path, secure, httpOnly, expirationDate}.
// Возвращает число загруженных cookies.
func LoadCookiesFile(client *http.Client, path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	if trimmed := bytes.TrimSpace(data); len(trimmed) > 0 && trimmed[0] == '[' {
		return LoadCookiesJSON(client, trimmed)
	}
	return LoadCookies(client, bytes.NewReader(data))
}

// jsonCookie — запись JSON-экспорта cookies из расширения браузера.
type jsonCookie struct {
	Domain         string  `json:"domain"`
	Name           string  `json:"name"`
	Value          string  `json:"value"`
	Path           string  `json:"path"`
	Secure         bool    `json:"secure"`
	HTTPOnly       bool    `json:"httpOnly"`
	HostOnly       bool    `json:"hostOnly"`
	Session        bool    `json:"session"`
	ExpirationDate float64 `json:"expirationDate"`
}

// LoadCookiesJSON читает JSON-экспорт cookies (см. LoadCookiesFile).
func LoadCookiesJSON(client *http.Client, data []byte) (int, error) {
	if client == nil || client.Jar == nil {
		return 0, fmt.Errorf("cookies: HTTP client has no cookie jar")
	}
	var list []jsonCookie
	if err := json.Unmarshal(data, &list); err != nil {
		return 0, fmt.Errorf("cookies: invalid JSON export: %w", err)
	}
	byHost := map[string][]*http.Cookie{}
	for _, jc := range list {
		host := strings.TrimPrefix(jc.Domain, ".")
		if host == "" || jc.Name == "" {
			continue
		}
		c := &http.Cookie{Name: jc.Name, Value: jc.Value, Path: jc.Path, Secure: jc.Secure, HttpOnly: jc.HTTPOnly}
		if c.Path == "" {
			c.Path = "/"
		}
		if !jc.HostOnly {
			c.Domain = "." + host
		}
		if !jc.Session && jc.ExpirationDate > 0 {
			c.Expires = time.Unix(int64(jc.ExpirationDate), 0)
			if c.Expires.Before(time.Now()) {
				continue
			}
		}
		byHost[host] = append(byHost[host], c)
	}
	return setCookies(client, byHost), nil
}

func setCookies(client *http.Client, byHost map[string][]*http.Cookie) int {
	n := 0
	for host, cs := range byHost {
		client.Jar.SetCookies(&url.URL{Scheme: "https", Host: host, Path: "/"}, cs)
		n += len(cs)
	}
	return n
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
	return setCookies(client, byHost), nil
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
