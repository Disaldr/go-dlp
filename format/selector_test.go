package format

import (
	"testing"

	"github.com/Disaldr/go-dlp/media"
)

func testFormats() []*media.Format {
	return []*media.Format{
		{FormatID: "18", Ext: "mp4", Height: 360, VCodec: "avc1.42001E", ACodec: "mp4a.40.2", TBR: 500},
		{FormatID: "137", Ext: "mp4", Height: 1080, VCodec: "avc1.640028", ACodec: "none", TBR: 4000},
		{FormatID: "248", Ext: "webm", Height: 1080, VCodec: "vp9", ACodec: "none", TBR: 3000},
		{FormatID: "136", Ext: "mp4", Height: 720, VCodec: "avc1.4d401f", ACodec: "none", TBR: 2000},
		{FormatID: "140", Ext: "m4a", VCodec: "none", ACodec: "mp4a.40.2", TBR: 128, ABR: 128},
		{FormatID: "251", Ext: "webm", VCodec: "none", ACodec: "opus", TBR: 160, ABR: 160},
	}
}

func TestSelect(t *testing.T) {
	cases := map[string]string{
		"best":                      "18",
		"b":                         "18",
		"bv*+ba/b":                  "248+251",
		"bv+ba":                     "248+251",
		"bv[ext=mp4]+ba[ext=m4a]/b": "137+140",
		"bv[height<=720]+ba":        "136+251",
		"worst":                     "18",
		"wa":                        "140",
		"137+140":                   "137+140",
		"mp4":                       "137",
		"m4a":                       "140",
		"bv*[vcodec^=avc1]":         "137",
		"bv[vcodec!=vp9]":           "137",
		"bestvideo.2":               "137",
		"(bv[height>2000]/bv[height>=720])+ba[acodec*=mp4a]": "248+140",
		"bv[filesize<10M]/b": "18",
		"bv[filesize<?10M]":  "248",
		"nonexistent/251":    "251",
	}
	for spec, want := range cases {
		sel, err := Compile(spec)
		if err != nil {
			t.Errorf("Compile(%q): %v", spec, err)
			continue
		}
		got, err := sel.Select(testFormats())
		if err != nil {
			t.Errorf("Select(%q): %v", spec, err)
			continue
		}
		if got[0].ID() != want {
			t.Errorf("Select(%q) = %s, want %s", spec, got[0].ID(), want)
		}
	}
}

func TestSelectMultiple(t *testing.T) {
	got, err := MustCompile("bv,ba").Select(testFormats())
	if err != nil || len(got) != 2 || got[0].ID() != "248" || got[1].ID() != "251" {
		t.Fatalf("got %v %v", got, err)
	}
}

func TestSelectAudioOnlySite(t *testing.T) {
	audio := []*media.Format{
		{FormatID: "mp3", Ext: "mp3", VCodec: "none", ACodec: "mp3", ABR: 128},
		{FormatID: "opus", Ext: "opus", VCodec: "none", ACodec: "opus", ABR: 64},
	}
	for spec, want := range map[string]string{"bv*+ba/b": "opus", "b": "opus", "w": "mp3", "best[abr>100]": "mp3"} {
		got, err := MustCompile(spec).Select(audio)
		if err != nil || got[0].ID() != want {
			t.Errorf("%s = %v %v, want %s", spec, got, err, want)
		}
	}
}

func TestSelectErrors(t *testing.T) {
	if _, err := Compile("best[height"); err == nil {
		t.Error("expected parse error")
	}
	if _, err := MustCompile("bv[height>5000]").Select(testFormats()); err == nil {
		t.Error("expected not available error")
	}
}
