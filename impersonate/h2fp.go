package impersonate

import (
	"bytes"
	"encoding/binary"
	"net"
	"slices"
	"strings"
	"sync"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

// H2Profile — HTTP/2-отпечаток браузера (то, что видно в «akamai fingerprint»:
// SETTINGS|WINDOW_UPDATE|PRIORITY|порядок псевдозаголовков).
type H2Profile struct {
	// Settings — параметры первого кадра SETTINGS в нужном порядке.
	Settings []http2.Setting
	// ConnWindowIncrement — инкремент начального WINDOW_UPDATE соединения.
	ConnWindowIncrement uint32
	// PseudoOrder — порядок псевдозаголовков: "masp" = :method :authority :scheme :path.
	PseudoOrder string
	// Priority — приоритет в кадре HEADERS (nil — без флага PRIORITY).
	Priority *http2.PriorityParam
	// HeaderOrder — порядок обычных заголовков (в нижнем регистре); прочие — следом.
	HeaderOrder []string
}

func (p *H2Profile) setting(id http2.SettingID) (uint32, bool) {
	for _, s := range p.Settings {
		if s.ID == id {
			return s.Val, true
		}
	}
	return 0, false
}

// initialWindow — INITIAL_WINDOW_SIZE профиля (по умолчанию 65535).
func (p *H2Profile) initialWindow() uint32 {
	if v, ok := p.setting(http2.SettingInitialWindowSize); ok {
		return v
	}
	return 65535
}

// fpConn переписывает исходящие кадры HTTP/2 под профиль браузера:
// первый SETTINGS заменяется профильным (значения, влияющие на состояние x/net —
// окна и размер таблицы, — x/net настраивается так же), а блоки заголовков
// перекодируются с нужным порядком псевдозаголовков и флагом PRIORITY.
// Наш HPACK-кодировщик работает без динамической таблицы, поэтому таблица
// декодера сервера всегда согласована.
type fpConn struct {
	net.Conn
	p *H2Profile

	mu           sync.Mutex
	buf          []byte
	prefaceDone  bool
	settingsDone bool
	dec          *hpack.Decoder
	enc          *hpack.Encoder
	encBuf       bytes.Buffer
	pending      []byte // склеиваемый блок HEADERS+CONTINUATION
	pendingHdr   frameHeader
	err          error
}

type frameHeader struct {
	length   uint32
	typ      http2.FrameType
	flags    http2.Flags
	streamID uint32
}

func newFPConn(c net.Conn, p *H2Profile) *fpConn {
	fc := &fpConn{Conn: c, p: p}
	fc.dec = hpack.NewDecoder(4096, nil)
	fc.enc = hpack.NewEncoder(&fc.encBuf)
	fc.enc.SetMaxDynamicTableSizeLimit(0)
	return fc
}

func (c *fpConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return 0, c.err
	}
	c.buf = append(c.buf, b...)
	var out []byte
	if !c.prefaceDone {
		if len(c.buf) < len(http2.ClientPreface) {
			return len(b), nil
		}
		out = append(out, c.buf[:len(http2.ClientPreface)]...)
		c.buf = c.buf[len(http2.ClientPreface):]
		c.prefaceDone = true
	}
	for len(c.buf) >= 9 {
		h := frameHeader{
			length:   uint32(c.buf[0])<<16 | uint32(c.buf[1])<<8 | uint32(c.buf[2]),
			typ:      http2.FrameType(c.buf[3]),
			flags:    http2.Flags(c.buf[4]),
			streamID: binary.BigEndian.Uint32(c.buf[5:9]) & 0x7fffffff,
		}
		total := 9 + int(h.length)
		if len(c.buf) < total {
			break
		}
		payload := c.buf[9:total]
		var err error
		out, err = c.transform(out, h, payload)
		if err != nil {
			c.err = err
			return 0, err
		}
		c.buf = c.buf[total:]
	}
	if len(out) > 0 {
		if _, err := c.Conn.Write(out); err != nil {
			c.err = err
			return 0, err
		}
	}
	return len(b), nil
}

func (c *fpConn) transform(out []byte, h frameHeader, payload []byte) ([]byte, error) {
	switch {
	case h.typ == http2.FrameSettings && !c.settingsDone && h.flags&http2.FlagSettingsAck == 0:
		c.settingsDone = true
		var body []byte
		for _, s := range c.p.Settings {
			body = binary.BigEndian.AppendUint16(body, uint16(s.ID))
			body = binary.BigEndian.AppendUint32(body, s.Val)
		}
		return appendFrame(out, http2.FrameSettings, 0, 0, body), nil
	case h.typ == http2.FrameHeaders:
		c.pendingHdr = h
		c.pending = append(c.pending[:0], payload...)
		if h.flags&http2.FlagHeadersEndHeaders == 0 {
			return out, nil
		}
		return c.flushHeaders(out)
	case h.typ == http2.FrameContinuation && c.pending != nil:
		c.pending = append(c.pending, payload...)
		if h.flags&http2.FlagContinuationEndHeaders == 0 {
			return out, nil
		}
		return c.flushHeaders(out)
	}
	return appendFrame(out, h.typ, h.flags, h.streamID, payload), nil
}

