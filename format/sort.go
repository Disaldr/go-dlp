// Package format реализует сортировку форматов и язык выбора форматов
// в стиле yt-dlp ("bv*+ba/b", "best[height<=720]", "137+140" и т.д.).
package format

import (
	"slices"
	"strings"

	"github.com/Disaldr/go-dlp/media"
)

func codecBase(c string) string {
	return strings.ToLower(strings.SplitN(c, ".", 2)[0])
}

// VCodecRank — предпочтение видеокодека (больше — лучше), как в yt-dlp.
func VCodecRank(c string) int {
	switch codecBase(c) {
	case "av01":
		return 8
	case "vp09", "vp9":
		return 7
	case "hev1", "hvc1", "h265", "dvh1", "dvhe":
		return 6
	case "avc1", "avc3", "h264":
		return 5
	case "vp8":
		return 4
	case "h263":
		return 3
	case "theora":
		return 2
	case "none":
		return 0
	}
	return 1
}

// ACodecRank — предпочтение аудиокодека (больше — лучше).
func ACodecRank(c string) int {
	switch codecBase(c) {
	case "flac":
		return 12
	case "alac":
		return 11
	case "wav", "pcm":
		return 10
	case "opus":
		return 9
	case "vorbis":
		return 8
	case "aac", "mp4a":
		return 7
	case "mp3":
		return 6
	case "ac-4":
		return 5
	case "ec-3", "eac3":
		return 4
	case "ac-3", "ac3":
		return 3
	case "dtsc", "dts":
		return 2
	case "none":
		return 0
	}
	return 1
}

func protoRank(p string) int {
	switch p {
	case media.ProtoHTTPS, "":
		return 3
	case media.ProtoHTTP:
		return 2
	case media.ProtoHLS:
		return 1
	}
	return 0
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// key — ключ сортировки, сравнивается лексикографически.
func key(f *media.Format) []float64 {
	br := f.TBR
	if br == 0 {
		br = f.VBR + f.ABR
	}
	return []float64{
		float64(f.Preference),
		float64(f.LanguagePreference),
		f.Quality,
		float64(f.Height),
		float64(f.Width),
		f.FPS,
		float64(boolInt(f.DynamicRange != "" && f.DynamicRange != "SDR")),
		float64(VCodecRank(f.VCodec)),
		float64(f.AudioChannels),
		float64(ACodecRank(f.ACodec)),
		float64(f.Size()),
		br,
		float64(f.ASR),
		float64(protoRank(f.Protocol)),
	}
}

// Compare сравнивает форматы: <0 если a хуже b.
func Compare(a, b *media.Format) int {
	ka, kb := key(a), key(b)
	for i := range ka {
		switch {
		case ka[i] < kb[i]:
			return -1
		case ka[i] > kb[i]:
			return 1
		}
	}
	return 0
}

// Sort сортирует форматы от худшего к лучшему (как yt-dlp: лучший — последний).
func Sort(formats []*media.Format) {
	slices.SortStableFunc(formats, Compare)
}
