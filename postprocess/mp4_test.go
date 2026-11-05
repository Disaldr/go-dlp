package postprocess

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	mp4 "github.com/abema/go-mp4"
	"github.com/Disaldr/go-dlp/media"
)

// probeTracks открывает MP4 и возвращает его дорожки.
func probeTracks(t *testing.T, path string) mp4.Tracks {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	pi, err := mp4.Probe(f)
	if err != nil {
		t.Fatalf("probe %s: %v", path, err)
	}
	return pi.Tracks
}

// ffmpegDecodes проверяет через ffmpeg, что файл валиден и декодируется
// (пропускается, если ffmpeg недоступен).
func ffmpegDecodes(t *testing.T, path string) {
	t.Helper()
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Logf("ffmpeg not found, skipping decode check")
		return
	}
	cmd := exec.Command(bin, "-v", "error", "-i", path, "-f", "null", "-")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg could not decode merged %s: %v\n%s", path, err, out)
	}
	if len(out) > 0 {
		t.Fatalf("ffmpeg reported errors decoding %s:\n%s", path, out)
	}
}

func TestMergeMP4(t *testing.T) {
	cases := []struct {
		name          string
		video, audio  string
		wantVCodec    mp4.Codec
		wantACodec    mp4.Codec
	}{
		{"fragmented", "testdata/vfrag.mp4", "testdata/afrag.mp4", mp4.CodecAVC1, mp4.CodecMP4A},
		{"progressive", "testdata/vprog.mp4", "testdata/aprog.m4a", mp4.CodecAVC1, mp4.CodecMP4A},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "merged.mp4")
			inputs := []Input{
				{Path: tc.video, Format: &media.Format{VCodec: "avc1", ACodec: media.CodecNone}},
				{Path: tc.audio, Format: &media.Format{VCodec: media.CodecNone, ACodec: "mp4a"}},
			}
			if err := NewGoMerger().Merge(context.Background(), out, inputs); err != nil {
				t.Fatalf("Merge: %v", err)
			}
			tracks := probeTracks(t, out)
			if len(tracks) != 2 {
				t.Fatalf("merged file has %d tracks, want 2", len(tracks))
			}
			if tracks[0].TrackID != 1 || tracks[1].TrackID != 2 {
				t.Errorf("track IDs = %d,%d; want 1,2", tracks[0].TrackID, tracks[1].TrackID)
			}
			if tracks[0].Codec != tc.wantVCodec {
				t.Errorf("video codec = %v, want %v", tracks[0].Codec, tc.wantVCodec)
			}
			if tracks[1].Codec != tc.wantACodec {
				t.Errorf("audio codec = %v, want %v", tracks[1].Codec, tc.wantACodec)
			}
			ffmpegDecodes(t, out)
		})
	}
}

func TestRemuxMP4(t *testing.T) {
	out := filepath.Join(t.TempDir(), "remuxed.mp4")
	if err := NewGoMerger().Remux(context.Background(), "testdata/vprog.mp4", out); err != nil {
		t.Fatalf("Remux: %v", err)
	}
	tracks := probeTracks(t, out)
	if len(tracks) != 1 {
		t.Fatalf("remuxed file has %d tracks, want 1", len(tracks))
	}
	ffmpegDecodes(t, out)
}

// GoMerger должен делегировать неподдерживаемый контейнер в Fallback.
type fakeMerger struct{ merged, remuxed bool }

func (f *fakeMerger) Merge(context.Context, string, []Input) error { f.merged = true; return nil }
func (f *fakeMerger) Remux(context.Context, string, string) error  { f.remuxed = true; return nil }

func TestGoMergerFallback(t *testing.T) {
	fb := &fakeMerger{}
	g := &GoMerger{Fallback: fb}
	// .ts ремукс не поддержан встроенным муксером → делегируется.
	if err := g.Remux(context.Background(), "in.ts", "out.mp4"); err != nil {
		t.Fatal(err)
	}
	if !fb.remuxed {
		t.Error("TS remux was not delegated to fallback")
	}
	// Без Fallback — ErrUnsupportedMux.
	if err := NewGoMerger().Remux(context.Background(), "in.ts", "out.mp4"); err == nil {
		t.Error("expected error remuxing TS without fallback")
	}
}
