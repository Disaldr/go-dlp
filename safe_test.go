package dlp

import (
	"strings"
	"testing"
)

func TestSafeNames(t *testing.T) {
	// обычные значения не меняются
	for _, s := range []string{"137", "hls-720p", "en.ru_RU-1.2", "dash-V300"} {
		if got := safeComponent(s); got != s {
			t.Errorf("safeComponent(%q) = %q", s, got)
		}
	}
	// враждебные — без разделителей пути, ".." и ведущей точки
	for _, s := range []string{"../../etc/x", "..", "a/b\\c", ".hidden", "x/../../y", ""} {
		got := safeComponent(s)
		if got == "" || strings.ContainsAny(got, `/\`) || strings.Contains(got, "..") || got[0] == '.' {
			t.Errorf("safeComponent(%q) = %q is not safe", s, got)
		}
	}
	for in, want := range map[string]string{"mp4": "mp4", "M4A": "m4a", "../mp4": "", "mp4/x": "", "averyverylongext": ""} {
		if got := safeExt(in); got != want {
			t.Errorf("safeExt(%q) = %q, want %q", in, got, want)
		}
	}
}
