// Package sabr — экспериментальный каркас протокола YouTube SABR (Server ABR):
// разбор контейнера UMP (Universal Media Playback) и примитивы protobuf для
// сборки запроса VideoPlaybackAbrRequest.
//
// ВНИМАНИЕ: каркас не подключён к загрузке. Полный цикл SABR (сборка запроса,
// обмен, инициализация форматов) требует PO Token и проверки против живого
// YouTube, которой пока нет. Здесь реализованы и покрыты тестами независимые,
// проверяемые части: кодек UMP-varint, итератор частей UMP и protobuf-wire.
package sabr

import (
	"encoding/binary"
	"errors"
)

// Типы частей UMP, используемые SABR (подмножество).
const (
	PartOnesieHeader           = 10
	PartOnesieData             = 11
	PartMediaHeader            = 20
	PartMedia                  = 21
	PartMediaEnd               = 22
	PartLiveMetadata           = 31
	PartNextRequestPolicy      = 35
	PartFormatInitMetadata     = 42
	PartSabrRedirect           = 43
	PartStreamProtectionStatus = 45
	PartSabrContextUpdate      = 57
	PartSabrError              = 58
)

// ErrShortBuffer — данных не хватает для разбора очередной части/varint.
var ErrShortBuffer = errors.New("sabr: short buffer")

// umpVarintLen возвращает полную длину UMP-varint по первому байту. Число
// ведущих единиц в b0 задаёт количество байт (1..5).
func umpVarintLen(b0 byte) int {
	switch {
	case b0 < 0x80:
		return 1
	case b0 < 0xc0:
		return 2
	case b0 < 0xe0:
		return 3
	case b0 < 0xf0:
		return 4
	default:
		return 5
	}
}

// readUmpVarint читает UMP-varint из начала b и возвращает значение и число
// прочитанных байт. При нехватке данных n == 0.
func readUmpVarint(b []byte) (uint64, int) {
	if len(b) == 0 {
		return 0, 0
	}
	n := umpVarintLen(b[0])
	if len(b) < n {
		return 0, 0
	}
	switch n {
	case 1:
		return uint64(b[0]), 1
	case 2:
		return uint64(b[0]&0x3f) | uint64(b[1])<<6, 2
	case 3:
		return uint64(b[0]&0x1f) | uint64(b[1])<<5 | uint64(b[2])<<13, 3
	case 4:
		return uint64(b[0]&0x0f) | uint64(b[1])<<4 | uint64(b[2])<<12 | uint64(b[3])<<20, 4
	default:
		return uint64(binary.LittleEndian.Uint32(b[1:5])), 5
	}
}

// appendUmpVarint дописывает v в кодировке UMP-varint.
func appendUmpVarint(dst []byte, v uint64) []byte {
	switch {
	case v < 1<<7:
		return append(dst, byte(v))
	case v < 1<<14:
		return append(dst, byte(0x80|(v&0x3f)), byte(v>>6))
	case v < 1<<21:
		return append(dst, byte(0xc0|(v&0x1f)), byte(v>>5), byte(v>>13))
	case v < 1<<28:
		return append(dst, byte(0xe0|(v&0x0f)), byte(v>>4), byte(v>>12), byte(v>>20))
	default:
		var tmp [5]byte
		tmp[0] = 0xf0
		binary.LittleEndian.PutUint32(tmp[1:], uint32(v))
		return append(dst, tmp[:]...)
	}
}

// Part — одна часть UMP: тип и полезная нагрузка.
type Part struct {
	Type    int
	Payload []byte
}

// ParseParts разбирает поток UMP на части: [varint type][varint size][payload].
// Возвращает ошибку при усечённых данных.
func ParseParts(b []byte) ([]Part, error) {
	var parts []Part
	for len(b) > 0 {
		typ, n := readUmpVarint(b)
		if n == 0 {
			return parts, ErrShortBuffer
		}
		b = b[n:]
		size, n := readUmpVarint(b)
		if n == 0 {
			return parts, ErrShortBuffer
		}
		b = b[n:]
		if uint64(len(b)) < size {
			return parts, ErrShortBuffer
		}
		payload := make([]byte, size)
		copy(payload, b[:size])
		b = b[size:]
		parts = append(parts, Part{Type: int(typ), Payload: payload})
	}
	return parts, nil
}

// WriteParts собирает части обратно в поток UMP (для тестов и отладки).
func WriteParts(parts []Part) []byte {
	var out []byte
	for _, p := range parts {
		out = appendUmpVarint(out, uint64(p.Type))
		out = appendUmpVarint(out, uint64(len(p.Payload)))
		out = append(out, p.Payload...)
	}
	return out
}

// Media собирает полезные нагрузки всех частей MEDIA в один поток (тело
// медиасегмента). Заголовки (MEDIA_HEADER) и служебные части пропускаются.
func Media(parts []Part) []byte {
	var out []byte
	for _, p := range parts {
		if p.Type == PartMedia {
			out = append(out, p.Payload...)
		}
	}
	return out
}
