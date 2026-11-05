package extractor

import (
	"bufio"
	"io"
	"os"
	"strings"
)

// Login — учётные данные для входа на сайт.
type Login struct {
	Username  string
	Password  string
	TwoFactor string
}

// CredStore выдаёт учётные данные экстракторам: заданные явно (Options) или из
// файла .netrc. Экстрактор запрашивает их по «машине» — обычно своему ключу
// (ie_key) или хосту сайта.
type CredStore struct {
	// Default — учётка по умолчанию (из Options.Username/Password), если в
	// .netrc нет записи для машины.
	Default   Login
	byMachine map[string]Login
}

// NewCredStore создаёт хранилище с записями по машинам (например, из netrc).
func NewCredStore(byMachine map[string]Login) *CredStore {
	if byMachine == nil {
		byMachine = map[string]Login{}
	}
	return &CredStore{byMachine: byMachine}
}

// For возвращает учётные данные для машины. Сначала ищется точное совпадение
// по имени машины, затем запись default/Default. ok=false — ничего нет.
func (c *CredStore) For(machine string) (Login, bool) {
	if c == nil {
		return Login{}, false
	}
	machine = strings.ToLower(machine)
	if l, ok := c.byMachine[machine]; ok {
		return l, true
	}
	if c.Default.Username != "" || c.Default.Password != "" {
		return c.Default, true
	}
	if l, ok := c.byMachine["default"]; ok {
		return l, true
	}
	return Login{}, false
}

// Credentials — учётные данные для машины (обёртка над Env.Creds).
func (e *Env) Credentials(machine string) (Login, bool) {
	return e.Creds.For(machine)
}

// ParseNetrc разбирает файл формата netrc. Поддерживаются токены machine,
// login, password, account (→ TwoFactor), default и однострочные macdef
// (пропускаются). Возвращает записи по имени машины (в нижнем регистре,
// «default» — запись default).
func ParseNetrc(r io.Reader) (map[string]Login, error) {
	out := map[string]Login{}
	toks := netrcTokens(r)
	var machine string
	var cur Login
	flush := func() {
		if machine != "" {
			out[strings.ToLower(machine)] = cur
		}
		machine, cur = "", Login{}
	}
	for i := 0; i < len(toks); i++ {
		switch toks[i] {
		case "machine":
			flush()
			if i+1 < len(toks) {
				i++
				machine = toks[i]
			}
		case "default":
			flush()
			machine = "default"
		case "login":
			if i+1 < len(toks) {
				i++
				cur.Username = toks[i]
			}
		case "password":
			if i+1 < len(toks) {
				i++
				cur.Password = toks[i]
			}
		case "account":
			if i+1 < len(toks) {
				i++
				cur.TwoFactor = toks[i]
			}
		case "macdef":
			// macdef NAME ... пустая строка — пропускаем до разделителя.
			i++ // имя макроса
		}
	}
	flush()
	return out, nil
}

// netrcTokens разбивает netrc на токены по пробелам/переводам строк.
func netrcTokens(r io.Reader) []string {
	var toks []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		toks = append(toks, strings.Fields(line)...)
	}
	return toks
}

// LoadNetrc читает и разбирает файл netrc по пути.
func LoadNetrc(path string) (map[string]Login, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseNetrc(f)
}

// DefaultNetrcPath — стандартный путь к файлу netrc ($HOME/.netrc).
func DefaultNetrcPath() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h + "/.netrc"
	}
	return ""
}
