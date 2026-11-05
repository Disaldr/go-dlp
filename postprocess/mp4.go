package postprocess

// Склейка MP4 на чистом Go без перекодирования. Поддерживаются два вида
// исходников (одна дорожка на файл — типичный случай bestvideo+bestaudio):
//
//   - фрагментированный MP4 (empty_moov + moof/mdat, как у adaptive-форматов
//     YouTube): пары moof+mdat копируются дословно (data_offset в trun задан
//     относительно начала moof, поэтому при сохранении раскладки остаётся
//     верным); патчатся только track_ID в tfhd и sequence_number в mfhd;
//   - прогрессивный MP4 (единый mdat со stco): данные дорожек переносятся в
//     новый mdat, а смещения чанков в stco/co64 пересчитываются.
//
// Смешивать фрагментированные и прогрессивные входы нельзя — возвращается
// ErrUnsupportedMux (вызывающий может прибегнуть к ffmpeg).

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	mp4 "github.com/abema/go-mp4"
)

func be32(b []byte) uint32           { return binary.BigEndian.Uint32(b) }
func be64(b []byte) uint64           { return binary.BigEndian.Uint64(b) }
func pb32(b []byte, o int, v uint32) { binary.BigEndian.PutUint32(b[o:], v) }
func pb64(b []byte, o int, v uint64) { binary.BigEndian.PutUint64(b[o:], v) }

// breg — область box внутри слайса: start — смещение заголовка, size — полный
// размер box, hdr — длина заголовка (8 или 16).
type breg struct {
	typ         string
	start, size int
	hdr         int
}

func hdrLen(b []byte, off int) int {
	if be32(b[off:]) == 1 {
		return 16
	}
	return 8
}

// scanAt сканирует последовательность box в b начиная с start.
func scanAt(b []byte, start int) []breg {
	var rs []breg
	for off := start; off+8 <= len(b); {
		sz := int(be32(b[off:]))
		typ := string(b[off+4 : off+8])
		hh := 8
		switch {
		case sz == 1:
			if off+16 > len(b) {
				return rs
			}
			sz = int(be64(b[off+8:]))
			hh = 16
		case sz == 0:
			sz = len(b) - off
		}
		if sz < hh || off+sz > len(b) {
			return rs
		}
		rs = append(rs, breg{typ, off, sz, hh})
		off += sz
	}
	return rs
}

// childrenOf возвращает дочерние box внутри box-слайса b.
func childrenOf(b []byte) []breg { return scanAt(b, hdrLen(b, 0)) }

func findChild(b []byte, typ string) (breg, bool) {
	for _, r := range childrenOf(b) {
		if r.typ == typ {
			return r, true
		}
	}
	return breg{}, false
}

// findPath спускается по именам, возвращая абсолютную область внутри b.
func findPath(b []byte, path ...string) (breg, bool) {
	abs := 0
	cur := b
	var last breg
	for _, p := range path {
		r, ok := findChild(cur, p)
		if !ok {
			return breg{}, false
		}
		abs += r.start
		cur = b[abs : abs+r.size]
		last = breg{p, abs, r.size, r.hdr}
	}
	return last, true
}

// fits сообщает, что в box r полезной нагрузки хватает ещё на need байт после
// заголовка (для безопасного чтения/записи полей по фиксированным смещениям).
func (r breg) fits(need int) bool { return r.size >= r.hdr+need }

func makeBox(typ string, parts ...[]byte) []byte {
	n := 8
	for _, p := range parts {
		n += len(p)
	}
	b := make([]byte, 8, n)
	pb32(b, 0, uint32(n))
	copy(b[4:8], typ)
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}

func sub(b []byte, r breg) []byte {
	cp := make([]byte, r.size)
	copy(cp, b[r.start:r.start+r.size])
	return cp
}

// mp4in — разобранный исходный файл.
type mp4in struct {
	f          *os.File
	ftyp       []byte
	moov       []byte
	fragmented bool
	frags      []fragPair // пары moof/mdat (фрагментированный)
	probe      *mp4.ProbeInfo
}

type fragPair struct {
	moof     breg // область внутри файла (start — абсолютное смещение)
	mdatOff  int64
	mdatSize int64
}

