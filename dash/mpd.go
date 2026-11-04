// Package dash разбирает манифесты MPEG-DASH (.mpd) в форматы.
//
// Поддерживаются SegmentTemplate ($Number$/$Time$, SegmentTimeline),
// SegmentList, SegmentBase и представления одним файлом (BaseURL), а также
// живые (type="dynamic") манифесты с SegmentTemplate: в них отдаются
// сегменты у live-края, а загрузчик перечитывает манифест.
// Зашифрованные (ContentProtection) представления пропускаются.
package dash

import (
	"encoding/xml"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Disaldr/go-dlp/media"
)

// LiveEdgeSegments — со скольких последних сегментов начинается запись эфира.
var LiveEdgeSegments = 3

// nowFunc — текущее время (переопределяется в тестах).
var nowFunc = time.Now

type mpdXML struct {
	Type                  string      `xml:"type,attr"`
	Duration              string      `xml:"mediaPresentationDuration,attr"`
	AvailabilityStartTime string      `xml:"availabilityStartTime,attr"`
	MinimumUpdatePeriod   string      `xml:"minimumUpdatePeriod,attr"`
	BaseURL               []string    `xml:"BaseURL"`
	Periods               []periodXML `xml:"Period"`
}

// Manifest — результат разбора MPD.
type Manifest struct {
	Formats []*media.Format
	// Dynamic — живой манифест.
	Dynamic bool
	// MinimumUpdatePeriod — как часто перечитывать живой манифест.
	MinimumUpdatePeriod time.Duration
}

// live — параметры расчёта окна живого потока.
type live struct {
	enabled     bool
	now         time.Time
	ast         time.Time // availabilityStartTime
	periodStart float64
}

type periodXML struct {
	Start    string       `xml:"start,attr"`
	Duration string       `xml:"duration,attr"`
	BaseURL  []string     `xml:"BaseURL"`
	Sets     []adaptation `xml:"AdaptationSet"`
}

type segmentInfo struct {
	Template *segTemplate `xml:"SegmentTemplate"`
	List     *segList     `xml:"SegmentList"`
	Base     *segBase     `xml:"SegmentBase"`
}

type common struct {
	MimeType     string `xml:"mimeType,attr"`
	ContentType  string `xml:"contentType,attr"`
	Codecs       string `xml:"codecs,attr"`
	Width        int    `xml:"width,attr"`
	Height       int    `xml:"height,attr"`
	FrameRate    string `xml:"frameRate,attr"`
	SamplingRate int    `xml:"audioSamplingRate,attr"`
	Channels     []struct {
		Value string `xml:"value,attr"`
	} `xml:"AudioChannelConfiguration"`
	Protection []struct {
		Scheme string `xml:"schemeIdUri,attr"`
	} `xml:"ContentProtection"`
}

type adaptation struct {
	common
	segmentInfo
	Lang    string           `xml:"lang,attr"`
	BaseURL []string         `xml:"BaseURL"`
	Reps    []representation `xml:"Representation"`
}

type representation struct {
	common
	segmentInfo
	ID        string   `xml:"id,attr"`
	Bandwidth int64    `xml:"bandwidth,attr"`
	BaseURL   []string `xml:"BaseURL"`
}

type segTemplate struct {
	Media          string    `xml:"media,attr"`
	Initialization string    `xml:"initialization,attr"`
	StartNumber    *int64    `xml:"startNumber,attr"`
	Timescale      int64     `xml:"timescale,attr"`
	Duration       int64     `xml:"duration,attr"`
	PTO            int64     `xml:"presentationTimeOffset,attr"`
	Timeline       *timeline `xml:"SegmentTimeline"`
}

type timeline struct {
	S []struct {
		T *int64 `xml:"t,attr"`
		D int64  `xml:"d,attr"`
		R int64  `xml:"r,attr"`
	} `xml:"S"`
}

type segList struct {
	Timescale int64 `xml:"timescale,attr"`
	Duration  int64 `xml:"duration,attr"`
	Init      *struct {
		SourceURL string `xml:"sourceURL,attr"`
		Range     string `xml:"range,attr"`
	} `xml:"Initialization"`
	URLs []struct {
		Media string `xml:"media,attr"`
		Range string `xml:"mediaRange,attr"`
	} `xml:"SegmentURL"`
}

