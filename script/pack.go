package script

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// PackFormat — версия формата файла пакета.
const PackFormat = 1

// packFile — содержимое pack.json. Подпись (pack.json.sig) — ed25519 над
// точными байтами pack.json, в base64.
type packFile struct {
	Format  int               `json:"format"`
	Version int64             `json:"version"`
	HostAPI int               `json:"host_api"`
	Created string            `json:"created,omitempty"`
	Scripts map[string]string `json:"scripts"`
}

// Pack — проверенный и скомпилированный пакет скриптов.
type Pack struct {
	Version    int64
	HostAPI    int
	Extractors []*Extractor
	// Raw и Sig — исходные байты (для кеша на диске).
	Raw, Sig []byte
}

// ErrSignature — подпись пакета не сходится с ключом.
var ErrSignature = errors.New("extractor pack: bad signature")

// ParsePack проверяет подпись (sig — base64 или 64 «сырых» байта) и
// компилирует скрипты пакета.
func ParsePack(data, sig []byte, pub ed25519.PublicKey) (*Pack, error) {
	if len(pub) != ed25519.PublicKeySize {
		return nil, errors.New("extractor pack: public key required")
	}
	raw := sig
	if len(raw) != ed25519.SignatureSize {
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
		if err != nil {
			return nil, ErrSignature
		}
		raw = b
	}
	if !ed25519.Verify(pub, data, raw) {
		return nil, ErrSignature
	}
	p, err := parseUnsigned(data)
	if err != nil {
		return nil, err
	}
	p.Sig = sig
	return p, nil
}

func parseUnsigned(data []byte) (*Pack, error) {
	var pf packFile
	if err := json.Unmarshal(data, &pf); err != nil {
		return nil, fmt.Errorf("extractor pack: %w", err)
	}
	if pf.Format != PackFormat {
		return nil, fmt.Errorf("extractor pack: unsupported format %d", pf.Format)
	}
	if pf.HostAPI > HostAPI {
		return nil, fmt.Errorf("extractor pack v%d needs host API %d, have %d (update the application)", pf.Version, pf.HostAPI, HostAPI)
	}
	exts, err := compileAll(pf.Scripts)
	if err != nil {
		return nil, err
	}
	return &Pack{Version: pf.Version, HostAPI: pf.HostAPI, Extractors: exts, Raw: data}, nil
}

func compileAll(scripts map[string]string) ([]*Extractor, error) {
	names := make([]string, 0, len(scripts))
	for n := range scripts {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []*Extractor
	seen := map[string]string{}
	for _, n := range names {
		exts, err := Compile(n, scripts[n])
		if err != nil {
			return nil, err
		}
		for _, e := range exts {
			k := strings.ToLower(e.name)
			if f, dup := seen[k]; dup {
				return nil, fmt.Errorf("extractor %q defined in both %s and %s", e.name, f, n)
			}
			seen[k] = n
			out = append(out, e)
		}
	}
	return out, nil
}

// BuildPack собирает pack.json из каталога: все *.js и файл VERSION
// (целое число, растёт с каждой публикацией).
func BuildPack(dir fs.FS, hostAPI int) ([]byte, int64, error) {
	scripts, version, err := readDir(dir)
	if err != nil {
		return nil, 0, err
	}
	if _, err := compileAll(scripts); err != nil {
		return nil, 0, err
	}
	data, err := json.MarshalIndent(packFile{
		Format: PackFormat, Version: version, HostAPI: hostAPI,
		Created: time.Now().UTC().Format(time.RFC3339), Scripts: scripts,
	}, "", " ")
	return data, version, err
}

// SignPack возвращает подпись pack.json в base64.
func SignPack(data []byte, priv ed25519.PrivateKey) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data)) + "\n")
}

// LoadDir компилирует скрипты каталога без подписи — для встроенных
// скриптов и локальной разработки (--extractor-scripts).
func LoadDir(dir fs.FS) (*Pack, error) {
	scripts, version, err := readDir(dir)
	if err != nil {
		return nil, err
	}
	exts, err := compileAll(scripts)
	if err != nil {
		return nil, err
	}
	return &Pack{Version: version, HostAPI: HostAPI, Extractors: exts}, nil
}

func readDir(dir fs.FS) (map[string]string, int64, error) {
	entries, err := fs.ReadDir(dir, ".")
	if err != nil {
		return nil, 0, err
	}
	scripts := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".js" {
			continue
		}
		b, err := fs.ReadFile(dir, e.Name())
		if err != nil {
			return nil, 0, err
		}
		scripts[e.Name()] = string(b)
	}
	var version int64
	if b, err := fs.ReadFile(dir, "VERSION"); err == nil {
		if version, err = strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err != nil {
			return nil, 0, fmt.Errorf("VERSION: %w", err)
		}
	}
	return scripts, version, nil
}
