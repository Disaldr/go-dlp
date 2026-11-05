package postprocess

import (
	"context"
	"errors"
)

// Merger объединяет видео- и аудиодорожки и перепаковывает контейнер — всегда
// без перекодирования (копирование потоков). Реализации: *FFmpeg (внешний
// бинарник ffmpeg) и *GoMerger (чистый Go, без зависимости от ffmpeg).
// Пользователь библиотеки может подставить свою реализацию через
// dlp.Options.Merger.
type Merger interface {
	// Merge объединяет дорожки из inputs в файл out. Первый вход с видео даёт
	// видеодорожку, аудиодорожки берутся из аудио-входов. Контейнер out
	// определяется по расширению.
	Merge(ctx context.Context, out string, inputs []Input) error
	// Remux перепаковывает in в контейнер out без перекодирования.
	Remux(ctx context.Context, in, out string) error
}

// ErrUnsupportedMux — реализация Merger не умеет данный контейнер или кодек.
// Клиент может обработать её запасным путём (например, сохранить HLS как .ts).
var ErrUnsupportedMux = errors.New("muxer: unsupported container or codec")

// CrossContainerMerger — необязательный интерфейс: умеет ли реализация
// объединять дорожки из РАЗНЫХ семейств контейнеров (например, видео MP4 +
// аудио WebM). ffmpeg умеет; встроенный Go-муксер — только одинаковые.
type CrossContainerMerger interface {
	CrossContainer() bool
}

// CanCrossContainer сообщает, умеет ли merger смешивать разные контейнеры.
func CanCrossContainer(m Merger) bool {
	if c, ok := m.(CrossContainerMerger); ok {
		return c.CrossContainer()
	}
	return false
}

var (
	_ Merger = (*FFmpeg)(nil)
	_ Merger = (*GoMerger)(nil)
)
