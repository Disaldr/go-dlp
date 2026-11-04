package outtmpl

import (
	"testing"

	"github.com/Disaldr/go-dlp/media"
)

func TestFilename(t *testing.T) {
	info := &media.Info{ID: "abc", Title: "Hello: World / Part 1?", Ext: "mp4", PlaylistIndex: 3, Uploader: ""}
	cases := map[string]string{
		Default: "Hello： World ⧸ Part 1？ [abc].mp4",
		"%(playlist_index)03d - %(title).5s.%(ext)s": "003 - Hello.mp4",
		"%(uploader,id)s/%(id)s.%(ext)s":             "abc/abc.mp4",
		"%(uploader|Unknown)s 100%%.%(ext)s":         "Unknown 100%.mp4",
	}
	for tmpl, want := range cases {
		if got := Filename("", tmpl, info, false); got != want {
			t.Errorf("%q -> %q, want %q", tmpl, got, want)
		}
	}
	if got := Filename("", Default, info, true); got != "Hello__World___Part_1_ [abc].mp4" {
		t.Errorf("restricted: %q", got)
	}
}
