// Package hls разбирает плейлисты HLS (m3u8): master-плейлисты превращаются
// в форматы, media-плейлисты — в списки сегментов для загрузчика.
package hls

import (
	"bufio"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/Disaldr/go-dlp/media"
)

// ParseAttrs разбирает список атрибутов вида KEY=VAL,KEY2="a,b".
func ParseAttrs(s string) map[string]string {
	attrs := map[string]string{}
	for len(s) > 0 {
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			break
		}
		k := strings.TrimSpace(s[:eq])
		s = s[eq+1:]
		var v string
		if strings.HasPrefix(s, `"`) {
			end := strings.IndexByte(s[1:], '"')
			if end < 0 {
				v, s = s[1:], ""
			} else {
				v, s = s[1:end+1], s[end+2:]
			}
			s = strings.TrimPrefix(s, ",")
		} else {
			comma := strings.IndexByte(s, ',')
			if comma < 0 {
				v, s = s, ""
			} else {
				v, s = s[:comma], s[comma+1:]
			}
		}
		attrs[k] = strings.TrimSpace(v)
	}
	return attrs
}

func resolve(base, ref string) string {
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

func lines(body string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// IsMaster сообщает, является ли плейлист master-плейлистом.
func IsMaster(body string) bool {
	return strings.Contains(body, "#EXT-X-STREAM-INF")
}

// SplitCodecs делит строку CODECS на видео- и аудиокодек.
func SplitCodecs(codecs string) (vcodec, acodec string) {
	vcodec, acodec = media.CodecNone, media.CodecNone
	if strings.TrimSpace(codecs) == "" {
		return "", ""
	}
	for _, c := range strings.Split(codecs, ",") {
		c = strings.TrimSpace(c)
		base := strings.ToLower(strings.SplitN(c, ".", 2)[0])
		switch base {
		case "avc1", "avc3", "hev1", "hvc1", "vp8", "vp9", "vp09", "av01", "dvh1", "dvhe", "theora", "h264", "h265":
			if vcodec == media.CodecNone {
				vcodec = c
			}
		case "mp4a", "opus", "vorbis", "ac-3", "ec-3", "flac", "mp3", "alac", "ac-4", "dtsc":
			if acodec == media.CodecNone {
				acodec = c
			}
		}
	}
	return vcodec, acodec
}

// ParseMaster превращает master-плейлист в форматы. Если плейлист — media,
// возвращает единственный формат с самим плейлистом.
func ParseMaster(body, manifestURL, idPrefix string) []*media.Format {
	if idPrefix == "" {
		idPrefix = "hls"
	}
	if !IsMaster(body) {
		return []*media.Format{{
			FormatID: idPrefix, URL: manifestURL, ManifestURL: manifestURL,
			Ext: "mp4", Protocol: media.ProtoHLS,
		}}
	}
	ls := lines(body)

	type rendition struct{ attrs map[string]string }
	groups := map[string][]rendition{}
	audioGroupOf := map[*media.Format]string{}
	var formats []*media.Format
	seenID := map[string]int{}
	uniq := func(id string) string {
		seenID[id]++
		if n := seenID[id]; n > 1 {
			return fmt.Sprintf("%s-%d", id, n-1)
		}
		return id
	}

	for _, l := range ls {
		if strings.HasPrefix(l, "#EXT-X-MEDIA:") {
			a := ParseAttrs(strings.TrimPrefix(l, "#EXT-X-MEDIA:"))
			groups[a["GROUP-ID"]] = append(groups[a["GROUP-ID"]], rendition{a})
			if a["TYPE"] == "AUDIO" && a["URI"] != "" {
				name := a["NAME"]
				if name == "" {
					name = a["GROUP-ID"]
				}
				f := &media.Format{
					FormatID:    uniq(sanitizeID(idPrefix + "-" + a["GROUP-ID"] + "-" + name)),
					URL:         resolve(manifestURL, a["URI"]),
					ManifestURL: manifestURL,
					Ext:         "mp4",
					Protocol:    media.ProtoHLS,
					VCodec:      media.CodecNone,
					Language:    a["LANGUAGE"],
					FormatNote:  name,
				}
				audioGroupOf[f] = a["GROUP-ID"]
				if a["DEFAULT"] == "YES" && a["LANGUAGE"] != "" {
					f.LanguagePreference = 1
				}
				if ch := a["CHANNELS"]; ch != "" {
					n, _ := strconv.Atoi(strings.SplitN(ch, "/", 2)[0])
					f.AudioChannels = n
				}
				formats = append(formats, f)
			}
		}
	}
	// кодек для аудиоформатов возьмём из первого варианта, ссылающегося на группу
	audioCodecByGroup := map[string]string{}

	for i := 0; i < len(ls); i++ {
		l := ls[i]
		if !strings.HasPrefix(l, "#EXT-X-STREAM-INF:") {
			continue
		}
		a := ParseAttrs(strings.TrimPrefix(l, "#EXT-X-STREAM-INF:"))
		uri := ""
		for j := i + 1; j < len(ls); j++ {
			if !strings.HasPrefix(ls[j], "#") {
				uri = ls[j]
				i = j
				break
			}
		}
		if uri == "" {
			continue
		}
		f := &media.Format{
			URL:         resolve(manifestURL, uri),
			ManifestURL: manifestURL,
			Ext:         "mp4",
			Protocol:    media.ProtoHLS,
		}
		bw, _ := strconv.ParseFloat(a["AVERAGE-BANDWIDTH"], 64)
		if bw == 0 {
			bw, _ = strconv.ParseFloat(a["BANDWIDTH"], 64)
		}
		f.TBR = bw / 1000
		if res := a["RESOLUTION"]; res != "" {
			if w, h, ok := strings.Cut(res, "x"); ok {
				f.Width, _ = strconv.Atoi(w)
				f.Height, _ = strconv.Atoi(h)
			}
		}
		f.FPS, _ = strconv.ParseFloat(a["FRAME-RATE"], 64)
		f.VCodec, f.ACodec = SplitCodecs(a["CODECS"])
		if f.VCodec == media.CodecNone && f.Height > 0 {
			f.VCodec = ""
		}
		if ag := a["AUDIO"]; ag != "" {
			hasURI := false
			for _, r := range groups[ag] {
				if r.attrs["URI"] != "" {
					hasURI = true
				}
			}
			if f.ACodec != media.CodecNone && f.ACodec != "" {
				audioCodecByGroup[ag] = f.ACodec
			}
			if hasURI && f.VCodec != media.CodecNone {
				f.ACodec = media.CodecNone
			}
		}
		if a["VIDEO-RANGE"] == "PQ" || a["VIDEO-RANGE"] == "HLG" {
			f.DynamicRange = "HDR"
		}
		id := idPrefix
		if f.Height > 0 {
			id += fmt.Sprintf("-%dp", f.Height)
		} else if f.TBR > 0 {
			id += fmt.Sprintf("-%d", int(f.TBR))
		}
		f.FormatID = uniq(id)
		formats = append(formats, f)
	}
	for f, g := range audioGroupOf {
		if c := audioCodecByGroup[g]; c != "" && f.ACodec == "" {
			f.ACodec = c
		}
	}
	return formats
}

func sanitizeID(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '_'
	}, s)
}

