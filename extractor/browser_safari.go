package extractor

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"net/http"
	"os"
	"time"
)

func readSafariCookies(path string) (map[string][]*http.Cookie, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseSafariCookies(data)
}

// BinaryCookies contains a big-endian page table and little-endian records.
// https://github.com/libyal/dtformats/blob/main/documentation/Safari%20Cookies.asciidoc
func parseSafariCookies(data []byte) (map[string][]*http.Cookie, error) {
	invalid := errors.New("invalid Safari Cookies.binarycookies database")
	if len(data) < 8 || string(data[:4]) != "cook" {
		return nil, invalid
	}
	pages := uint64(binary.BigEndian.Uint32(data[4:8]))
	if pages > uint64((len(data)-8)/4) {
		return nil, invalid
	}
	byHost := map[string][]*http.Cookie{}
	start := uint64(8) + pages*4
	for i := uint64(0); i < pages; i++ {
		size := uint64(binary.BigEndian.Uint32(data[8+i*4:]))
		if size < 8 || size > uint64(len(data))-start {
			return nil, invalid
		}
		page := data[start : start+size]
		start += size
		if !bytes.Equal(page[:4], []byte{0, 0, 1, 0}) {
			return nil, invalid
		}
		count := uint64(binary.LittleEndian.Uint32(page[4:8]))
		if count > (size-8)/4 {
			return nil, invalid
		}
		for j := uint64(0); j < count; j++ {
			offset := uint64(binary.LittleEndian.Uint32(page[8+j*4:]))
			if offset < 8+count*4 || offset > size || size-offset < 56 {
				return nil, invalid
			}
			recordSize := uint64(binary.LittleEndian.Uint32(page[offset:]))
			if recordSize < 56 || recordSize > size-offset {
				return nil, invalid
			}
			record := page[offset : offset+recordSize]
			fields := make([]string, 4)
			for k := range fields {
				pos := uint64(binary.LittleEndian.Uint32(record[16+k*4:]))
				if pos < 56 || pos >= recordSize {
					return nil, invalid
				}
				end := bytes.IndexByte(record[pos:], 0)
				if end < 0 {
					return nil, invalid
				}
				fields[k] = string(record[pos : pos+uint64(end)])
			}
			flags := binary.LittleEndian.Uint32(record[8:12])
			expires := math.Float64frombits(binary.LittleEndian.Uint64(record[40:48]))
			if math.IsNaN(expires) || math.IsInf(expires, 0) || expires < -978307200 || expires > 253402300799-978307200 {
				return nil, invalid
			}
			appendBrowserCookie(byHost, fields[0], &http.Cookie{
				Name: fields[1], Path: fields[2], Value: fields[3],
				Secure: flags&1 != 0, HttpOnly: flags&4 != 0,
				Expires: time.Unix(int64(expires)+978307200, 0),
			})
		}
	}
	return byHost, nil
}
