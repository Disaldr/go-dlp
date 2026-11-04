package extractor_test

import (
	"context"
	"strings"
	"testing"
	"time"

	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/media"
)

func fake(name string, prio int, prefix string) ex.Extractor {
	return &ex.Func{
		Key:   name,
		Prio:  prio,
		Match: func(u string) bool { return strings.HasPrefix(u, prefix) },
		ExtractF: func(context.Context, *ex.Env, string) (*media.Info, error) {
			return &media.Info{ID: name}, nil
		},
	}
}

func TestRegistryPriorityAndFind(t *testing.T) {
	r := ex.NewRegistry(
		fake("generic", ex.PriorityFallback, "http"),
		fake("site", 0, "https://site"),
		fake("site-tab", 10, "https://site/list"),
	)
	cases := map[string]string{
		"https://site/video":  "site",
		"https://site/list/1": "site-tab",
		"http://other":        "generic",
	}
	for u, want := range cases {
		e, err := r.Find(u)
		if err != nil || e.Name() != want {
			t.Errorf("Find(%q) = %v, %v; want %s", u, e, err, want)
		}
	}
	if _, err := r.Find("ftp://x"); err == nil {
		t.Error("expected ErrUnsupportedURL")
	}
}

func TestRegistryWatchAndSubscribe(t *testing.T) {
	r := ex.NewRegistry(fake("generic", ex.PriorityFallback, "http"))
	events, unsubscribe := r.Subscribe(8)
	defer unsubscribe()

	updates := make(chan ex.Update)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Watch(ctx, updates, nil) }()

	updates <- ex.Add(fake("site", 0, "https://site"))
	expectEvent(t, events, ex.EventAdded, "site")
	if e, _ := r.Find("https://site/v"); e.Name() != "site" {
		t.Fatalf("new extractor not used: %s", e.Name())
	}

	// горячая замена: то же имя, другой шаблон
	updates <- ex.Add(fake("site", 0, "https://new-site"))
	expectEvent(t, events, ex.EventReplaced, "site")
	if e, _ := r.Find("https://site/v"); e.Name() != "generic" {
		t.Fatalf("replacement not applied, got %s", e.Name())
	}

	updates <- ex.Remove("site")
	expectEvent(t, events, ex.EventRemoved, "site")
	if _, ok := r.Get("site"); ok {
		t.Fatal("extractor not removed")
	}

	close(updates)
	if err := <-done; err != nil {
		t.Fatalf("Watch returned %v", err)
	}
	if v := r.Version(); v != 4 {
		t.Errorf("version = %d, want 4", v)
	}
}

func expectEvent(t *testing.T, ch <-chan ex.Event, typ ex.EventType, name string) {
	t.Helper()
	select {
	case ev := <-ch:
		if ev.Type != typ || ev.Name != name {
			t.Fatalf("event = %+v, want %v %s", ev, typ, name)
		}
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for %v %s", typ, name)
	}
}

func TestTraverse(t *testing.T) {
	v := map[string]any{"a": []any{map[string]any{"b": "1"}, map[string]any{"b": 2.0}}}
	if ex.Str(v, "a", 0, "b") != "1" || ex.Int(v, "a", -1, "b") != 2 || ex.Int(v, "a", 0, "b") != 1 {
		t.Fatal("traverse failed")
	}
	if ex.Get(v, "a", 5) != nil {
		t.Fatal("out of range must be nil")
	}
	if n := len(ex.FindAll(v, "b")); n != 2 {
		t.Fatalf("FindAll = %d", n)
	}
}

func TestJSONAfter(t *testing.T) {
	page := `<script>var ytInitialData = {"x":{"y":"};</script>"}};var other=1;</script>`
	v, ok := ex.JSONAfter(page, "var ytInitialData = ")
	if !ok || ex.Str(v, "x", "y") != "};</script>" {
		t.Fatalf("JSONAfter = %v %v", v, ok)
	}
}

func TestParseDuration(t *testing.T) {
	for in, want := range map[string]float64{"1:02:03": 3723, "PT1H2M3S": 3723, "62": 62, "PT45S": 45} {
		if got := ex.ParseDuration(in); got != want {
			t.Errorf("ParseDuration(%q) = %v, want %v", in, got, want)
		}
	}
}
