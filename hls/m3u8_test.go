package hls

import (
	"strings"
	"testing"

	"github.com/Disaldr/go-dlp/media"
)

const master = `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="English",LANGUAGE="en",DEFAULT=YES,URI="audio/en.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=2000000,AVERAGE-BANDWIDTH=1800000,RESOLUTION=1280x720,FRAME-RATE=30,CODECS="avc1.64001f,mp4a.40.2",AUDIO="aud"
720/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360,CODECS="avc1.4d401e,mp4a.40.2"
https://cdn.example.com/360.m3u8
`

func TestParseMaster(t *testing.T) {
	fs := ParseMaster(master, "https://example.com/v/master.m3u8", "hls")
	if len(fs) != 3 {
		t.Fatalf("got %d formats", len(fs))
	}
	audio, hd, sd := fs[0], fs[1], fs[2]
	if audio.VCodec != media.CodecNone || audio.ACodec != "mp4a.40.2" || audio.URL != "https://example.com/v/audio/en.m3u8" || audio.Language != "en" {
		t.Errorf("audio = %+v", audio)
	}
	if hd.Height != 720 || hd.ACodec != media.CodecNone || hd.TBR != 1800 || hd.FPS != 30 || hd.URL != "https://example.com/v/720/index.m3u8" {
		t.Errorf("hd = %+v", hd)
	}
	if sd.Height != 360 || sd.ACodec != "mp4a.40.2" || sd.FormatID != "hls-360p" {
		t.Errorf("sd = %+v", sd)
	}
}

func TestParseMedia(t *testing.T) {
	body := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:7
#EXT-X-KEY:METHOD=AES-128,URI="key.bin",IV=0x0000000000000000000000000000000A
#EXTINF:6.0,
seg7.ts
#EXT-X-KEY:METHOD=NONE
#EXTINF:5.5,
seg8.ts
#EXT-X-ENDLIST
`
	p, err := ParseMedia(body, "https://e.com/a/index.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	if !p.EndList || len(p.Segments) != 2 || p.Segments[0].Seq != 7 || p.Segments[1].Seq != 8 {
		t.Fatalf("playlist = %+v", p)
	}
	s := p.Segments[0]
	if s.Key == nil || s.Key.URI != "https://e.com/a/key.bin" || s.Key.IV[15] != 10 || s.URI != "https://e.com/a/seg7.ts" {
		t.Errorf("segment = %+v key=%+v", s, s.Key)
	}
	if p.Segments[1].Key != nil {
		t.Error("METHOD=NONE must reset key")
	}
}

func TestParseMediaRejectsBadByteRange(t *testing.T) {
	for _, br := range []string{"9223372036854775807@1", "-5", "100@-1", "abc", "2000000000", "10@9223372036854775800"} {
		body := "#EXTM3U\n#EXT-X-BYTERANGE:" + br + "\n#EXTINF:1,\ns.ts\n"
		if _, err := ParseMedia(body, "https://e.com/p.m3u8"); err == nil {
			t.Errorf("byte range %q must be rejected", br)
		}
	}
	ok := "#EXTM3U\n#EXT-X-BYTERANGE:100@0\n#EXTINF:1,\ns.ts\n#EXT-X-BYTERANGE:50\n#EXTINF:1,\ns.ts\n"
	p, err := ParseMedia(ok, "https://e.com/p.m3u8")
	if err != nil || p.Segments[1].Offset != 100 || p.Segments[1].Length != 50 {
		t.Fatalf("valid ranges: %+v %v", p, err)
	}
}

func TestParseMediaRejectsLongIV(t *testing.T) {
	body := "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"k\",IV=0x" + strings.Repeat("ab", 17) + "\n#EXTINF:1,\ns.ts\n"
	if _, err := ParseMedia(body, "https://e.com/p.m3u8"); err == nil {
		t.Error("IV longer than 16 bytes must be rejected")
	}
}
