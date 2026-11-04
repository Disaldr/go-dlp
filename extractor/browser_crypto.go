package extractor

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/pbkdf2"
)

// Chromium's OS crypt formats are documented in components/os_crypt/sync:
// https://chromium.googlesource.com/chromium/src/+/refs/heads/main/components/os_crypt/sync/
// Database v24 prepends SHA-256(host_key) before encrypting cookie values.
type chromiumDecryptor struct {
	platform   string
	password   func() ([]byte, error)
	windowsKey func() ([]byte, error)
	key        []byte
}

func (d *chromiumDecryptor) decrypt(data []byte, host string, version int) ([]byte, error) {
	if bytes.HasPrefix(data, []byte("v20")) {
		return nil, errors.New("App-Bound Encryption (v20) is not supported; use Firefox or an exported --cookies file")
	}
	prefix := ""
	if len(data) >= 3 {
		prefix = string(data[:3])
	}
	var plaintext []byte
	var err error
	switch d.platform {
	case "windows":
		if prefix != "v10" && prefix != "v11" {
			if len(data) > 0 && data[0] == 'v' {
				return nil, errors.New("unsupported Chromium cookie encryption version")
			}
			return unprotectBrowserData(data)
		}
		if d.key == nil {
			d.key, err = d.windowsKey()
			if err != nil {
				return nil, err
			}
		}
		block, err := aes.NewCipher(d.key)
		if err != nil {
			return nil, errors.New("invalid Chromium AES key")
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		if len(data) < 3+gcm.NonceSize()+gcm.Overhead() {
			return nil, errors.New("truncated AES-GCM cookie")
		}
		plaintext, err = gcm.Open(nil, data[3:3+gcm.NonceSize()], data[3+gcm.NonceSize():], nil)
	case "darwin", "linux":
		if prefix != "v10" && prefix != "v11" {
			return nil, errors.New("unsupported Chromium cookie encryption version")
		}
		iterations := 1
		var key []byte
		if d.platform == "linux" && prefix == "v10" {
			key = pbkdf2.Key([]byte("peanuts"), []byte("saltysalt"), 1, 16, sha1.New)
		} else {
			if d.platform == "darwin" {
				if prefix != "v10" {
					return nil, errors.New("unsupported macOS cookie encryption version")
				}
				iterations = 1003
			}
			if d.key == nil {
				password, err := d.password()
				if err != nil {
					return nil, err
				}
				d.key = pbkdf2.Key(password, []byte("saltysalt"), iterations, 16, sha1.New)
			}
			key = d.key
		}
		plaintext, err = decryptBrowserCBC(data[3:], key)
		if err == nil {
			plaintext, err = validateBrowserPlaintext(plaintext, host, version)
		}
		if err != nil && d.platform == "linux" {
			// Older Linux Chromium versions sometimes used the empty password.
			emptyKey := pbkdf2.Key(nil, []byte("saltysalt"), 1, 16, sha1.New)
			plaintext, err = decryptBrowserCBC(data[3:], emptyKey)
			if err == nil {
				plaintext, err = validateBrowserPlaintext(plaintext, host, version)
			}
		}
		return plaintext, err
	default:
		return nil, fmt.Errorf("Chromium cookie decryption is not supported on %s", d.platform)
	}
	if err != nil {
		return nil, errors.New("cookie decryption failed (wrong OS account or encryption key)")
	}
	return validateBrowserPlaintext(plaintext, host, version)
}

func decryptBrowserCBC(data, key []byte) ([]byte, error) {
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("invalid AES-CBC cookie length")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	plaintext := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, bytes.Repeat([]byte(" "), aes.BlockSize)).CryptBlocks(plaintext, data)
	padding := int(plaintext[len(plaintext)-1])
	if padding == 0 || padding > aes.BlockSize || !bytes.Equal(plaintext[len(plaintext)-padding:], bytes.Repeat([]byte{byte(padding)}, padding)) {
		return nil, errors.New("cookie decryption failed (invalid padding)")
	}
	return plaintext[:len(plaintext)-padding], nil
}

