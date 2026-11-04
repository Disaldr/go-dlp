package youtube

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ex "github.com/Disaldr/go-dlp/extractor"
)

func setTimeouts(t *testing.T, load, call time.Duration) {
	t.Helper()
	ol, oc := jsLoadTimeout, jsCallTimeout
	jsLoadTimeout, jsCallTimeout = load, call
	t.Cleanup(func() { jsLoadTimeout, jsCallTimeout = ol, oc })
}

// Зациклившаяся n-функция прерывается по таймауту, после чего плеер
// отключается и следующие вызовы не тратят время.
func TestCallTimeoutDisablesPlayer(t *testing.T) {
	setTimeouts(t, time.Second, 100*time.Millisecond)
	code := strings.Replace(miniPlayer, `var b=a.split(""),c=0;b.reverse();return b.join("")`, `var b=a.split(""),c=0;for(;;){}`, 1)
	p, err := newPlayer(context.Background(), code)
	if err != nil {
		t.Fatalf("newPlayer: %v", err)
	}
	if !p.hasN {
		t.Fatal("n-function not found in looping player")
	}
	start := time.Now()
	if _, ok := p.decryptN("xyz"); ok {
		t.Fatal("decryptN on infinite loop succeeded")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("decryptN took %v", d)
	}
	start = time.Now()
	if _, err := p.decryptSig("abcdefgh"); !errors.Is(err, errJSInterrupted) {
		t.Fatalf("decryptSig after interrupt: err = %v, want errJSInterrupted", err)
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("dead player call took %v", d)
	}
}

// Зацикливание при исполнении кода плеера ограничено jsLoadTimeout, а отмена
// контекста прерывает его сразу.
func TestLoadTimeoutAndCancel(t *testing.T) {
	setTimeouts(t, 200*time.Millisecond, time.Second)
	code := "var _yt_player={};(function(g){var window=this;" +
		`var Sg=function(a,b,c){var u={};u.k("alr","yes");return u};for(;;){}` +
		"})(_yt_player);"
	if findSignerName(code) == "" {
		t.Fatal("test player: signer not detected")
	}
	start := time.Now()
	if _, err := newPlayer(context.Background(), code); !errors.Is(err, errJSInterrupted) {
		t.Fatalf("err = %v, want errJSInterrupted", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("load took %v", d)
	}

	setTimeouts(t, time.Minute, time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start = time.Now()
	_, err := newPlayer(ctx, code)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("cancel took %v", d)
	}
}

func stubLoader(t *testing.T, fn func(ctx context.Context, url string) (*player, error)) {
	t.Helper()
	old := loadPlayerFn
	loadPlayerFn = func(ctx context.Context, _ *ex.Env, url string) (*player, error) { return fn(ctx, url) }
	playerCacheMu.Lock()
	playerCache = map[string]*cachedPlayer{}
	playerCacheMu.Unlock()
	t.Cleanup(func() {
		loadPlayerFn = old
		playerCacheMu.Lock()
		playerCache = map[string]*cachedPlayer{}
		playerCacheMu.Unlock()
	})
}

// Отменённая загрузка не запоминается: следующий вызов загружает заново.
// Обычная ошибка запоминается (в пределах playerErrTTL).
func TestPlayerCacheErrors(t *testing.T) {
	var calls atomic.Int32
	fail := true
	stubLoader(t, func(ctx context.Context, url string) (*player, error) {
		calls.Add(1)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if url == "bad" {
			return nil, errors.New("parse failed")
		}
		if fail {
			return nil, errors.New("unreachable")
		}
		return &player{}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := getPlayer(ctx, nil, "good"); err == nil {
		t.Fatal("cancelled load succeeded")
	}
	fail = false
	if p, err := getPlayer(context.Background(), nil, "good"); err != nil || p == nil {
		t.Fatalf("load after cancel: %v", err)
	}
	if _, err := getPlayer(context.Background(), nil, "good"); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("loader calls = %d, want 2", n)
	}

	for range 3 {
		if _, err := getPlayer(context.Background(), nil, "bad"); err == nil {
			t.Fatal("bad player loaded")
		}
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("loader calls = %d, want 3 (error cached)", n)
	}
	playerCacheMu.Lock()
	playerCache["bad"].failedAt = time.Now().Add(-playerErrTTL - time.Second)
	playerCacheMu.Unlock()
	getPlayer(context.Background(), nil, "bad")
	if n := calls.Load(); n != 4 {
		t.Fatalf("loader calls = %d, want 4 (expired error retried)", n)
	}
}

// Параллельные запросы одного URL ждут одной загрузки; если загружавший
// отменил запрос, ожидающий загружает сам.
func TestPlayerCacheSingleflight(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	stubLoader(t, func(ctx context.Context, url string) (*player, error) {
		if calls.Add(1) == 1 {
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return &player{}, nil
	})

	ctx1, cancel1 := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	errs := make([]error, 5)
	wg.Add(1)
	go func() { defer wg.Done(); _, errs[0] = getPlayer(ctx1, nil, "u") }()
	for calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	for i := 1; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, errs[i] = getPlayer(context.Background(), nil, "u") }()
	}
	time.Sleep(20 * time.Millisecond)
	cancel1()
	wg.Wait()
	if errs[0] == nil {
		t.Error("cancelled caller got no error")
	}
	for i := 1; i < 5; i++ {
		if errs[i] != nil {
			t.Errorf("waiter %d: %v", i, errs[i])
		}
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("loader calls = %d, want 2", n)
	}
	close(release)
}

func TestPlayerCacheEviction(t *testing.T) {
	stubLoader(t, func(context.Context, string) (*player, error) { return &player{}, nil })
	for i := range maxCachedPlayers + 3 {
		getPlayer(context.Background(), nil, fmt.Sprint(i))
		getPlayer(context.Background(), nil, "0") // «горячая» версия
	}
	playerCacheMu.Lock()
	defer playerCacheMu.Unlock()
	if len(playerCache) != maxCachedPlayers {
		t.Fatalf("cache size = %d, want %d", len(playerCache), maxCachedPlayers)
	}
	if _, ok := playerCache["0"]; !ok {
		t.Error("recently used player evicted")
	}
	if _, ok := playerCache[fmt.Sprint(maxCachedPlayers+2)]; !ok {
		t.Error("newest player evicted")
	}
}
