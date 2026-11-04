// Command go-dlp-pack собирает и подписывает пакет JS-экстракторов.
//
//	go run ./cmd/go-dlp-pack            # script/scripts → dist/pack.json(.sig)
//	gh release upload extractors dist/pack.json dist/pack.json.sig --clobber
//
// Перед публикацией увеличьте script/scripts/VERSION: приложения ставят
// пакет, только если его версия больше установленной.
package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Disaldr/go-dlp/script"
)

func main() {
	home, _ := os.UserHomeDir()
	dir := flag.String("dir", "script/scripts", "каталог со скриптами и VERSION")
	keyPath := flag.String("key", filepath.Join(home, ".config", "go-dlp", "pack-signing.key"), "закрытый ключ ed25519 (seed в base64)")
	out := flag.String("out", "dist", "каталог результата")
	flag.Parse()
	if err := run(*dir, *keyPath, *out); err != nil {
		fmt.Fprintln(os.Stderr, "go-dlp-pack:", err)
		os.Exit(1)
	}
}

func run(dir, keyPath, out string) error {
	kb, err := os.ReadFile(keyPath)
	if err != nil {
		return err
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(kb)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return fmt.Errorf("%s: bad key", keyPath)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	if !bytes.Equal(priv.Public().(ed25519.PublicKey), script.DefaultPublicKey) {
		return fmt.Errorf("key does not match script.DefaultPublicKey; apps would reject this pack")
	}
	data, version, err := script.BuildPack(os.DirFS(dir), script.HostAPI)
	if err != nil {
		return err
	}
	sig := script.SignPack(data, priv)
	p, err := script.ParsePack(data, sig, script.DefaultPublicKey)
	if err != nil {
		return fmt.Errorf("self-check: %w", err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "pack.json"), data, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "pack.json.sig"), sig, 0o644); err != nil {
		return err
	}
	names := make([]string, 0, len(p.Extractors))
	for _, e := range p.Extractors {
		names = append(names, e.Name())
	}
	fmt.Printf("pack v%d (host API %d): %s\n", version, script.HostAPI, strings.Join(names, ", "))
	fmt.Printf("publish: gh release upload extractors %s %s --clobber\n",
		filepath.Join(out, "pack.json"), filepath.Join(out, "pack.json.sig"))
	return nil
}
