package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Disaldr/go-dlp/downloader"
	"github.com/Disaldr/go-dlp/media"
)

func TestProgressOutput(t *testing.T) {
	var b bytes.Buffer
	p := &progressPrinter{w: &b, template: "P:%(progress)j URL:%(info.webpage_url)s", info: &media.Info{WebpageURL: "https://example.test/a/b"}}
	p.update(downloader.Progress{Status: downloader.StatusDownloading, Filename: "/tmp/a:b.mp4", Downloaded: 42, Total: 100, TotalEstimated: true, Speed: 21, ETA: 2 * time.Second})
	line := strings.TrimSuffix(b.String(), "\n")
	encoded, url, ok := strings.Cut(strings.TrimPrefix(line, "P:"), " URL:")
	if !ok || url != "https://example.test/a/b" {
		t.Fatalf("URL changed: %q", line)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(encoded), &fields); err != nil {
		t.Fatal(err)
	}
	if fields["total_bytes"] != nil || fields["total_bytes_estimate"] != float64(100) || fields["eta"] != float64(2) || fields["filename"] != "/tmp/a:b.mp4" {
		t.Fatalf("wrong progress fields: %v", fields)
	}
	b.Reset()
	p = &progressPrinter{w: &b, newline: true, noColors: true}
	p.update(downloader.Progress{Status: downloader.StatusDownloading, Downloaded: 42, Total: 100})
	p.update(downloader.Progress{Status: downloader.StatusFinished, Downloaded: 100, Total: 100})
	if strings.ContainsAny(b.String(), "\r\x1b") || strings.Count(b.String(), "\n") != 2 {
		t.Fatalf("unexpected terminal control sequences: %q", b.String())
	}
}
