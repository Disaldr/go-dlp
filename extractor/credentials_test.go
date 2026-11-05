package extractor

import (
	"strings"
	"testing"
)

func TestParseNetrc(t *testing.T) {
	const src = `
# комментарий
machine youtube.com login alice password s3cret account 123456
machine vimeo.com
  login bob
  password hunter2
default login guest password none
`
	m, err := ParseNetrc(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if l := m["youtube.com"]; l.Username != "alice" || l.Password != "s3cret" || l.TwoFactor != "123456" {
		t.Errorf("youtube.com = %+v", l)
	}
	if l := m["vimeo.com"]; l.Username != "bob" || l.Password != "hunter2" {
		t.Errorf("vimeo.com = %+v (multiline)", l)
	}
	if l := m["default"]; l.Username != "guest" {
		t.Errorf("default = %+v", l)
	}
}

func TestCredStorePrecedence(t *testing.T) {
	store := NewCredStore(map[string]Login{
		"youtube.com": {Username: "fromNetrc", Password: "p1"},
		"default":     {Username: "def", Password: "p0"},
	})

	// Точное совпадение по машине.
	if l, ok := store.For("youtube.com"); !ok || l.Username != "fromNetrc" {
		t.Errorf("machine match: %+v %v", l, ok)
	}
	// Нет машины, нет Default → запись default.
	if l, ok := store.For("other.com"); !ok || l.Username != "def" {
		t.Errorf("default fallback: %+v %v", l, ok)
	}

	// Явный Default приоритетнее netrc default (но не точного совпадения).
	store.Default = Login{Username: "explicit", Password: "px"}
	if l, _ := store.For("other.com"); l.Username != "explicit" {
		t.Errorf("explicit Default should win over netrc default, got %+v", l)
	}
	if l, _ := store.For("youtube.com"); l.Username != "fromNetrc" {
		t.Errorf("exact machine must still win over Default, got %+v", l)
	}

	// nil-хранилище безопасно.
	var nilStore *CredStore
	if _, ok := nilStore.For("x"); ok {
		t.Error("nil store should return ok=false")
	}

	// Env.Credentials делегирует в Creds.
	env := &Env{Creds: store}
	if l, ok := env.Credentials("youtube.com"); !ok || l.Username != "fromNetrc" {
		t.Errorf("Env.Credentials = %+v %v", l, ok)
	}
}
