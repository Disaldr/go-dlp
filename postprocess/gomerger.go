package postprocess

import (
	"context"
	"fmt"
	"strings"
)

// GoMerger — склейка и ремукс дорожек на чистом Go, без ffmpeg. Поддерживает
// контейнеры MP4 (mp4/m4a/mov) и Matroska (mkv/webm) — всегда копированием
// потоков, без перекодирования. Неподдерживаемые случаи (например, ремукс
// MPEG-TS) делегируются в Fallback, если он задан, иначе возвращается
// ErrUnsupportedMux.
type GoMerger struct {
	// Fallback — запасной Merger для контейнеров/кодеков, которые встроенный
	// муксер не умеет. Обычно *FFmpeg. nil — вернуть ErrUnsupportedMux.
	Fallback Merger
}

// NewGoMerger создаёт встроенный муксер на чистом Go.
func NewGoMerger() *GoMerger { return &GoMerger{} }

// container определяет контейнер по расширению пути.
func container(path string) string {
	i := strings.LastIndexByte(path, '.')
	if i < 0 {
		return ""
	}
	switch strings.ToLower(path[i+1:]) {
	case "mp4", "m4a", "m4v", "mov", "mp4a":
		return "mp4"
	case "mkv", "webm", "weba", "mka":
		return "mkv"
	case "ts":
		return "ts"
	}
	return strings.ToLower(path[i+1:])
}

// Merge объединяет дорожки inputs в файл out без перекодирования.
func (g *GoMerger) Merge(ctx context.Context, out string, inputs []Input) error {
	if len(inputs) == 0 {
		return fmt.Errorf("merge: no inputs")
	}
	switch container(out) {
	case "mp4":
		if err := mergeMP4(out, inputs); err != nil {
			return g.maybeFallbackMerge(ctx, out, inputs, err)
		}
		return nil
	case "mkv":
		if err := mergeMatroska(out, inputs); err != nil {
			return g.maybeFallbackMerge(ctx, out, inputs, err)
		}
		return nil
	default:
		return g.maybeFallbackMerge(ctx, out, inputs, fmt.Errorf("%w: output %s", ErrUnsupportedMux, out))
	}
}

// Remux перепаковывает in в контейнер out без перекодирования.
func (g *GoMerger) Remux(ctx context.Context, in, out string) error {
	// Ремукс — частный случай слияния одного входа. Контейнер входа должен
	// совпадать по семейству с выходом; MPEG-TS встроенный муксер не читает.
	if container(in) == "ts" {
		return g.maybeFallbackRemux(ctx, in, out, fmt.Errorf("%w: MPEG-TS input", ErrUnsupportedMux))
	}
	inputs := []Input{{Path: in}}
	switch container(out) {
	case "mp4":
		if err := mergeMP4(out, inputs); err != nil {
			return g.maybeFallbackRemux(ctx, in, out, err)
		}
		return nil
	case "mkv":
		if err := mergeMatroska(out, inputs); err != nil {
			return g.maybeFallbackRemux(ctx, in, out, err)
		}
		return nil
	default:
		return g.maybeFallbackRemux(ctx, in, out, fmt.Errorf("%w: output %s", ErrUnsupportedMux, out))
	}
}

func (g *GoMerger) maybeFallbackMerge(ctx context.Context, out string, inputs []Input, cause error) error {
	if g.Fallback != nil {
		return g.Fallback.Merge(ctx, out, inputs)
	}
	return cause
}

func (g *GoMerger) maybeFallbackRemux(ctx context.Context, in, out string, cause error) error {
	if g.Fallback != nil {
		return g.Fallback.Remux(ctx, in, out)
	}
	return cause
}
