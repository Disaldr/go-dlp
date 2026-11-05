package dlp

import (
	"testing"

	"github.com/Disaldr/go-dlp/media"
)

// Для live HLS StreamLinksInfo должен отдать один адрес master-манифеста с
// заголовками, схлопнув варианты, и пометить IsLive.
func TestStreamLinksHLSManifest(t *testing.T) {
	c, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	const master = "https://usher.example/live.m3u8?token=abc"
	info := &media.Info{
		ID:          "chan",
		IsLive:      true,
		HTTPHeaders: map[string]string{"Referer": "https://example/"},
		Formats: []*media.Format{
			{FormatID: "hls-720", URL: "https://v/720.m3u8", ManifestURL: master, Protocol: media.ProtoHLS, Height: 720},
			{FormatID: "hls-480", URL: "https://v/480.m3u8", ManifestURL: master, Protocol: media.ProtoHLS, Height: 480},
		},
	}
	links, err := c.StreamLinksInfo(info)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 {
		t.Fatalf("got %d links, want 1 (master manifest)", len(links))
	}
	l := links[0]
	if l.URL != master {
		t.Errorf("URL = %q, want master %q", l.URL, master)
	}
	if !l.IsLive {
		t.Error("IsLive not set")
	}
	if l.Protocol != media.ProtoHLS {
		t.Errorf("Protocol = %q", l.Protocol)
	}
	if l.Headers["Referer"] != "https://example/" {
		t.Errorf("headers not carried: %v", l.Headers)
	}
}

// Для прогрессивных форматов отдаётся лучший прямой URL.
func TestStreamLinksProgressive(t *testing.T) {
	c, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	info := &media.Info{
		ID: "vid",
		Formats: []*media.Format{
			{FormatID: "sd", URL: "https://x/sd.mp4", Ext: "mp4", Height: 360, VCodec: "avc1", ACodec: "mp4a"},
			{FormatID: "hd", URL: "https://x/hd.mp4", Ext: "mp4", Height: 720, VCodec: "avc1", ACodec: "mp4a"},
		},
	}
	links, err := c.StreamLinksInfo(info)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 {
		t.Fatalf("got %d links, want 1", len(links))
	}
	if links[0].FormatID != "hd" {
		t.Errorf("FormatID = %q, want hd", links[0].FormatID)
	}
	if links[0].URL != "https://x/hd.mp4" {
		t.Errorf("URL = %q", links[0].URL)
	}
}
