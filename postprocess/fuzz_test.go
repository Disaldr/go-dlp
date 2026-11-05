package postprocess

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// Муксер читает недоверенные байты из сети — битый/враждебный вход не должен
// ронять процесс паникой; ожидается аккуратная ошибка (ErrUnsupportedMux).
func TestMergeMP4MalformedNoPanic(t *testing.T) {
	dir := t.TempDir()
	valid, err := os.ReadFile("testdata/vfrag.mp4")
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(1))
	cases := map[string][]byte{
		"empty":       {},
		"garbage":     {0, 0, 0, 8, 'j', 'u', 'n', 'k'},
		"truncated":   valid[:len(valid)/2],
		"tiny-header": {0, 0, 0, 1},
		"huge-size":   {0xff, 0xff, 0xff, 0xff, 'm', 'o', 'o', 'v'},
	}
	// Несколько случайных обрезков валидного файла.
	for i := 0; i < 20; i++ {
		n := rng.Intn(len(valid))
		cases["rand-trunc-"+string(rune('a'+i))] = valid[:n]
	}
	g := NewGoMerger()
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			in := filepath.Join(dir, "in.mp4")
			if err := os.WriteFile(in, data, 0o644); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(dir, "out.mp4")
			// Не должно паниковать; результат — ошибка (без ffmpeg-фоллбэка).
			_ = g.Merge(context.Background(), out, []Input{{Path: in}, {Path: in}})
			_ = g.Remux(context.Background(), in, out)
		})
	}
}
