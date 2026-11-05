package postprocess

// Склейка контейнеров Matroska/WebM на чистом Go без перекодирования.
// Исходники (по одной дорожке на файл) разбираются целиком через ebml, затем
// кадры обеих дорожек сливаются в глобальном порядке по времени и пишутся
// заново блочным writer-ом webm (он сам нарезает кластеры и проставляет
// относительные таймкоды). Подходит для webm-совместимых кодеков
// (VP8/VP9/AV1 + Opus/Vorbis) — типичный случай adaptive-форматов YouTube.

import (
	"fmt"
	"os"
	"sort"

	"github.com/at-wat/ebml-go"
	"github.com/at-wat/ebml-go/webm"
)

// mkvContainer — минимальная схема для разбора входного webm/mkv.
type mkvContainer struct {
	Segment struct {
		Info    webm.Info      `ebml:"Info"`
		Tracks  webm.Tracks    `ebml:"Tracks"`
		Cluster []webm.Cluster `ebml:"Cluster"`
	} `ebml:"Segment"`
}

// mkvFrame — один кадр с абсолютным временем (мс) для слияния.
type mkvFrame struct {
	track    int
	ts       int64
	keyframe bool
	data     []byte
}

func mergeMatroska(out string, inputs []Input) error {
	if len(inputs) == 0 {
		return fmt.Errorf("%w: no inputs", ErrUnsupportedMux)
	}
	var tracks []webm.TrackEntry
	var frames []mkvFrame
	for i, inp := range inputs {
		f, err := os.Open(inp.Path)
		if err != nil {
			return err
		}
		var c mkvContainer
		if err := ebml.Unmarshal(f, &c); err != nil {
			f.Close()
			return fmt.Errorf("%w: parse %s: %v", ErrUnsupportedMux, inp.Path, err)
		}
		f.Close()
		if len(c.Segment.Tracks.TrackEntry) != 1 {
			return fmt.Errorf("%w: %s has %d tracks, expected 1", ErrUnsupportedMux, inp.Path, len(c.Segment.Tracks.TrackEntry))
		}
		scale := c.Segment.Info.TimecodeScale
		if scale == 0 {
			scale = 1_000_000 // по умолчанию 1 мс
		}
		te := c.Segment.Tracks.TrackEntry[0]
		te.TrackNumber = uint64(i + 1)
		te.TrackUID = uint64(i + 1)
		tracks = append(tracks, te)

		add := func(b ebml.Block, clusterTC uint64) error {
			if len(b.Data) != 1 {
				// Лейсинг (несколько кадров в блоке) не поддержан — у кадров
				// нет индивидуальных таймкодов.
				return fmt.Errorf("%w: laced blocks in %s", ErrUnsupportedMux, inp.Path)
			}
			tcUnits := int64(clusterTC) + int64(b.Timecode)
			ts := tcUnits * int64(scale) / 1_000_000 // в миллисекундах
			frames = append(frames, mkvFrame{track: i, ts: ts, keyframe: b.Keyframe, data: b.Data[0]})
			return nil
		}
		for _, cl := range c.Segment.Cluster {
			for _, sb := range cl.SimpleBlock {
				if err := add(sb, cl.Timecode); err != nil {
					return err
				}
			}
			for _, bg := range cl.BlockGroup {
				if err := add(bg.Block, cl.Timecode); err != nil {
					return err
				}
			}
		}
	}

	// Глобальный порядок по времени; внутри одного времени — стабильно по дорожке.
	sort.SliceStable(frames, func(a, b int) bool { return frames[a].ts < frames[b].ts })

	of, err := os.Create(out)
	if err != nil {
		return err
	}
	// writer закрывает of сам.
	ws, err := webm.NewSimpleBlockWriter(of, tracks)
	if err != nil {
		of.Close()
		return fmt.Errorf("%w: %v", ErrUnsupportedMux, err)
	}
	var writeErr error
	for _, fr := range frames {
		if _, err := ws[fr.track].Write(fr.keyframe, fr.ts, fr.data); err != nil {
			writeErr = err
			break
		}
	}
	for _, w := range ws {
		if err := w.Close(); err != nil && writeErr == nil {
			writeErr = err
		}
	}
	if writeErr != nil {
		return fmt.Errorf("%w: %v", ErrUnsupportedMux, writeErr)
	}
	return nil
}
