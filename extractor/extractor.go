// Package extractor содержит контракт экстрактора, реестр экстракторов
// с реактивными обновлениями через каналы и вспомогательные функции
// для написания собственных экстракторов.
package extractor

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/Disaldr/go-dlp/media"
)

// Extractor — минимальный контракт экстрактора.
type Extractor interface {
	// Name — уникальный ключ экстрактора (ie_key), например "youtube".
	Name() string
	// Suitable сообщает, умеет ли экстрактор обрабатывать URL.
	Suitable(rawURL string) bool
	// Extract извлекает информацию о медиа.
	Extract(ctx context.Context, env *Env, rawURL string) (*media.Info, error)
}

// Prioritized — необязательный интерфейс: экстракторы с большим приоритетом
// проверяются раньше. По умолчанию приоритет 0, generic использует PriorityFallback.
type Prioritized interface {
	Priority() int
}

// Describer — необязательное описание экстрактора.
type Describer interface {
	Description() string
}

// PriorityFallback — приоритет экстракторов «последней надежды».
const PriorityFallback = -1000

// PriorityOf возвращает приоритет экстрактора.
func PriorityOf(e Extractor) int {
	if p, ok := e.(Prioritized); ok {
		return p.Priority()
	}
	return 0
}

// Ошибки-категории; проверяются через errors.Is.
var (
	// ErrUnsupportedURL — экстрактор не умеет обрабатывать URL. Если его вернул
	// Extract, клиент передаёт URL следующему подходящему экстрактору.
	ErrUnsupportedURL = errors.New("unsupported URL")
	ErrUnavailable    = errors.New("video unavailable")
	ErrLoginRequired  = errors.New("login required")
	ErrGeoRestricted  = errors.New("geo restricted")
	ErrNoFormats      = errors.New("no formats found")
)

// Error — ошибка экстракции.
type Error struct {
	Extractor string
	ID        string
	Msg       string
	// Kind — одна из ошибок-категорий (может быть nil).
	Kind  error
	Cause error
}

func (e *Error) Error() string {
	s := "[" + e.Extractor + "]"
	if e.ID != "" {
		s += " " + e.ID + ":"
	}
	s += " " + e.Msg
	if e.Cause != nil {
		s += ": " + e.Cause.Error()
	}
	return s
}

// Unwrap позволяет errors.Is/As видеть категорию и причину.
func (e *Error) Unwrap() []error {
	var errs []error
	if e.Kind != nil {
		errs = append(errs, e.Kind)
	}
	if e.Cause != nil {
		errs = append(errs, e.Cause)
	}
	return errs
}

// Errorf создаёт ошибку экстрактора.
func Errorf(ie, id string, kind error, format string, args ...any) *Error {
	return &Error{Extractor: ie, ID: id, Kind: kind, Msg: fmt.Sprintf(format, args...)}
}

// Wrap оборачивает причину.
func Wrap(ie, id string, cause error, format string, args ...any) *Error {
	return &Error{Extractor: ie, ID: id, Cause: cause, Msg: fmt.Sprintf(format, args...)}
}

// Matcher — набор регулярных выражений для Suitable (аналог _VALID_URL).
type Matcher []*regexp.Regexp

// MustMatcher компилирует шаблоны; паникует при ошибке.
func MustMatcher(patterns ...string) Matcher {
	m := make(Matcher, 0, len(patterns))
	for _, p := range patterns {
		m = append(m, regexp.MustCompile(p))
	}
	return m
}

// Match сообщает, подходит ли URL хотя бы под один шаблон.
func (m Matcher) Match(u string) bool {
	for _, re := range m {
		if re.MatchString(u) {
			return true
		}
	}
	return false
}

// Groups возвращает именованные группы первого совпавшего шаблона.
func (m Matcher) Groups(u string) (map[string]string, bool) {
	for _, re := range m {
		sm := re.FindStringSubmatch(u)
		if sm == nil {
			continue
		}
		g := make(map[string]string)
		for i, name := range re.SubexpNames() {
			if name != "" && sm[i] != "" {
				g[name] = sm[i]
			}
		}
		return g, true
	}
	return nil, false
}

// Func — экстрактор из функций; удобно для плагинов и тестов.
type Func struct {
	Key      string
	Desc     string
	Prio     int
	Match    func(string) bool
	ExtractF func(ctx context.Context, env *Env, rawURL string) (*media.Info, error)
}

func (f *Func) Name() string           { return f.Key }
func (f *Func) Description() string    { return f.Desc }
func (f *Func) Priority() int          { return f.Prio }
func (f *Func) Suitable(u string) bool { return f.Match != nil && f.Match(u) }
func (f *Func) Extract(ctx context.Context, env *Env, u string) (*media.Info, error) {
	return f.ExtractF(ctx, env, u)
}
