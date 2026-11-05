package dlp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Disaldr/go-dlp/media"
	"github.com/Disaldr/go-dlp/postprocess"
)

func ptrBool(b bool) *bool { return &b }

// resolve должен накладывать непустые поля Request поверх настроек клиента и
// наследовать остальные.
func TestResolveOverlay(t *testing.T) {
	c, err := New(Options{
		Format:            "worst",
		OutputDir:         "/base",
		MergeOutputFormat: "mkv",
		Overwrite:         false,
		RestrictFilenames: true,
		WriteSubtitles:    false,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Пустой Request — всё из клиента.
	if co := c.resolve(Request{}); co.format != "worst" || co.outputDir != "/base" ||
		co.mergeOutputFormat != "mkv" || co.overwrite != false || co.restrictFilenames != true {
		t.Fatalf("empty Request did not inherit: %+v", co)
	}

	// Переопределения.
	co := c.resolve(Request{
		Format:         "best",
		OutputDir:      "/custom",
		Overwrite:      ptrBool(true),
		WriteSubtitles: ptrBool(true),
	})
	if co.format != "best" {
		t.Errorf("format = %q, want best", co.format)
	}
	if co.outputDir != "/custom" {
		t.Errorf("outputDir = %q, want /custom", co.outputDir)
	}
	if !co.overwrite {
		t.Error("overwrite not overridden to true")
	}
	if !co.writeSubtitles {
		t.Error("writeSubtitles not overridden to true")
	}
	// Не заданное в Request — унаследовано.
	if co.mergeOutputFormat != "mkv" {
		t.Errorf("mergeOutputFormat = %q, want inherited mkv", co.mergeOutputFormat)
	}
	if !co.restrictFilenames {
		t.Error("restrictFilenames should stay inherited true")
	}
}

// PrepareWith должен применять per-call каталог и формат.
func TestPrepareWithOverrides(t *testing.T) {
	c, err := New(Options{OutputDir: "/base"})
	if err != nil {
		t.Fatal(err)
	}
	info := &media.Info{
		ID:    "vid1",
		Title: "Clip",
		Formats: []*media.Format{
			{FormatID: "sd", URL: "http://x/sd.mp4", Ext: "mp4", Height: 360, VCodec: "avc1", ACodec: "mp4a"},
			{FormatID: "hd", URL: "http://x/hd.mp4", Ext: "mp4", Height: 720, VCodec: "avc1", ACodec: "mp4a"},
		},
	}

	prepared, err := c.PrepareWith(info, Request{OutputDir: "/custom", Format: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared) != 1 {
		t.Fatalf("got %d prepared, want 1", len(prepared))
	}
	p := prepared[0]
	if dir := filepath.Dir(p.Filename); dir != "/custom" {
		t.Errorf("Filename dir = %q, want /custom", dir)
	}
	if p.Ext != "mp4" {
		t.Errorf("Ext = %q, want mp4", p.Ext)
	}
	// "b" выбирает лучший единичный формат — hd.
	if p.FormatID != "hd" {
		t.Errorf("FormatID = %q, want hd", p.FormatID)
	}

	// Клиентский каталог остаётся по умолчанию для обычного Prepare.
	base, err := c.Prepare(info)
	if err != nil {
		t.Fatal(err)
	}
	if dir := filepath.Dir(base[0].Filename); dir != "/base" {
		t.Errorf("default Prepare dir = %q, want /base", dir)
	}
}

func TestDefaultFormatByMergerCapability(t *testing.T) {
	// Муксер без кросс-контейнера (встроенный Go без ffmpeg) → предпочитать
	// совместимые пары.
	cGo, err := New(Options{Merger: postprocess.NewGoMerger()})
	if err != nil {
		t.Fatal(err)
	}
	if got := cGo.DefaultFormat(); !strings.Contains(got, "ext=mp4") || !strings.Contains(got, "ext=webm") {
		t.Errorf("Go-merger default format = %q, want same-container preference", got)
	}

	// Кросс-контейнерный муксер (ffmpeg-подобный) → обычный bv*+ba/b.
	cCross, err := New(Options{Merger: crossMerger{}})
	if err != nil {
		t.Fatal(err)
	}
	if got := cCross.DefaultFormat(); got != "bv*+ba/b" {
		t.Errorf("cross-merger default format = %q, want bv*+ba/b", got)
	}
}

type crossMerger struct{}

func (crossMerger) Merge(context.Context, string, []postprocess.Input) error { return nil }
func (crossMerger) Remux(context.Context, string, string) error              { return nil }
func (crossMerger) CrossContainer() bool                                     { return true }