type segBase struct {
	IndexRange string `xml:"indexRange,attr"`
}

// Parse разбирает MPD и возвращает форматы. idPrefix — префикс format_id ("dash").
func Parse(body []byte, manifestURL, idPrefix string) ([]*media.Format, error) {
	m, err := ParseManifest(body, manifestURL, idPrefix)
	if err != nil {
		return nil, err
	}
	return m.Formats, nil
}

// ParseManifest разбирает MPD, включая живые манифесты.
func ParseManifest(body []byte, manifestURL, idPrefix string) (*Manifest, error) {
	if idPrefix == "" {
		idPrefix = "dash"
	}
	var m mpdXML
	if err := xml.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("dash: %w", err)
	}
	if len(m.Periods) == 0 {
		return nil, fmt.Errorf("dash: no periods")
	}
	man := &Manifest{Dynamic: m.Type == "dynamic"}
	lv := live{enabled: man.Dynamic, now: nowFunc()}
	if man.Dynamic {
		ast, err := time.Parse(time.RFC3339Nano, m.AvailabilityStartTime)
		if err != nil {
			return nil, fmt.Errorf("dash: dynamic manifest without valid availabilityStartTime: %q", m.AvailabilityStartTime)
		}
		lv.ast = ast
		man.MinimumUpdatePeriod = time.Duration(ParseDuration(m.MinimumUpdatePeriod) * float64(time.Second))
	}
	base := join(manifestURL, first(m.BaseURL))
	// в живом манифесте актуален последний начавшийся период
	p := m.Periods[0]
	if man.Dynamic {
		p = m.Periods[len(m.Periods)-1]
	}
	lv.periodStart = ParseDuration(p.Start)
	periodDur := ParseDuration(p.Duration)
	if periodDur == 0 && !man.Dynamic {
		periodDur = ParseDuration(m.Duration)
	}
	pbase := join(base, first(p.BaseURL))

	var out []*media.Format
	seen := map[string]int{}
	for _, as := range p.Sets {
		abase := join(pbase, first(as.BaseURL))
		for _, r := range as.Reps {
			c := merge(as.common, r.common)
			if len(c.Protection) > 0 {
				continue // DRM
			}
			mt := c.MimeType
			if mt == "" {
				mt = c.ContentType
			}
			kind := strings.SplitN(mt, "/", 2)[0]
			if kind != "video" && kind != "audio" {
				continue // text/image и пр.
			}
			f := &media.Format{
				ManifestURL:        manifestURL,
				Live:               man.Dynamic,
				DASHRepresentation: r.ID,
				Width:              c.Width,
				Height:             c.Height,
				FPS:                parseFrameRate(c.FrameRate),
				ASR:                c.SamplingRate,
				TBR:                float64(r.Bandwidth) / 1000,
				Language:           as.Lang,
				Container:          containerOf(mt),
			}
			f.Ext = extOf(mt, kind)
			codec := c.Codecs
			if kind == "video" {
				f.VCodec, f.ACodec, f.VBR = codec, media.CodecNone, f.TBR
			} else {
				f.VCodec, f.ACodec, f.ABR = media.CodecNone, codec, f.TBR
				if len(c.Channels) > 0 {
					f.AudioChannels, _ = strconv.Atoi(c.Channels[0].Value)
				}
			}
			id := r.ID
			if id == "" {
				id = strconv.FormatInt(r.Bandwidth, 10)
			}
			f.FormatID = idPrefix + "-" + id
			if n := seen[f.FormatID]; n > 0 {
				f.FormatID += fmt.Sprintf("-%d", n)
			}
			seen[idPrefix+"-"+id]++

			rbase := join(abase, first(r.BaseURL))
			tmpl := r.Template
			if tmpl == nil {
				tmpl = as.Template
			}
			list := r.List
			if list == nil {
				list = as.List
			}
			switch {
			case tmpl != nil:
				frags, err := fromTemplate(tmpl, rbase, r, periodDur, lv)
				if err != nil {
					return nil, err
				}
				f.Fragments, f.Protocol = frags, media.ProtoDASH
				f.URL = rbase
			case man.Dynamic:
				continue // живые SegmentList/SegmentBase не поддерживаются
			case list != nil:
				f.Fragments, f.Protocol = fromList(list, rbase), media.ProtoDASH
				f.URL = rbase
			default:
				// один файл (с SegmentBase или без) — качаем целиком как обычный HTTPS
				f.URL, f.Protocol = rbase, media.ProtoHTTPS
			}
			out = append(out, f)
		}
	}
	man.Formats = out
	return man, nil
}

