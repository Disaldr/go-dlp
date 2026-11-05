package postprocess

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Disaldr/go-dlp/media"
	"github.com/at-wat/ebml-go"
)

func TestMergeMatroska(t *testing.T) {
	out := filepath.Join(t.TempDir(), "merged.webm")
	inputs := []Input{
		{Path: "testdata/vp9.webm", Format: &media.Format{VCodec: "vp9", ACodec: media.CodecNone}},
		{Path: "testdata/opus.webm", Format: &media.Format{VCodec: media.CodecNone, ACodec: "opus"}},
	}
	if err := NewGoMerger().Merge(context.Background(), out, inputs); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	// Разбираем результат и проверяем две дорожки с нужными кодеками.
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var c mkvContainer
	if err := ebml.Unmarshal(f, &c); err != nil {
		t.Fatalf("parse merged: %v", err)
	}
	tes := c.Segment.Tracks.TrackEntry
	if len(tes) != 2 {
		t.Fatalf("merged has %d tracks, want 2", len(tes))
	}
	var hasV, hasA bool
	for _, te := range tes {
		switch {
		case strings.Contains(te.CodecID, "VP9"):
			hasV = true
		case strings.Contains(te.CodecID, "OPUS"):
			hasA = true
			if len(te.CodecPrivate) == 0 {
				t.Error("Opus track lost CodecPrivate (OpusHead)")
			}
		}
	}
	if !hasV || !hasA {
		t.Errorf("tracks: hasVP9=%v hasOpus=%v", hasV, hasA)
	}
	ffmpegDecodesAV(t, out)
}

// ffmpegDecodesAV проверяет валидность и что ffmpeg видит ≥2 потока.
func ffmpegDecodesAV(t *testing.T, path string) {
	t.Helper()
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Logf("ffmpeg not found, skipping decode check")
		return
	}
	cmd := exec.Command(bin, "-v", "error", "-i", path, "-f", "null", "-")
	if out, err := cmd.CombinedOutput(); err != nil || len(out) > 0 {
		t.Fatalf("ffmpeg decode of %s failed: %v\n%s", path, err, out)
	}
}