func validateBrowserPlaintext(data []byte, host string, version int) ([]byte, error) {
	if version >= 24 {
		hash := sha256.Sum256([]byte(host))
		if len(data) < len(hash) || !bytes.Equal(data[:len(hash)], hash[:]) {
			return nil, errors.New("cookie decryption failed (domain hash mismatch)")
		}
		data = data[len(hash):]
	}
	if !utf8.Valid(data) {
		return nil, errors.New("cookie decryption failed (invalid UTF-8)")
	}
	return data, nil
}

func chromiumKeyringName(browser, platform string) string {
	name := map[string]string{
		"chrome": "Chrome", "chromium": "Chromium", "brave": "Brave", "whale": "Whale",
		"edge": "Chromium", "opera": "Chromium", "vivaldi": "Chrome",
	}[browser]
	if platform == "darwin" {
		switch browser {
		case "edge":
			name = "Microsoft Edge"
		case "opera":
			name = "Opera"
		case "vivaldi":
			name = "Vivaldi"
		}
	}
	return name
}

func chromiumPassword(ctx context.Context, browser string) ([]byte, error) {
	name := chromiumKeyringName(browser, runtime.GOOS)
	if runtime.GOOS == "darwin" {
		password, err := browserSecretCommand(ctx, "security", "find-generic-password", "-w", "-a", name, "-s", name+" Safe Storage")
		if err != nil {
			return nil, errors.New("cannot read macOS Keychain; allow access to the browser's Safe Storage item, or use an exported --cookies file")
		}
		return password, nil
	}
	if strings.Contains(strings.ToUpper(os.Getenv("XDG_CURRENT_DESKTOP")), "KDE") || os.Getenv("KDE_SESSION_VERSION") != "" {
		daemon := "kwalletd"
		switch os.Getenv("KDE_SESSION_VERSION") {
		case "5":
			daemon = "kwalletd5"
		case "6":
			daemon = "kwalletd6"
		}
		wallet := "kdewallet"
		if output, err := browserSecretCommand(ctx, "dbus-send", "--session", "--print-reply=literal", "--dest=org.kde."+daemon, "/modules/"+daemon, "org.kde.KWallet.networkWallet"); err == nil {
			if value := strings.TrimSpace(string(output)); value != "" {
				wallet = value
			}
		}
		password, err := browserSecretCommand(ctx, "kwallet-query", "--read-password", name+" Safe Storage", "--folder", name+" Keys", wallet)
		if err != nil || bytes.HasPrefix(bytes.ToLower(password), []byte("failed to read")) {
			return nil, errors.New("cannot read KWallet; install kwallet-query and unlock the browser's wallet, or use --cookies")
		}
		return password, nil
	}
	password, err := browserSecretCommand(ctx, "secret-tool", "lookup", "application", strings.ToLower(name))
	if err != nil {
		return nil, errors.New("cannot read Secret Service keyring; install secret-tool (libsecret) and unlock the login keyring, or use --cookies")
	}
	return password, nil
}

func browserSecretCommand(ctx context.Context, command string, args ...string) ([]byte, error) {
	// Never put a key or cookie value in command arguments, errors or logs.
	output, err := exec.CommandContext(ctx, command, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("%s failed", command)
	}
	return bytes.TrimSuffix(output, []byte("\n")), nil
}

func chromiumWindowsKey(cookiePath string) ([]byte, error) {
	dir := filepath.Dir(cookiePath)
	for range 4 {
		data, err := os.ReadFile(filepath.Join(dir, "Local State"))
		if err == nil {
			var state struct {
				OSCrypt struct {
					EncryptedKey string `json:"encrypted_key"`
				} `json:"os_crypt"`
			}
			if json.Unmarshal(data, &state) != nil {
				return nil, errors.New("invalid Chromium Local State file")
			}
			key, err := base64.StdEncoding.DecodeString(state.OSCrypt.EncryptedKey)
			if err != nil || !bytes.HasPrefix(key, []byte("DPAPI")) {
				return nil, errors.New("Local State has no supported DPAPI encryption key")
			}
			return unprotectBrowserData(key[5:])
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read Chromium Local State: %w", err)
		}
		dir = filepath.Dir(dir)
	}
	return nil, errors.New("cannot find Chromium Local State file above the cookies database")
}