// Key — параметры шифрования сегментов.
type Key struct {
	Method string // NONE, AES-128, SAMPLE-AES
	URI    string
	IV     []byte // nil — использовать номер сегмента
}

// Segment — сегмент media-плейлиста.
type Segment struct {
	URI      string
	Duration float64
	Seq      int64
	Key      *Key
	// ByteRange: Length > 0 — загружать диапазон [Offset, Offset+Length).
	Offset, Length int64
}

// MediaPlaylist — разобранный media-плейлист.
type MediaPlaylist struct {
	TargetDuration float64
	MediaSequence  int64
	Segments       []Segment
	Init           *Segment // EXT-X-MAP
	EndList        bool
}

// ParseMedia разбирает media-плейлист.
func ParseMedia(body, playlistURL string) (*MediaPlaylist, error) {
	ls := lines(body)
	if len(ls) == 0 || !strings.HasPrefix(ls[0], "#EXTM3U") {
		return nil, fmt.Errorf("hls: not an m3u8 playlist")
	}
	if IsMaster(body) {
		return nil, fmt.Errorf("hls: expected media playlist, got master")
	}
	p := &MediaPlaylist{}
	var key *Key
	var dur float64
	var nextOffset int64
	var br *[2]int64
	seq := int64(0)
	for _, l := range ls {
		switch {
		case strings.HasPrefix(l, "#EXT-X-TARGETDURATION:"):
			p.TargetDuration, _ = strconv.ParseFloat(strings.TrimPrefix(l, "#EXT-X-TARGETDURATION:"), 64)
		case strings.HasPrefix(l, "#EXT-X-MEDIA-SEQUENCE:"):
			p.MediaSequence, _ = strconv.ParseInt(strings.TrimPrefix(l, "#EXT-X-MEDIA-SEQUENCE:"), 10, 64)
			seq = p.MediaSequence
		case strings.HasPrefix(l, "#EXT-X-KEY:"):
			a := ParseAttrs(strings.TrimPrefix(l, "#EXT-X-KEY:"))
			if a["METHOD"] == "NONE" {
				key = nil
				continue
			}
			k := &Key{Method: a["METHOD"], URI: resolve(playlistURL, a["URI"])}
			if iv := a["IV"]; iv != "" {
				b, err := parseHex(iv)
				if err != nil {
					return nil, fmt.Errorf("hls: bad IV: %w", err)
				}
				k.IV = b
			}
			key = k
		case strings.HasPrefix(l, "#EXT-X-MAP:"):
			a := ParseAttrs(strings.TrimPrefix(l, "#EXT-X-MAP:"))
			s := &Segment{URI: resolve(playlistURL, a["URI"]), Key: key}
			if r := a["BYTERANGE"]; r != "" {
				s.Length, s.Offset = parseByteRange(r, 0)
			}
			p.Init = s
		case strings.HasPrefix(l, "#EXTINF:"):
			v := strings.TrimPrefix(l, "#EXTINF:")
			v, _, _ = strings.Cut(v, ",")
			dur, _ = strconv.ParseFloat(v, 64)
		case strings.HasPrefix(l, "#EXT-X-BYTERANGE:"):
			length, off := parseByteRange(strings.TrimPrefix(l, "#EXT-X-BYTERANGE:"), nextOffset)
			br = &[2]int64{off, length}
		case l == "#EXT-X-ENDLIST":
			p.EndList = true
		case strings.HasPrefix(l, "#"):
			// прочие теги игнорируем
		default:
			s := Segment{URI: resolve(playlistURL, l), Duration: dur, Seq: seq, Key: key}
			if br != nil {
				s.Offset, s.Length = br[0], br[1]
				nextOffset = s.Offset + s.Length
				br = nil
			}
			p.Segments = append(p.Segments, s)
			seq++
			dur = 0
		}
	}
	return p, nil
}

func parseByteRange(s string, prevEnd int64) (length, offset int64) {
	l, o, hasOff := strings.Cut(s, "@")
	length, _ = strconv.ParseInt(l, 10, 64)
	offset = prevEnd
	if hasOff {
		offset, _ = strconv.ParseInt(o, 10, 64)
	}
	return length, offset
}

func parseHex(s string) ([]byte, error) {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if len(s)%2 == 1 {
		s = "0" + s
	}
	out := make([]byte, len(s)/2)
	for i := range out {
		b, err := strconv.ParseUint(s[2*i:2*i+2], 16, 8)
		if err != nil {
			return nil, err
		}
		out[i] = byte(b)
	}
	// IV должен быть 16 байт — дополняем слева нулями
	if len(out) < 16 {
		out = append(make([]byte, 16-len(out)), out...)
	}
	return out, nil
}
