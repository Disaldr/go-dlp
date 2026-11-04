// Package extractortest — хелперы для тестов экстракторов (аналог _TESTS
// в yt-dlp): проверка сопоставления URL и онлайн-проверки результата.
//
// Онлайн-тесты ходят в сеть и запускаются только при GODLP_ONLINE=1:
//
//	GODLP_ONLINE=1 go test ./extractors/...
package extractortest

import (
	"context"
	"os"
	"regexp"
	"testing"
	"time"

	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/media"
)

// Match — ожидаемое сопоставление: {URL, имя экстрактора}; "" — не должен
// подойти никто, кроме fallback-экстракторов.
type Match [2]string

// CheckMatches проверяет, какой экстрактор реестра выбирается для URL.
// Fallback-экстракторы (generic) при этом не учитываются.
func CheckMatches(t *testing.T, reg *ex.Registry, cases []Match) {
	t.Helper()
	for _, m := range cases {
		c := struct{ URL, Want string }{m[0], m[1]}
		got := ""
		for _, e := range reg.List() {
			if ex.PriorityOf(e) <= ex.PriorityFallback {
				continue
			}
			if e.Suitable(c.URL) {
				got = e.Name()
				break
			}
		}
		if got != c.Want {
			t.Errorf("%s: matched %q, want %q", c.URL, got, c.Want)
		}
	}
}

// Online пропускает тест, если не задан GODLP_ONLINE=1.
func Online(t *testing.T) {
	t.Helper()
	if os.Getenv("GODLP_ONLINE") != "1" {
		t.Skip("online test; set GODLP_ONLINE=1 to run")
	}
}

// Expect — ожидания к результату экстракции. Пустые поля не проверяются;
// строковые поля с префиксом "re:" сравниваются как регулярные выражения.
type Expect struct {
	ID, Title, Uploader string
	Kind                media.Kind
	MinFormats          int
	MinEntries          int
	MinDuration         float64
}

// Extract выполняет экстракцию с таймаутом и проверяет ожидания.
func Extract(t *testing.T, e ex.Extractor, rawURL string, want Expect) *media.Info {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	env := ex.NewEnv()
	env.Registry = ex.NewRegistry(e)
	info, err := e.Extract(ctx, env, rawURL)
	if err != nil {
		t.Fatalf("Extract(%s): %v", rawURL, err)
	}
	check := func(field, got, want string) {
		if want == "" {
			return
		}
		if re, ok := cutRe(want); ok {
			if !re.MatchString(got) {
				t.Errorf("%s = %q, want match %s", field, got, want)
			}
			return
		}
		if got != want {
			t.Errorf("%s = %q, want %q", field, got, want)
		}
	}
	check("id", info.ID, want.ID)
	check("title", info.Title, want.Title)
	check("uploader", info.Uploader, want.Uploader)
	if want.Kind != "" && info.Kind != want.Kind {
		t.Errorf("kind = %q, want %q", info.Kind, want.Kind)
	}
	if len(info.Formats) < want.MinFormats {
		t.Errorf("formats = %d, want >= %d", len(info.Formats), want.MinFormats)
	}
	if len(info.Entries) < want.MinEntries {
		t.Errorf("entries = %d, want >= %d", len(info.Entries), want.MinEntries)
	}
	if info.Duration < want.MinDuration {
		t.Errorf("duration = %v, want >= %v", info.Duration, want.MinDuration)
	}
	for _, f := range info.Formats {
		if f.URL == "" || f.FormatID == "" {
			t.Errorf("format without url/id: %+v", f)
		}
	}
	return info
}

func cutRe(s string) (*regexp.Regexp, bool) {
	if len(s) > 3 && s[:3] == "re:" {
		return regexp.MustCompile(s[3:]), true
	}
	return nil, false
}
