package dash

import (
	"strings"
	"testing"
	"time"

	"github.com/Disaldr/go-dlp/media"
)

const numberMPD = `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT10S">
 <Period>
  <AdaptationSet mimeType="video/mp4" codecs="avc1.4d401f" frameRate="30000/1001">
   <SegmentTemplate media="v/$RepresentationID$/seg-$Number%05d$.m4s" initialization="v/$RepresentationID$/init.mp4" startNumber="1" timescale="1000" duration="4000"/>
   <Representation id="720" bandwidth="2000000" width="1280" height="720"/>
  </AdaptationSet>
  <AdaptationSet mimeType="audio/mp4" lang="en">
   <Representation id="a128" bandwidth="128000" codecs="mp4a.40.2" audioSamplingRate="48000">
    <AudioChannelConfiguration schemeIdUri="urn:mpeg:dash:23003:3:audio_channel_configuration:2011" value="2"/>
    <SegmentTemplate media="a/$Number$.m4s" initialization="a/init.mp4" duration="5" />
   </Representation>
  </AdaptationSet>
  <AdaptationSet mimeType="video/mp4">
   <ContentProtection schemeIdUri="urn:uuid:edef8ba9-79d6-4ace-a3c8-27dcd51d21ed"/>
   <Representation id="drm" bandwidth="1" />
  </AdaptationSet>
 </Period>
</MPD>`

