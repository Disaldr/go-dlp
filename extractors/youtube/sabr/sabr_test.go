package sabr

import (
	"bytes"
	"testing"
)

func TestUmpVarintRoundTrip(t *testing.T) {
	vals := []uint64{0, 1, 127, 128, 200, 16383, 16384, 1<<21 - 1, 1 << 21, 1<<28 - 1, 1 << 28, 1<<32 - 1}
	for _, v := range vals {
		b := appendUmpVarint(nil, v)
		got, n := readUmpVarint(b)
		if n != len(b) {
			t.Errorf("v=%d: read %d bytes, encoded %d", v, n, len(b))
		}
		if got != v {
			t.Errorf("round-trip v=%d → %d", v, got)
		}
	}
}

func TestUmpVarintVectors(t *testing.T) {
	// Ручная проверка границ размеров против спецификации.
	cases := []struct {
		v    uint64
		size int
		b0   byte
	}{
		{100, 1, 100},
		{200, 2, 0x88}, // 0x80 | (200&0x3f=8)
		{16384, 3, 0xc0 | (16384 & 0x1f)},
		{1 << 21, 4, 0xe0 | ((1 << 21) & 0x0f)},
		{1 << 28, 5, 0xf0},
	}
	for _, c := range cases {
		b := appendUmpVarint(nil, c.v)
		if len(b) != c.size {
			t.Errorf("v=%d: size %d, want %d", c.v, len(b), c.size)
		}
		if b[0] != c.b0 {
			t.Errorf("v=%d: b0=%#x, want %#x", c.v, b[0], c.b0)
		}
	}
}

func TestReadUmpVarintShort(t *testing.T) {
	if _, n := readUmpVarint(nil); n != 0 {
		t.Error("empty must return n=0")
	}
	// Заявлен размер 3, но дан 1 байт.
	if _, n := readUmpVarint([]byte{0xc0}); n != 0 {
		t.Error("truncated varint must return n=0")
	}
}

func TestParseWriteParts(t *testing.T) {
	parts := []Part{
		{Type: PartMediaHeader, Payload: []byte{1, 2, 3}},
		{Type: PartMedia, Payload: bytes.Repeat([]byte{0xab}, 300)}, // >127 → многобайтовый size
		{Type: PartMedia, Payload: []byte{4, 5}},
		{Type: PartMediaEnd, Payload: nil},
	}
	enc := WriteParts(parts)
	got, err := ParseParts(enc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(parts) {
		t.Fatalf("got %d parts, want %d", len(got), len(parts))
	}
	for i := range parts {
		if got[i].Type != parts[i].Type || !bytes.Equal(got[i].Payload, parts[i].Payload) {
			t.Errorf("part %d mismatch: %+v vs %+v", i, got[i], parts[i])
		}
	}
	// Media склеивает только MEDIA-части.
	media := Media(got)
	want := append(bytes.Repeat([]byte{0xab}, 300), 4, 5)
	if !bytes.Equal(media, want) {
		t.Errorf("Media aggregation wrong: %d bytes", len(media))
	}
}

func TestParsePartsTruncated(t *testing.T) {
	// size=10, но payload короче.
	b := appendUmpVarint(nil, PartMedia)
	b = appendUmpVarint(b, 10)
	b = append(b, 1, 2, 3)
	if _, err := ParseParts(b); err != ErrShortBuffer {
		t.Errorf("err = %v, want ErrShortBuffer", err)
	}
}

func TestProtoRoundTrip(t *testing.T) {
	var w PBWriter
	w.Varint(1, 150)
	w.Bytes(2, []byte("hello"))
	w.Message(3, func(m *PBWriter) {
		m.Varint(1, 42)
		m.Fixed32(2, 0xdeadbeef)
	})
	w.Fixed64(4, 0x1122334455667788)

	fields, err := ParseProto(w.Buffer())
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 4 {
		t.Fatalf("got %d fields", len(fields))
	}
	if fields[0].Field != 1 || fields[0].Wire != WireVarint || fields[0].Varint != 150 {
		t.Errorf("field1 = %+v", fields[0])
	}
	if fields[1].Field != 2 || string(fields[1].Bytes) != "hello" {
		t.Errorf("field2 = %+v", fields[1])
	}
	// Вложенное сообщение разбирается рекурсивно.
	sub, err := ParseProto(fields[2].Bytes)
	if err != nil || len(sub) != 2 {
		t.Fatalf("submessage parse: %v %+v", err, sub)
	}
	if sub[0].Varint != 42 || sub[1].Varint != 0xdeadbeef {
		t.Errorf("submessage fields = %+v", sub)
	}
	if fields[3].Varint != 0x1122334455667788 {
		t.Errorf("field4 fixed64 = %#x", fields[3].Varint)
	}
}

func TestProtoVarintMultibyte(t *testing.T) {
	// 150 кодируется как 0x96 0x01.
	b := appendProtoVarint(nil, 150)
	if !bytes.Equal(b, []byte{0x96, 0x01}) {
		t.Errorf("appendProtoVarint(150) = %#x", b)
	}
	v, n := readProtoVarint(b)
	if v != 150 || n != 2 {
		t.Errorf("readProtoVarint = %d, %d", v, n)
	}
}
