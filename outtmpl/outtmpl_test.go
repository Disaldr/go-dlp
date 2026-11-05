package outtmpl

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

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

func TestBytePrecisionAndRawOutput(t *testing.T) {
	title := strings.Repeat("Ё🙂", 50)
	name := Evaluate("%(title).100B", map[string]any{"title": title}, false)
	if len(name) > 100 || !utf8.ValidString(name) || !strings.HasPrefix(title, name) {
		t.Fatalf("invalid byte truncation: %q (%d bytes)", name, len(name))
	}
	path := "/tmp/Каталог/Title: \"sample\".mp4"
	if got := Render("FILE:%(filepath)s", map[string]any{"filepath": path}); got != "FILE:"+path {
		t.Fatalf("path was sanitized: %q", got)
	}
	got := Render("%(progress)j", map[string]any{"progress": map[string]any{"filename": path, "speed": 12.5}})
	var decoded map[string]any
	if err := json.Unmarshal([]byte(got), &decoded); err != nil || decoded["filename"] != path {
		t.Fatalf("invalid raw JSON %q: %v", got, err)
	}
	if got := Render("%(n).2f", map[string]any{"n": int64(123)}); got != "123.00" {
		t.Fatalf("numeric format = %q", got)
	}
}

func TestWindowsFilenames(t *testing.T) {
	for title, want := range map[string]string{"CON": "_CON.mp4", "LPT1": "_LPT1.mp4", "com¹": "_com¹.mp4", "Normal": "Normal.mp4"} {
		info := &media.Info{Title: title, Ext: "mp4"}
		if got := FilenameWithWindows("", "%(title)s.%(ext)s", info, false, true); got != want {
			t.Errorf("%q -> %q, want %q", title, got, want)
		}
	}
	info := &media.Info{Title: "folder", Ext: "mp4"}
	if got := FilenameWithWindows("", "%(title)s. /name?.mp4. ", info, false, true); got != "folder/name？.mp4" {
		t.Errorf("trailing dots/spaces or reserved characters: %q", got)
	}
	abs := filepath.Join(t.TempDir(), "%(title)s.%(ext)s")
	if got := FilenameWithWindows("", abs, info, false, true); !filepath.IsAbs(got) {
		t.Fatalf("absolute output became relative: %q", got)
	}
}