func (c *fpConn) flushHeaders(out []byte) ([]byte, error) {
	block := c.pending
	h := c.pendingHdr
	c.pending = nil
	// x/net не добавляет PADDED/PRIORITY, но на всякий случай учитываем
	if h.flags&http2.FlagHeadersPadded != 0 && len(block) > 0 {
		pad := int(block[0])
		block = block[1 : len(block)-pad]
	}
	if h.flags&http2.FlagHeadersPriority != 0 && len(block) >= 5 {
		block = block[5:]
	}
	fields, err := c.dec.DecodeFull(block)
	if err != nil {
		return nil, err
	}
	fields = c.reorder(fields)
	c.encBuf.Reset()
	for _, f := range fields {
		if err := c.enc.WriteField(f); err != nil {
			return nil, err
		}
	}
	encoded := c.encBuf.Bytes()

	flags := h.flags & http2.FlagHeadersEndStream
	var prio []byte
	if pp := c.p.Priority; pp != nil {
		flags |= http2.FlagHeadersPriority
		dep := pp.StreamDep & 0x7fffffff
		if pp.Exclusive {
			dep |= 1 << 31
		}
		prio = binary.BigEndian.AppendUint32(nil, dep)
		prio = append(prio, pp.Weight)
	}
	const maxFrame = 16384
	first := append(prio, encoded[:min(len(encoded), maxFrame-len(prio))]...)
	rest := encoded[len(first)-len(prio):]
	if len(rest) == 0 {
		flags |= http2.FlagHeadersEndHeaders
	}
	out = appendFrame(out, http2.FrameHeaders, flags, h.streamID, first)
	for len(rest) > 0 {
		n := min(len(rest), maxFrame)
		var cf http2.Flags
		if n == len(rest) {
			cf = http2.FlagContinuationEndHeaders
		}
		out = appendFrame(out, http2.FrameContinuation, cf, h.streamID, rest[:n])
		rest = rest[n:]
	}
	return out, nil
}

var pseudoByLetter = map[byte]string{'m': ":method", 'a': ":authority", 's': ":scheme", 'p': ":path"}

func (c *fpConn) reorder(fields []hpack.HeaderField) []hpack.HeaderField {
	var pseudo, regular []hpack.HeaderField
	for _, f := range fields {
		if strings.HasPrefix(f.Name, ":") {
			pseudo = append(pseudo, f)
		} else {
			regular = append(regular, f)
		}
	}
	rank := func(name string, order []string) int {
		if i := slices.Index(order, name); i >= 0 {
			return i
		}
		return len(order)
	}
	var porder []string
	for i := 0; i < len(c.p.PseudoOrder); i++ {
		porder = append(porder, pseudoByLetter[c.p.PseudoOrder[i]])
	}
	slices.SortStableFunc(pseudo, func(a, b hpack.HeaderField) int { return rank(a.Name, porder) - rank(b.Name, porder) })
	slices.SortStableFunc(regular, func(a, b hpack.HeaderField) int {
		return rank(a.Name, c.p.HeaderOrder) - rank(b.Name, c.p.HeaderOrder)
	})
	return append(pseudo, regular...)
}

func appendFrame(out []byte, typ http2.FrameType, flags http2.Flags, stream uint32, payload []byte) []byte {
	n := len(payload)
	out = append(out, byte(n>>16), byte(n>>8), byte(n), byte(typ), byte(flags))
	out = binary.BigEndian.AppendUint32(out, stream&0x7fffffff)
	return append(out, payload...)
}

// --- профили браузеров ---

var chromeH2 = &H2Profile{
	Settings: []http2.Setting{
		{ID: http2.SettingHeaderTableSize, Val: 65536},
		{ID: http2.SettingEnablePush, Val: 0},
		{ID: http2.SettingInitialWindowSize, Val: 6291456},
		{ID: http2.SettingMaxHeaderListSize, Val: 262144},
	},
	ConnWindowIncrement: 15663105,
	PseudoOrder:         "masp",
	Priority:            &http2.PriorityParam{StreamDep: 0, Exclusive: true, Weight: 255},
	HeaderOrder: []string{
		"cache-control", "sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform", "upgrade-insecure-requests",
		"user-agent", "accept", "origin", "sec-fetch-site", "sec-fetch-mode", "sec-fetch-user", "sec-fetch-dest",
		"referer", "accept-encoding", "accept-language", "cookie", "priority",
	},
}

var firefoxH2 = &H2Profile{
	Settings: []http2.Setting{
		{ID: http2.SettingHeaderTableSize, Val: 65536},
		{ID: http2.SettingEnablePush, Val: 0},
		{ID: http2.SettingInitialWindowSize, Val: 131072},
		{ID: http2.SettingMaxFrameSize, Val: 16384},
	},
	ConnWindowIncrement: 12517377,
	PseudoOrder:         "mpas",
	Priority:            &http2.PriorityParam{StreamDep: 0, Exclusive: false, Weight: 41},
	HeaderOrder: []string{
		"user-agent", "accept", "accept-language", "accept-encoding", "referer", "origin", "cookie",
		"upgrade-insecure-requests", "sec-fetch-dest", "sec-fetch-mode", "sec-fetch-site", "sec-fetch-user", "priority", "te",
	},
}

var safariH2 = &H2Profile{
	Settings: []http2.Setting{
		{ID: http2.SettingInitialWindowSize, Val: 4194304},
		{ID: http2.SettingMaxConcurrentStreams, Val: 100},
	},
	ConnWindowIncrement: 10485760,
	PseudoOrder:         "mspa",
	Priority:            &http2.PriorityParam{StreamDep: 0, Exclusive: false, Weight: 254},
	HeaderOrder:         []string{"accept", "sec-fetch-site", "cookie", "sec-fetch-dest", "accept-language", "sec-fetch-mode", "user-agent", "referer", "accept-encoding"},
}