func parseMP4(path string) (*mp4in, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	size := st.Size()
	in := &mp4in{f: f}
	hdr := make([]byte, 16)
	var pendingMoof *fragPair
	for off := int64(0); off < size; {
		if _, err := f.ReadAt(hdr[:8], off); err != nil {
			break
		}
		bsize := int64(be32(hdr[:4]))
		typ := string(hdr[4:8])
		hh := int64(8)
		switch {
		case bsize == 1:
			if _, err := f.ReadAt(hdr[8:16], off+8); err != nil {
				break
			}
			bsize = int64(be64(hdr[8:16]))
			hh = 16
		case bsize == 0:
			bsize = size - off
		}
		if bsize < hh || off+bsize > size {
			break
		}
		switch typ {
		case "ftyp":
			in.ftyp = make([]byte, bsize)
			if _, err := f.ReadAt(in.ftyp, off); err != nil {
				f.Close()
				return nil, err
			}
		case "moov":
			in.moov = make([]byte, bsize)
			if _, err := f.ReadAt(in.moov, off); err != nil {
				f.Close()
				return nil, err
			}
		case "moof":
			pendingMoof = &fragPair{moof: breg{"moof", int(off), int(bsize), int(hh)}}
		case "mdat":
			if pendingMoof != nil {
				pendingMoof.mdatOff = off
				pendingMoof.mdatSize = bsize
				in.frags = append(in.frags, *pendingMoof)
				pendingMoof = nil
			}
		}
		off += bsize
	}
	if in.moov == nil {
		f.Close()
		return nil, fmt.Errorf("%w: no moov in %s", ErrUnsupportedMux, path)
	}
	_, in.fragmented = findChild(in.moov, "mvex")
	if !in.fragmented {
		if _, err := f.Seek(0, io.SeekStart); err == nil {
			in.probe, _ = mp4.Probe(f)
		}
	}
	return in, nil
}

func (in *mp4in) close() {
	if in.f != nil {
		in.f.Close()
	}
}

// patchTkhdID переписывает track_ID в tkhd внутри копии trak.
func patchTkhdID(trak []byte, id uint32) error {
	r, ok := findChild(trak, "tkhd")
	if !ok {
		return fmt.Errorf("%w: trak without tkhd", ErrUnsupportedMux)
	}
	if !r.fits(1) {
		return fmt.Errorf("%w: tkhd too small", ErrUnsupportedMux)
	}
	ver := trak[r.start+r.hdr]
	// v0: ver/flags(4) + creation(4) + modification(4) + track_ID(4)
	// v1: ver/flags(4) + creation(8) + modification(8) + track_ID(4)
	o := r.start + r.hdr + 4 + 8
	need := 4 + 8 + 4
	if ver == 1 {
		o = r.start + r.hdr + 4 + 16
		need = 4 + 16 + 4
	}
	if !r.fits(need) {
		return fmt.Errorf("%w: tkhd too small for track_ID", ErrUnsupportedMux)
	}
	pb32(trak, o, id)
	return nil
}

func mergeMP4(out string, inputs []Input) error {
	var ins []*mp4in
	defer func() {
		for _, in := range ins {
			in.close()
		}
	}()
	fragCount := 0
	for _, inp := range inputs {
		in, err := parseMP4(inp.Path)
		if err != nil {
			return err
		}
		ins = append(ins, in)
		if in.fragmented {
			fragCount++
		}
	}
	switch fragCount {
	case len(ins):
		return mergeMP4Fragmented(out, ins)
	case 0:
		return mergeMP4Progressive(out, ins)
	default:
		return fmt.Errorf("%w: mixed fragmented and progressive inputs", ErrUnsupportedMux)
	}
}

// buildTraks вытаскивает по одной копии trak из каждого входа, нумеруя
// track_ID подряд с 1. Возвращает также next_track_id.
func buildTraks(ins []*mp4in) ([][]byte, uint32, error) {
	var traks [][]byte
	id := uint32(1)
	for _, in := range ins {
		r, ok := findChild(in.moov, "trak")
		if !ok {
			return nil, 0, fmt.Errorf("%w: moov without trak", ErrUnsupportedMux)
		}
		trak := sub(in.moov, r)
		if err := patchTkhdID(trak, id); err != nil {
			return nil, 0, err
		}
		traks = append(traks, trak)
		id++
	}
	return traks, id, nil
}

