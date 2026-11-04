package script

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// DefaultPackURL — пакет экстракторов go-dlp (rolling-релиз «extractors»).
// Подпись лежит рядом: DefaultPackURL + ".sig".
const DefaultPackURL = "https://github.com/Disaldr/go-dlp/releases/download/extractors/pack.json"

// DefaultPublicKey — ключ, которым подписываются официальные пакеты.
var DefaultPublicKey = mustKey("DujO9ermGnLVUNC7TJ2tdt0dGMeq4PG5Ht4aEdFwA9o=")

func mustKey(b64 string) ed25519.PublicKey {
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(b) != ed25519.PublicKeySize {
		panic("script: bad public key")
	}
	return b
}

// maxPackSize — предел размера pack.json.
const maxPackSize = 16 << 20

// Updater скачивает подписанные пакеты и устанавливает их через Manager.
type Updater struct {
	// URL пакета; подпись — URL + ".sig". Пусто — DefaultPackURL.
	URL string
	// PublicKey — ключ проверки подписи. nil — DefaultPublicKey.
	PublicKey ed25519.PublicKey
	// CacheDir — каталог кеша (последний проверенный пакет); пусто — без кеша.
	CacheDir string
	// HTTP — клиент; nil — http.DefaultClient.
	HTTP   *http.Client
	Logger *slog.Logger

	etag string
}

func (u *Updater) url() string {
	if u.URL != "" {
		return u.URL
	}
	return DefaultPackURL
}

func (u *Updater) key() ed25519.PublicKey {
	if u.PublicKey != nil {
		return u.PublicKey
	}
	return DefaultPublicKey
}

func (u *Updater) log() *slog.Logger {
	if u.Logger != nil {
		return u.Logger
	}
	return slog.New(slog.DiscardHandler)
}

// LoadCache устанавливает пакет из кеша, если он есть, подписан и новее
// установленного. Подпись проверяется и здесь: кеш мог быть подменён.
func (u *Updater) LoadCache(m *Manager) (bool, error) {
	if u.CacheDir == "" {
		return false, nil
	}
	data, err := os.ReadFile(filepath.Join(u.CacheDir, "pack.json"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	sig, err := os.ReadFile(filepath.Join(u.CacheDir, "pack.json.sig"))
	if err != nil {
		return false, err
	}
	p, err := ParsePack(data, sig, u.key())
	if err != nil {
		return false, fmt.Errorf("cached extractor pack: %w", err)
	}
	return m.Install(p)
}

// Check скачивает пакет и устанавливает его, если он новее текущего.
func (u *Updater) Check(ctx context.Context, m *Manager) (bool, error) {
	data, status, etag, err := u.get(ctx, u.url(), u.etag)
	if err != nil {
		return false, err
	}
	if status == http.StatusNotModified {
		return false, nil
	}
	sig, _, _, err := u.get(ctx, u.url()+".sig", "")
	if err != nil {
		return false, err
	}
	p, err := ParsePack(data, sig, u.key())
	if err != nil {
		return false, err
	}
	u.etag = etag
	installed, err := m.Install(p)
	if err != nil || !installed {
		return installed, err
	}
	u.log().Info("extractor pack installed", "version", p.Version, "extractors", len(p.Extractors))
	if u.CacheDir != "" {
		if err := u.writeCache(p); err != nil {
			u.log().Warn("extractor pack: cache write failed", "err", err)
		}
	}
	return true, nil
}

// Run: кеш, затем проверка каждые interval до отмены ctx (interval <= 0 —
// однократно). Ошибки сети не прерывают цикл, а пишутся в лог.
func (u *Updater) Run(ctx context.Context, m *Manager, interval time.Duration) error {
	if _, err := u.LoadCache(m); err != nil {
		u.log().Warn("extractor pack: cache", "err", err)
	}
	for {
		if _, err := u.Check(ctx, m); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			u.log().Warn("extractor pack: update check failed", "err", err)
		}
		if interval <= 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func (u *Updater) get(ctx context.Context, url, etag string) ([]byte, int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, "", err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	hc := u.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil, resp.StatusCode, etag, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, "", fmt.Errorf("extractor pack: GET %s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxPackSize+1))
	if err != nil {
		return nil, 0, "", err
	}
	if len(b) > maxPackSize {
		return nil, 0, "", fmt.Errorf("extractor pack: %s is larger than %d bytes", url, maxPackSize)
	}
	return b, resp.StatusCode, resp.Header.Get("ETag"), nil
}

func (u *Updater) writeCache(p *Pack) error {
	if err := os.MkdirAll(u.CacheDir, 0o755); err != nil {
		return err
	}
	// Сначала подпись, затем пакет: при сбое между записями LoadCache
	// увидит несовпадение и проигнорирует кеш, а не сломается.
	for _, f := range []struct {
		name string
		data []byte
	}{{"pack.json.sig", p.Sig}, {"pack.json", p.Raw}} {
		tmp := filepath.Join(u.CacheDir, f.name+".tmp")
		if err := os.WriteFile(tmp, f.data, 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, filepath.Join(u.CacheDir, f.name)); err != nil {
			return err
		}
	}
	return nil
}
