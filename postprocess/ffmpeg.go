// Package postprocess содержит постобработку через ffmpeg: слияние
// видео- и аудиодорожек и ремукс контейнера.
package postprocess

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/Disaldr/go-dlp/media"
)

// ErrNoFFmpeg — ffmpeg не найден.
var ErrNoFFmpeg = errors.New("ffmpeg not found")

// FFmpeg — обёртка над бинарником ffmpeg.
type FFmpeg struct {
	Path string
}

// FindFFmpeg ищет ffmpeg: явный путь (файл или каталог) либо PATH.
func FindFFmpeg(location string) (*FFmpeg, error) {
	if location != "" {
		if p, err := exec.LookPath(location); err == nil {
			return &FFmpeg{Path: p}, nil
		}
		if p, err := exec.LookPath(strings.TrimRight(location, "/") + "/ffmpeg"); err == nil {
			return &FFmpeg{Path: p}, nil
		}
		return nil, fmt.Errorf("%w at %s", ErrNoFFmpeg, location)
	}
	p, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, ErrNoFFmpeg
	}
	return &FFmpeg{Path: p}, nil
}

func (f *FFmpeg) run(ctx context.Context, args ...string) error {
	full := append([]string{"-y", "-hide_banner", "-nostdin", "-loglevel", "error"}, args...)
	cmd := exec.CommandContext(ctx, f.Path, full...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Input — входной файл для слияния.
type Input struct {
	Path   string
	Format *media.Format
}

// Merge сливает потоки: видео из первого входа с видео, аудио из всех
// аудио-входов (или из видео-входа, если отдельного аудио нет).
func (f *FFmpeg) Merge(ctx context.Context, out string, inputs []Input) error {
	var args []string
	for _, in := range inputs {
		args = append(args, "-i", in.Path)
	}
	videoMapped := false
	audioOnly := 0
	for _, in := range inputs {
		if in.Format.IsAudioOnly() {
			audioOnly++
		}
	}
	for i, in := range inputs {
		idx := strconv.Itoa(i)
		if in.Format.HasVideo() && !videoMapped {
			args = append(args, "-map", idx+":v:0")
			videoMapped = true
		}
		if in.Format.IsAudioOnly() || (audioOnly == 0 && in.Format.HasAudio()) {
			args = append(args, "-map", idx+":a:0?")
		}
	}
	args = append(args, "-c", "copy")
	if strings.HasSuffix(out, ".mp4") || strings.HasSuffix(out, ".m4a") || strings.HasSuffix(out, ".mov") {
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, "-f", muxer(out), out)
	return f.run(ctx, args...)
}

// Remux перепаковывает файл без перекодирования. Берутся только видео и аудио:
// служебные дорожки MPEG-TS (timed_id3 и т.п.) mp4-муксер не поддерживает,
// а ffmpeg в этом случае тихо пишет файл без moov и завершается успешно.
func (f *FFmpeg) Remux(ctx context.Context, in, out string) error {
	return f.run(ctx, "-i", in, "-map", "0:v?", "-map", "0:a?", "-c", "copy", "-f", muxer(out), out)
}

func muxer(out string) string {
	i := strings.LastIndexByte(out, '.')
	ext := ""
	if i >= 0 {
		ext = strings.ToLower(out[i+1:])
	}
	switch ext {
	case "mkv":
		return "matroska"
	case "m4a", "mp4", "mov":
		return "mp4"
	case "ts":
		return "mpegts"
	case "aac":
		return "adts"
	case "mka":
		return "matroska"
	case "", "part":
		return "matroska"
	}
	return ext
}

// MergeExt подбирает контейнер для слияния, как yt-dlp: если расширения
// совместимы — mp4 или webm, иначе mkv. preferred непустой — используется он.
func MergeExt(formats []*media.Format, preferred string) string {
	if preferred != "" {
		return preferred
	}
	mp4ish := map[string]bool{"mp4": true, "m4a": true, "m4v": true, "mov": true}
	webmish := map[string]bool{"webm": true, "weba": true}
	allMP4, allWebm := true, true
	for _, f := range formats {
		if !mp4ish[f.Ext] {
			allMP4 = false
		}
		if !webmish[f.Ext] {
			allWebm = false
		}
	}
	switch {
	case allMP4:
		return "mp4"
	case allWebm:
		return "webm"
	}
	return "mkv"
}

// CrossContainer: ffmpeg умеет смешивать дорожки из разных контейнеров.
func (f *FFmpeg) CrossContainer() bool { return true }
