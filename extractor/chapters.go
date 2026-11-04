package extractor

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Disaldr/go-dlp/media"
)

// RawChapter — глава до проверки: начало (Start < 0 — неизвестно) и название.
type RawChapter struct {
	Start float64
	Title string
}

// BuildChapters проверяет и достраивает главы (аналог _extract_chapters_helper
// yt-dlp): отбрасывает главы с неизвестным началом, вне [0, duration] и нарушающие
// порядок; конец главы — начало следующей или duration. sorted=true — сперва
// отсортировать (для эвристических источников вроде описания).
func BuildChapters(list []RawChapter, duration float64, sorted bool) []media.Chapter {
	if duration <= 0 || len(list) == 0 {
		return nil
	}
	if sorted {
		sort.SliceStable(list, func(i, j int) bool { return list[i].Start < list[j].Start })
	}
	var out []media.Chapter
	last := 0.0
	for _, c := range list {
		if c.Start < 0 || c.Start < last || c.Start > duration {
			continue
		}
		out = append(out, media.Chapter{StartTime: c.Start, Title: strings.TrimSpace(c.Title)})
		last = c.Start
	}
	// как yt-dlp: промежуток до первой главы — отдельная безымянная глава,
	// чтобы главы покрывали всё видео
	if len(out) > 0 && out[0].StartTime > 0 {
		out = append([]media.Chapter{{StartTime: 0}}, out...)
	}
	for i := range out {
		if i+1 < len(out) {
			out[i].EndTime = out[i+1].StartTime
		} else {
			out[i].EndTime = duration
		}
		if out[i].Title == "" {
			out[i].Title = fmt.Sprintf("<Untitled Chapter %d>", i+1)
		}
	}
	return out
}

const durationRe = `(?:\d+:)?\d{1,2}:\d{2}`

var (
	reChapterTimeFirst = regexp.MustCompile(`(?m)^\s*(` + durationRe + `)\b\W*\s(.+?)\s*$`)
	reChapterTimeLast  = regexp.MustCompile(`(?m)^\s*(.+?)\b\W*\s(` + durationRe + `)\s*$`)
)

// ChaptersFromDescription ищет главы в описании: строки «00:00 Название» или
// «Название 00:00» (как yt-dlp). Главы признаются, только если есть начало с 0:00.
func ChaptersFromDescription(desc string, duration float64) []media.Chapter {
	try := func(re *regexp.Regexp, timeIdx, titleIdx int) []media.Chapter {
		var raw []RawChapter
		hasZero := false
		for _, m := range re.FindAllStringSubmatch(desc, -1) {
			start := ParseDuration(m[timeIdx])
			hasZero = hasZero || start == 0
			raw = append(raw, RawChapter{Start: start, Title: m[titleIdx]})
		}
		// без таймкода 0:00 это скорее упоминания времени в тексте, а не главы
		if !hasZero {
			return nil
		}
		if ch := BuildChapters(raw, duration, true); len(ch) >= 2 {
			return ch
		}
		return nil
	}
	if ch := try(reChapterTimeFirst, 1, 2); ch != nil {
		return ch
	}
	return try(reChapterTimeLast, 2, 1)
}