func merge(a, b common) common {
	c := a
	if b.MimeType != "" {
		c.MimeType = b.MimeType
	}
	if b.ContentType != "" {
		c.ContentType = b.ContentType
	}
	if b.Codecs != "" {
		c.Codecs = b.Codecs
	}
	if b.Width != 0 {
		c.Width = b.Width
	}
	if b.Height != 0 {
		c.Height = b.Height
	}
	if b.FrameRate != "" {
		c.FrameRate = b.FrameRate
	}
	if b.SamplingRate != 0 {
		c.SamplingRate = b.SamplingRate
	}
	if len(b.Channels) > 0 {
		c.Channels = b.Channels
	}
	c.Protection = append(append([]struct {
		Scheme string `xml:"schemeIdUri,attr"`
	}{}, a.Protection...), b.Protection...)
	return c
}

var reTemplateVar = regexp.MustCompile(`\$(RepresentationID|Number|Bandwidth|Time)(%0\d+d)?\$`)

func expand(tmpl string, r representation, number, t int64) string {
	s := reTemplateVar.ReplaceAllStringFunc(tmpl, func(m string) string {
		sm := reTemplateVar.FindStringSubmatch(m)
		var v any
		switch sm[1] {
		case "RepresentationID":
			return r.ID
		case "Number":
			v = number
		case "Bandwidth":
			v = r.Bandwidth
		case "Time":
			v = t
		}
		f := sm[2]
		if f == "" {
			f = "%d"
		}
		return fmt.Sprintf(f, v)
	})
	return strings.ReplaceAll(s, "$$", "$")
}

// maxFragments защищает от ошибочных манифестов.
const maxFragments = 200000

func fromTemplate(t *segTemplate, base string, r representation, periodDur float64, lv live) ([]media.Fragment, error) {
	var frags []media.Fragment
	if t.Initialization != "" {
		frags = append(frags, media.Fragment{URL: join(base, expand(t.Initialization, r, 0, 0)), Init: true})
	}
	ts := t.Timescale
	if ts == 0 {
		ts = 1
	}
	num := int64(1)
	if t.StartNumber != nil {
		num = *t.StartNumber
	}
	// elapsed — сколько медиавремени (в единицах timescale) доступно в живом периоде
	var elapsed int64
	if lv.enabled {
		secs := lv.now.Sub(lv.ast).Seconds() - lv.periodStart
		if secs < 0 {
			return nil, fmt.Errorf("dash: live stream has not started yet")
		}
		elapsed = int64(secs*float64(ts)) + t.PTO
	}
	if t.Timeline != nil {
		var cur int64
		for _, s := range t.Timeline.S {
			if s.T != nil {
				cur = *s.T
			}
			repeat := s.R
			if repeat < 0 {
				// r=-1 — повторять до конца периода (в эфире — до текущего момента)
				var end int64
				switch {
				case lv.enabled:
					end = elapsed
				case periodDur > 0:
					end = int64(periodDur*float64(ts)) + t.PTO
				}
				if end == 0 || s.D == 0 {
					return nil, fmt.Errorf("dash: open-ended SegmentTimeline without period duration")
				}
				repeat = (end-cur)/s.D - 1
				if !lv.enabled {
					repeat = max((end-cur+s.D-1)/s.D-1, 0)
				}
			}
			for i := int64(0); i <= repeat; i++ {
				frags = append(frags, media.Fragment{
					URL:      join(base, expand(t.Media, r, num, cur)),
					Duration: float64(s.D) / float64(ts),
				})
				cur += s.D
				num++
				if len(frags) > maxFragments {
					return nil, fmt.Errorf("dash: too many fragments")
				}
			}
		}
		if lv.enabled {
			frags = liveEdge(frags)
		}
		return frags, nil
	}
	if t.Duration == 0 {
		return nil, fmt.Errorf("dash: SegmentTemplate without duration or timeline")
	}
	segDur := float64(t.Duration) / float64(ts)
	start, count := num, int64(0)
	if lv.enabled {
		// полностью доступные сегменты: [startNumber, startNumber+avail)
		avail := (elapsed - t.PTO) / t.Duration
		first := max(avail-int64(LiveEdgeSegments), 0)
		start, count = num+first, avail-first
	} else {
		if periodDur <= 0 {
			return nil, fmt.Errorf("dash: SegmentTemplate without period duration")
		}
		count = int64(math.Ceil(periodDur / segDur))
	}
	if count > maxFragments {
		return nil, fmt.Errorf("dash: too many fragments")
	}
	for i := int64(0); i < count; i++ {
		n := start + i
		frags = append(frags, media.Fragment{
			URL:      join(base, expand(t.Media, r, n, (n-num)*t.Duration+t.PTO)),
			Duration: segDur,
		})
	}
	return frags, nil
}

