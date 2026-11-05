package sabr

import "encoding/binary"

// Минимальный кодек protobuf wire-формата — достаточно для сборки запроса
// VideoPlaybackAbrRequest и разбора заголовков частей UMP без внешней
// зависимости и .proto-файлов.

// Wire-типы protobuf.
const (
	WireVarint = 0
	WireI64    = 1
	WireLen    = 2
	WireI32    = 5
)

// PBWriter строит protobuf-сообщение по номерам полей.
type PBWriter struct{ buf []byte }

func (w *PBWriter) tag(field, wire int) {
	w.buf = appendProtoVarint(w.buf, uint64(field)<<3|uint64(wire))
}

// Varint записывает поле-varint (int/uint/bool).
func (w *PBWriter) Varint(field int, v uint64) {
	w.tag(field, WireVarint)
	w.buf = appendProtoVarint(w.buf, v)
}

// Bytes записывает length-delimited поле (строка/байты/вложенное сообщение).
func (w *PBWriter) Bytes(field int, b []byte) {
	w.tag(field, WireLen)
	w.buf = appendProtoVarint(w.buf, uint64(len(b)))
	w.buf = append(w.buf, b...)
}

// Message записывает вложенное сообщение.
func (w *PBWriter) Message(field int, fn func(*PBWriter)) {
	var sub PBWriter
	fn(&sub)
	w.Bytes(field, sub.buf)
}

// Fixed32/Fixed64 — поля фиксированной длины.
func (w *PBWriter) Fixed32(field int, v uint32) {
	w.tag(field, WireI32)
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	w.buf = append(w.buf, b[:]...)
}

func (w *PBWriter) Fixed64(field int, v uint64) {
	w.tag(field, WireI64)
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	w.buf = append(w.buf, b[:]...)
}

// Bytes возвращает собранное сообщение.
func (w *PBWriter) Buffer() []byte { return w.buf }

func appendProtoVarint(dst []byte, v uint64) []byte {
	for v >= 0x80 {
		dst = append(dst, byte(v)|0x80)
		v >>= 7
	}
	return append(dst, byte(v))
}

// PBField — разобранное protobuf-поле.
type PBField struct {
	Field int
	Wire  int
	// Varint для WireVarint/I32/I64; Bytes для WireLen.
	Varint uint64
	Bytes  []byte
}

// ParseProto разбирает protobuf-сообщение в последовательность полей (без
// схемы). Удобно для инспекции ответов SABR.
func ParseProto(b []byte) ([]PBField, error) {
	var out []PBField
	for len(b) > 0 {
		key, n := readProtoVarint(b)
		if n == 0 {
			return out, ErrShortBuffer
		}
		b = b[n:]
		f := PBField{Field: int(key >> 3), Wire: int(key & 7)}
		switch f.Wire {
		case WireVarint:
			v, n := readProtoVarint(b)
			if n == 0 {
				return out, ErrShortBuffer
			}
			f.Varint, b = v, b[n:]
		case WireI64:
			if len(b) < 8 {
				return out, ErrShortBuffer
			}
			f.Varint, b = binary.LittleEndian.Uint64(b[:8]), b[8:]
		case WireI32:
			if len(b) < 4 {
				return out, ErrShortBuffer
			}
			f.Varint, b = uint64(binary.LittleEndian.Uint32(b[:4])), b[4:]
		case WireLen:
			size, n := readProtoVarint(b)
			if n == 0 {
				return out, ErrShortBuffer
			}
			b = b[n:]
			if uint64(len(b)) < size {
				return out, ErrShortBuffer
			}
			f.Bytes, b = b[:size], b[size:]
		default:
			return out, ErrShortBuffer
		}
		out = append(out, f)
	}
	return out, nil
}

func readProtoVarint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(b); i++ {
		v |= uint64(b[i]&0x7f) << (7 * i)
		if b[i] < 0x80 {
			return v, i + 1
		}
		if i >= 9 {
			break
		}
	}
	return 0, 0
}