func mergeMP4Fragmented(out string, ins []*mp4in) error {
	traks, nextID, err := buildTraks(ins)
	if err != nil {
		return err
	}
	// mvex из trex каждого входа с перенумерованными track_ID.
	var trexes [][]byte
	for i, in := range ins {
		mvex, ok := findChild(in.moov, "mvex")
		if !ok {
			return fmt.Errorf("%w: fragmented moov without mvex", ErrUnsupportedMux)
		}
		mvexBytes := in.moov[mvex.start : mvex.start+mvex.size]
		tr, ok := findChild(mvexBytes, "trex")
		if !ok {
			return fmt.Errorf("%w: mvex without trex", ErrUnsupportedMux)
		}
		if !tr.fits(8) {
			return fmt.Errorf("%w: trex too small", ErrUnsupportedMux)
		}
		trex := sub(mvexBytes, tr)
		pb32(trex, tr.hdr+4, uint32(i+1)) // track_ID
		trexes = append(trexes, trex)
	}
	mvhd := mvhdFrom(ins[0].moov)
	if mvhd == nil {
		return fmt.Errorf("%w: no mvhd", ErrUnsupportedMux)
	}
	pb32(mvhd, len(mvhd)-4, nextID)

	moovParts := [][]byte{mvhd}
	moovParts = append(moovParts, traks...)
	moovParts = append(moovParts, makeBox("mvex", trexes...))
	moov := makeBox("moov", moovParts...)

	of, err := os.Create(out)
	if err != nil {
		return err
	}
	defer of.Close()
	w := io.Writer(of)
	if _, err := w.Write(ins[0].ftyp); err != nil {
		return err
	}
	if _, err := w.Write(moov); err != nil {
		return err
	}
	seq := uint32(1)
	for i, in := range ins {
		id := uint32(i + 1)
		for _, fr := range in.frags {
			moof := make([]byte, fr.moof.size)
			if _, err := in.f.ReadAt(moof, int64(fr.moof.start)); err != nil {
				return err
			}
			patchMoof(moof, id, seq)
			seq++
			if _, err := w.Write(moof); err != nil {
				return err
			}
			if _, err := io.Copy(w, io.NewSectionReader(in.f, fr.mdatOff, fr.mdatSize)); err != nil {
				return err
			}
		}
	}
	return nil
}

// patchMoof переписывает sequence_number в mfhd и track_ID во всех tfhd.
func patchMoof(moof []byte, trackID, seq uint32) {
	if r, ok := findChild(moof, "mfhd"); ok && r.fits(8) {
		pb32(moof, r.start+r.hdr+4, seq) // ver/flags(4) + sequence_number(4)
	}
	for _, traf := range childrenOf(moof) {
		if traf.typ != "traf" {
			continue
		}
		trafBytes := moof[traf.start : traf.start+traf.size]
		if r, ok := findChild(trafBytes, "tfhd"); ok && r.fits(8) {
			pb32(moof, traf.start+r.start+r.hdr+4, trackID) // ver/flags(4) + track_ID(4)
		}
	}
}

func mvhdFrom(moov []byte) []byte {
	r, ok := findChild(moov, "mvhd")
	if !ok {
		return nil
	}
	return sub(moov, r)
}