// liveEdge оставляет init-сегменты и последние LiveEdgeSegments медиасегментов.
func liveEdge(frags []media.Fragment) []media.Fragment {
	var init, segs []media.Fragment
	for _, f := range frags {
		if f.Init {
			init = append(init, f)
		} else {
			segs = append(segs, f)
		}
	}
	if len(segs) > LiveEdgeSegments {
		segs = segs[len(segs)-LiveEdgeSegments:]
	}
	return append(init, segs...)
}

func fromList(l *segList, base string) []media.Fragment {
	var frags []media.Fragment
	if l.Init != nil {
		u := base
		if l.Init.SourceURL != "" {
			u = join(base, l.Init.SourceURL)
		}
		f := media.Fragment{URL: u, Init: true}
		f.Offset, f.Length = parseRange(l.Init.Range)
		frags = append(frags, f)
	}
	ts := l.Timescale
	if ts == 0 {
		ts = 1
	}
	for _, s := range l.URLs {
		u := base
		if s.Media != "" {
			u = join(base, s.Media)
		}
		f := media.Fragment{URL: u, Duration: float64(l.Duration) / float64(ts)}
		f.Offset, f.Length = parseRange(s.Range)
		frags = append(frags, f)
	}
	return frags
}

// parseRange разбирает "100-199" в (offset=100, length=100).
func parseRange(s string) (int64, int64) {
	a, b, ok := strings.Cut(s, "-")
	if !ok {
		return 0, 0
	}
	start, err1 := strconv.ParseInt(a, 10, 64)
	end, err2 := strconv.ParseInt(b, 10, 64)
	if err1 != nil || err2 != nil || end < start {
		return 0, 0
	}
	return start, end - start + 1
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return strings.TrimSpace(s[0])
}

func join(base, ref string) string {
	if ref == "" {
		return base
	}
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}

func parseFrameRate(s string) float64 {
	if a, b, ok := strings.Cut(s, "/"); ok {
		n, _ := strconv.ParseFloat(a, 64)
		d, _ := strconv.ParseFloat(b, 64)
		if d != 0 {
			return math.Round(n/d*1000) / 1000
		}
		return 0
	}
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func extOf(mime, kind string) string {
	switch {
	case strings.HasSuffix(mime, "/webm"):
		return "webm"
	case strings.HasSuffix(mime, "/mp4") && kind == "audio":
		return "m4a"
	case strings.HasSuffix(mime, "/mp4"):
		return "mp4"
	}
	return "mp4"
}

func containerOf(mime string) string {
	switch {
	case strings.HasSuffix(mime, "/mp4"):
		if strings.HasPrefix(mime, "audio") {
			return "m4a_dash"
		}
		return "mp4_dash"
	case strings.HasSuffix(mime, "/webm"):
		return "webm_dash"
	}
	return ""
}

var reISO = regexp.MustCompile(`^P(?:(\d+(?:\.\d+)?)D)?(?:T(?:(\d+(?:\.\d+)?)H)?(?:(\d+(?:\.\d+)?)M)?(?:(\d+(?:\.\d+)?)S)?)?$`)

// ParseDuration разбирает ISO 8601 длительность (PT1H2M3.5S) в секунды.
func ParseDuration(s string) float64 {
	m := reISO.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0
	}
	mult := []float64{86400, 3600, 60, 1}
	var total float64
	for i, g := range m[1:] {
		if g != "" {
			v, _ := strconv.ParseFloat(g, 64)
			total += v * mult[i]
		}
	}
	return total
}
