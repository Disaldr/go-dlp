package postprocess

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

func ValidateAudioFormat(format string) error {
	switch format {
	case "", "best", "aac", "alac", "flac", "m4a", "mp3", "opus", "vorbis", "wav":
		return nil
	}
	return fmt.Errorf("unsupported audio format %q", format)
}

// ValidateRemuxFormat принимает один контейнер; правила source>target не поддерживаются.
func ValidateRemuxFormat(format string) error {
	switch format {
	case "", "avi", "flv", "mkv", "mov", "mp4", "webm", "aac", "aiff", "alac", "flac", "m4a", "mka", "mp3", "ogg", "opus", "vorbis", "wav":
		return nil
	}
	return fmt.Errorf("unsupported remux format %q (expected a single container)", format)
}

// AudioTarget возвращает расширение и кодек для извлечения аудио.
// best сохраняет исходный кодек, если для него известен аудиоконтейнер.
func AudioTarget(format, codec string) (ext, encoder string) {
	codec = strings.ToLower(codec)
	if strings.HasPrefix(codec, "mp4a") {
		codec = "aac"
	}
	if format == "" || format == "best" {
		switch codec {
		case "aac", "alac":
			format = "m4a"
		case "mp3", "opus", "flac", "vorbis":
			format = codec
		default:
			if strings.HasPrefix(codec, "pcm_") {
				return "wav", "copy"
			}
			return "mka", "copy"
		}
	}
	switch format {
	case "m4a", "aac":
		ext, encoder = format, "aac"
		if codec == "aac" || (format == "m4a" && codec == "alac") {
			encoder = "copy"
		}
	case "alac":
		ext, encoder = "m4a", "alac"
	case "mp3":
		ext, encoder = "mp3", "libmp3lame"
	case "opus":
		ext, encoder = "opus", "libopus"
	case "vorbis":
		ext, encoder = "ogg", "libvorbis"
	case "flac":
		ext, encoder = "flac", "flac"
	case "wav":
		ext, encoder = "wav", "pcm_s16le"
	}
	if codec == format {
		encoder = "copy"
	}
	return ext, encoder
}

// AudioCodec читает реальный кодек, в том числе когда экстрактор его не сообщил.
func (f *FFmpeg) AudioCodec(ctx context.Context, in string) (string, error) {
	name := "ffprobe"
	if strings.EqualFold(filepath.Ext(f.Path), ".exe") {
		name += ".exe"
	}
	probe, err := exec.LookPath(filepath.Join(filepath.Dir(f.Path), name))
	if err != nil {
		probe, err = exec.LookPath(name)
	}
	if err != nil {
		return "", fmt.Errorf("extract audio: ffprobe not found: %w", err)
	}
	out, err := exec.CommandContext(ctx, probe, "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=codec_name", "-of", "default=noprint_wrappers=1:nokey=1", in).Output()
	if err != nil {
		return "", fmt.Errorf("ffprobe: %w", err)
	}
	codec := strings.TrimSpace(string(out))
	if codec == "" {
		return "", fmt.Errorf("extract audio: no audio stream in %s", in)
	}
	return codec, nil
}

// ExtractAudio копирует подходящий кодек или перекодирует в запрошенный формат.
func (f *FFmpeg) ExtractAudio(ctx context.Context, in, out, encoder string) error {
	args := []string{"-i", in, "-map", "0:a:0", "-vn", "-c:a", encoder}
	if encoder == "libmp3lame" || encoder == "libvorbis" {
		args = append(args, "-q:a", "5")
	}
	if encoder == "libopus" {
		args = append(args, "-b:a", "128k")
	}
	return f.run(ctx, append(args, "-f", muxer(out), out)...)
}