func mergeMP4Progressive(out string, ins []*mp4in) error {
	traks, nextID, err := buildTraks(ins)
	if err != nil {
		return err
	}
	// Для каждого входа — план данных (чанки) и область stco/co64 в копии trak.
	type plan struct {
		in         *mp4in
		trak       []byte
		refs       []bref
		entriesOff int // смещение первой записи stco/co64 внутри trak
		wide       bool
	}
	var plans []plan
	for i, in := range ins {
		if in.probe == nil || len(in.probe.Tracks) == 0 {
			return fmt.Errorf("%w: cannot probe %s", ErrUnsupportedMux, "input")
		}
		tr := in.probe.Tracks[0]
		refs, err := chunkRefs(tr)
		if err != nil {
			return err
		}
		trak := traks[i]
		stbl, ok := findPath(trak, "mdia", "minf", "stbl")
		if !ok {
			return fmt.Errorf("%w: no stbl", ErrUnsupportedMux)
		}
		stblBytes := trak[stbl.start : stbl.start+stbl.size]
		var cob breg
		wide := false
		if r, ok := findChild(stblBytes, "co64"); ok {
			cob, wide = r, true
		} else if r, ok := findChild(stblBytes, "stco"); ok {
			cob = r
		} else {
			return fmt.Errorf("%w: no stco/co64", ErrUnsupportedMux)
		}
		cobAbs := stbl.start + cob.start
		if !cob.fits(8) {
			return fmt.Errorf("%w: stco/co64 too small", ErrUnsupportedMux)
		}
		count := int(be32(trak[cobAbs+cob.hdr+4:]))
		if count != len(refs) {
			return fmt.Errorf("%w: chunk count mismatch (%d stco vs %d probed)", ErrUnsupportedMux, count, len(refs))
		}
		entrySize := 4
		if wide {
			entrySize = 8
		}
		// Записи должны целиком помещаться в box.
		if !cob.fits(8 + count*entrySize) {
			return fmt.Errorf("%w: stco/co64 entries exceed box size", ErrUnsupportedMux)
		}
		plans = append(plans, plan{in, trak, refs, cobAbs + cob.hdr + 8, wide})
	}

	mvhd := mvhdFrom(ins[0].moov)
	if mvhd == nil {
		return fmt.Errorf("%w: no mvhd", ErrUnsupportedMux)
	}
	pb32(mvhd, len(mvhd)-4, nextID)

	moovParts := [][]byte{mvhd}
	for _, p := range plans {
		moovParts = append(moovParts, p.trak)
	}
	moovLen := 8
	for _, p := range moovParts {
		moovLen += len(p)
	}
	var totalData int64
	for _, p := range plans {
		for _, r := range p.refs {
			totalData += r.size
		}
	}
	ftypLen := len(ins[0].ftyp)
	mdatHdr := 8
	if int64(ftypLen+moovLen)+8+totalData > 0xFFFFFFFF {
		mdatHdr = 16
	}
	dataStart := int64(ftypLen + moovLen + mdatHdr)

	// Назначаем новые смещения чанков по порядку дорожек.
	pos := dataStart
	for _, p := range plans {
		for i, r := range p.refs {
			if p.wide {
				pb64(p.trak, p.entriesOff+i*8, uint64(pos))
			} else {
				if pos > 0xFFFFFFFF {
					return fmt.Errorf("%w: offset exceeds 32 bits, needs co64", ErrUnsupportedMux)
				}
				pb32(p.trak, p.entriesOff+i*4, uint32(pos))
			}
			pos += r.size
		}
	}

	moov := makeBox("moov", moovParts...)
	if len(moov) != moovLen {
		return fmt.Errorf("moov length mismatch: %d != %d", len(moov), moovLen)
	}

	of, err := os.Create(out)
	if err != nil {
		return err
	}
	defer of.Close()
	if _, err := of.Write(ins[0].ftyp); err != nil {
		return err
	}
	if _, err := of.Write(moov); err != nil {
		return err
	}
	// mdat header.
	mh := make([]byte, mdatHdr)
	if mdatHdr == 16 {
		pb32(mh, 0, 1)
		copy(mh[4:8], "mdat")
		pb64(mh, 8, uint64(16)+uint64(totalData))
	} else {
		pb32(mh, 0, uint32(8+totalData))
		copy(mh[4:8], "mdat")
	}
	if _, err := of.Write(mh); err != nil {
		return err
	}
	for _, p := range plans {
		for _, r := range p.refs {
			if _, err := io.Copy(of, io.NewSectionReader(p.in.f, r.off, r.size)); err != nil {
				return err
			}
		}
	}
	return nil
}

// bref — область байтов в исходном файле.
type bref struct {
	off, size int64
}

func chunkRefs(tr *mp4.Track) ([]bref, error) {
	var refs []bref
	si := 0
	for _, ch := range tr.Chunks {
		var length int64
		for k := uint32(0); k < ch.SamplesPerChunk; k++ {
			if si >= len(tr.Samples) {
				return nil, fmt.Errorf("%w: sample index out of range", ErrUnsupportedMux)
			}
			length += int64(tr.Samples[si].Size)
			si++
		}
		refs = append(refs, bref{int64(ch.DataOffset), length})
	}
	return refs, nil
}
