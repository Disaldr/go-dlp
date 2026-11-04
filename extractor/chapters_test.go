package extractor_test

import (
	"testing"

	ex "github.com/Disaldr/go-dlp/extractor"
)

func TestChaptersFromDescription(t *testing.T) {
	desc := "Описание\n\n00:00 Intro\n0:05 The cool thing\n00:17 End\nhttps://example.com 99:99"
	ch := ex.ChaptersFromDescription(desc, 19)
	if len(ch) != 3 || ch[0].Title != "Intro" || ch[1].StartTime != 5 || ch[1].EndTime != 17 || ch[2].EndTime != 19 {
		t.Fatalf("chapters = %+v", ch)
	}
	// «Название 1:02» и часы
	ch = ex.ChaptersFromDescription("Start 0:00\nMiddle 1:02:03\n", 4000)
	if len(ch) != 2 || ch[1].StartTime != 3723 || ch[1].Title != "Middle" {
		t.Fatalf("title-first chapters = %+v", ch)
	}
	// без 0:00 — не главы, а просто упоминания времени
	if ch := ex.ChaptersFromDescription("смотрите с 1:20 и 2:30\n1:20 момент\n2:30 другой", 300); ch != nil {
		t.Fatalf("must ignore timestamps without 0:00: %+v", ch)
	}
	// промежуток до первой главы — безымянная глава (как в yt-dlp)
	ch = ex.BuildChapters([]ex.RawChapter{{Start: 10, Title: "A"}, {Start: 20}}, 30, false)
	if len(ch) != 3 || ch[0].Title != "<Untitled Chapter 1>" || ch[0].EndTime != 10 || ch[2].Title != "<Untitled Chapter 3>" {
		t.Fatalf("untitled chapters = %+v", ch)
	}
	// глава за пределами длительности отбрасывается
	if ch := ex.ChaptersFromDescription("0:00 a\n0:10 b\n9:00 c", 60); len(ch) != 2 || ch[1].EndTime != 60 {
		t.Fatalf("out-of-range chapter: %+v", ch)
	}
}