func TestParseTemplateNumber(t *testing.T) {
	fs, err := Parse([]byte(numberMPD), "https://cdn.example.com/x/manifest.mpd", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 2 {
		t.Fatalf("got %d formats (DRM must be skipped)", len(fs))
	}
	v, a := fs[0], fs[1]
	if v.FormatID != "dash-720" || v.Protocol != media.ProtoDASH || v.Height != 720 || v.FPS != 29.97 || v.ACodec != media.CodecNone || v.Ext != "mp4" {
		t.Errorf("video = %+v", v)
	}
	// init + ceil(10/4)=3 сегмента
	if len(v.Fragments) != 4 || v.Fragments[0].URL != "https://cdn.example.com/x/v/720/init.mp4" || v.Fragments[3].URL != "https://cdn.example.com/x/v/720/seg-00003.m4s" {
		t.Errorf("video fragments = %+v", v.Fragments)
	}
	if a.VCodec != media.CodecNone || a.ACodec != "mp4a.40.2" || a.Ext != "m4a" || a.AudioChannels != 2 || a.ASR != 48000 || a.Language != "en" {
		t.Errorf("audio = %+v", a)
	}
	if len(a.Fragments) != 3 || a.Fragments[2].URL != "https://cdn.example.com/x/a/2.m4s" {
		t.Errorf("audio fragments = %+v", a.Fragments)
	}
}

const timelineMPD = `<MPD type="static" mediaPresentationDuration="PT6S"><Period>
 <AdaptationSet mimeType="video/webm" codecs="vp9">
  <SegmentTemplate media="$RepresentationID$_$Time$.webm" initialization="$RepresentationID$_init.webm" timescale="10">
   <SegmentTimeline><S t="0" d="20" r="1"/><S d="20"/></SegmentTimeline>
  </SegmentTemplate>
  <Representation id="v1" bandwidth="500000" height="360"/>
 </AdaptationSet></Period></MPD>`

func TestParseTimeline(t *testing.T) {
	fs, err := Parse([]byte(timelineMPD), "https://e.com/m.mpd", "dash")
	if err != nil || len(fs) != 1 {
		t.Fatalf("%v %v", fs, err)
	}
	var urls []string
	for _, f := range fs[0].Fragments {
		urls = append(urls, f.URL)
	}
	want := []string{"https://e.com/v1_init.webm", "https://e.com/v1_0.webm", "https://e.com/v1_20.webm", "https://e.com/v1_40.webm"}
	if len(urls) != len(want) {
		t.Fatalf("urls = %v", urls)
	}
	for i := range want {
		if urls[i] != want[i] {
			t.Errorf("fragment %d = %s, want %s", i, urls[i], want[i])
		}
	}
	if fs[0].Ext != "webm" || fs[0].Fragments[1].Duration != 2 {
		t.Errorf("format = %+v", fs[0])
	}
}

const baseURLMPD = `<MPD type="static" mediaPresentationDuration="PT30S"><Period>
 <AdaptationSet contentType="video" mimeType="video/mp4">
  <Representation id="v" bandwidth="1000000" width="854" height="480" codecs="avc1.4d401e">
   <BaseURL>DASH_480.mp4</BaseURL><SegmentBase indexRange="800-1000"><Initialization range="0-799"/></SegmentBase>
  </Representation>
 </AdaptationSet></Period></MPD>`

func TestParseBaseURL(t *testing.T) {
	fs, err := Parse([]byte(baseURLMPD), "https://v.redd.it/abc/DASHPlaylist.mpd", "dash")
	if err != nil || len(fs) != 1 {
		t.Fatalf("%v %v", fs, err)
	}
	if f := fs[0]; f.Protocol != media.ProtoHTTPS || f.URL != "https://v.redd.it/abc/DASH_480.mp4" || f.Width != 854 {
		t.Errorf("format = %+v", f)
	}
}

func TestDynamicWithoutAST(t *testing.T) {
	if _, err := Parse([]byte(`<MPD type="dynamic"><Period/></MPD>`), "https://e.com/m.mpd", ""); err == nil {
		t.Error("dynamic manifest without availabilityStartTime must be rejected")
	}
}

const liveNumberMPD = `<MPD type="dynamic" availabilityStartTime="2026-01-01T00:00:00Z" minimumUpdatePeriod="PT2S"><Period start="PT0S">
 <AdaptationSet mimeType="video/mp4"><SegmentTemplate media="$RepresentationID$/$Number$.m4s" initialization="$RepresentationID$/init.mp4" startNumber="0" timescale="1" duration="2"/>
  <Representation id="v" bandwidth="1000"/></AdaptationSet></Period></MPD>`

func TestLiveNumber(t *testing.T) {
	nowFunc = func() time.Time { return time.Date(2026, 1, 1, 0, 1, 1, 0, time.UTC) } // +61 с
	defer func() { nowFunc = time.Now }()
	m, err := ParseManifest([]byte(liveNumberMPD), "https://live.example.com/m.mpd", "dash")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Dynamic || m.MinimumUpdatePeriod != 2*time.Second || !m.Formats[0].Live || m.Formats[0].DASHRepresentation != "v" {
		t.Fatalf("manifest = %+v", m)
	}
	// 61 с / 2 с = 30 полностью доступных сегментов (0..29), live-край — 27, 28, 29
	var urls []string
	for _, f := range m.Formats[0].Fragments {
		urls = append(urls, f.URL)
	}
	want := []string{"https://live.example.com/v/init.mp4", "https://live.example.com/v/27.m4s", "https://live.example.com/v/28.m4s", "https://live.example.com/v/29.m4s"}
	if strings.Join(urls, " ") != strings.Join(want, " ") {
		t.Errorf("fragments = %v, want %v", urls, want)
	}
	if !m.Formats[0].Fragments[0].Init || m.Formats[0].Fragments[1].Init {
		t.Error("init flag")
	}
}

const liveTimelineMPD = `<MPD type="dynamic" availabilityStartTime="2026-01-01T00:00:00Z"><Period>
 <AdaptationSet mimeType="audio/mp4"><SegmentTemplate media="a_$Time$.m4s" timescale="10">
  <SegmentTimeline><S t="100" d="20" r="-1"/></SegmentTimeline></SegmentTemplate>
  <Representation id="a" bandwidth="1"/></AdaptationSet></Period></MPD>`

func TestLiveTimelineOpenEnded(t *testing.T) {
	nowFunc = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 20, 0, time.UTC) } // 200 ед.
	defer func() { nowFunc = time.Now }()
	fs, err := Parse([]byte(liveTimelineMPD), "https://e.com/m.mpd", "dash")
	if err != nil {
		t.Fatal(err)
	}
	// доступно (200-100)/20 = 5 сегментов: t=100..180; край — 140,160,180
	var urls []string
	for _, f := range fs[0].Fragments {
		urls = append(urls, f.URL)
	}
	if strings.Join(urls, " ") != "https://e.com/a_140.m4s https://e.com/a_160.m4s https://e.com/a_180.m4s" {
		t.Errorf("fragments = %v", urls)
	}
}
