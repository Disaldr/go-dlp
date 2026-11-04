package downloader

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/Disaldr/go-dlp/dash"
	"github.com/Disaldr/go-dlp/hls"
	"github.com/Disaldr/go-dlp/media"
)

// HLS — нативный загрузчик HLS: сегменты качаются параллельно,
// пишутся по порядку в один файл (MPEG-TS или fMP4).
type HLS struct{ opts Options }

// NewHLS создаёт HLS-загрузчик.
func NewHLS(o Options) *HLS { return &HLS{opts: o} }

func (h *HLS) get(ctx context.Context, u string, headers map[string]string, off, length int64) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= h.opts.retries(); attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, backoff(attempt)); err != nil {
				return nil, err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		if h.opts.UserAgent != "" {
			req.Header.Set("User-Agent", h.opts.UserAgent)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if length > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+length-1))
		}
		resp, err := h.opts.client().Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, u)
			if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
				return nil, lastErr
			}
			continue
		}
		if err != nil {
			lastErr = err
			continue
		}
		return body, nil
	}
	return nil, lastErr
}

type keyCache struct {
	mu   sync.Mutex
	keys map[string][]byte
}

func (k *keyCache) get(ctx context.Context, h *HLS, uri string, headers map[string]string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if b, ok := k.keys[uri]; ok {
		return b, nil
	}
	b, err := h.get(ctx, uri, headers, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("hls: key: %w", err)
	}
	if len(b) != 16 {
		return nil, fmt.Errorf("hls: unexpected key length %d", len(b))
	}
	k.keys[uri] = b
	return b, nil
}

func decrypt(key []byte, seg hls.Segment, data []byte) ([]byte, error) {
	iv := seg.Key.IV
	if iv == nil {
		iv = make([]byte, 16)
		binary.BigEndian.PutUint64(iv[8:], uint64(seg.Seq))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(data)%aes.BlockSize != 0 {
		return nil, errors.New("hls: encrypted segment is not block-aligned")
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, data)
	// PKCS#7
	if n := len(out); n > 0 {
		pad := int(out[n-1])
		if pad > 0 && pad <= aes.BlockSize && pad <= n && bytes.Equal(out[n-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
			out = out[:n-pad]
		}
	}
	return out, nil
}

// Download скачивает HLS media-плейлист (f.URL) в dst.
// Для live-плейлистов (без EXT-X-ENDLIST) плейлист перечитывается,
// пока трансляция не закончится или не будет отменён ctx.
func (h *HLS) Download(ctx context.Context, f *media.Format, headers map[string]string, dst string) error {
	body, err := h.get(ctx, f.URL, headers, 0, 0)
	if err != nil {
		return err
	}
	pl, err := hls.ParseMedia(string(body), f.URL)
	if err != nil {
		// это может оказаться master-плейлист — берём лучший вариант
		if hls.IsMaster(string(body)) {
			vs := hls.ParseMaster(string(body), f.URL, "hls")
			if len(vs) == 0 {
				return err
			}
			best := vs[0]
			for _, v := range vs {
				if v.TBR > best.TBR {
					best = v
				}
			}
			cp := *f
			cp.URL = best.URL
			return h.Download(ctx, &cp, headers, dst)
		}
		return err
	}
	for _, s := range pl.Segments {
		if s.Key != nil && s.Key.Method != "AES-128" {
			return fmt.Errorf("hls: unsupported encryption %q (DRM?)", s.Key.Method)
		}
	}

	part := PartName(dst)
	out, err := os.Create(part)
	if err != nil {
		return err
	}
	defer out.Close()

	tr := newTracker(h.opts.Progress, dst, f.FormatID, h.opts.RateLimit)
	keys := &keyCache{keys: map[string][]byte{}}

	if pl.Init != nil {
		data, err := h.fetchSegment(ctx, *pl.Init, headers, keys)
		if err != nil {
			return fmt.Errorf("hls: init segment: %w", err)
		}
		if _, err := out.Write(data); err != nil {
			return err
		}
	}

	written := map[int64]bool{}
	for {
		var todo []hls.Segment
		for _, s := range pl.Segments {
			if !written[s.Seq] {
				todo = append(todo, s)
			}
		}
		if err := h.downloadSegments(ctx, todo, headers, keys, out, tr, len(written)); err != nil {
			if !pl.EndList && ctx.Err() != nil {
				break // остановка записи эфира — сохраняем скачанное
			}
			return err
		}
		for _, s := range todo {
			written[s.Seq] = true
		}
		if pl.EndList {
			break
		}
		wait := time.Duration(pl.TargetDuration * float64(time.Second))
		if wait <= 0 {
			wait = 5 * time.Second
		}
		if err := sleepCtx(ctx, wait); err != nil {
			// отмена live-записи — сохраняем то, что успели скачать
			break
		}
		body, err := h.get(ctx, f.URL, headers, 0, 0)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			return err
		}
		if pl, err = hls.ParseMedia(string(body), f.URL); err != nil {
			return err
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	tr.finish()
	return os.Rename(part, dst)
}

func (h *HLS) fetchSegment(ctx context.Context, s hls.Segment, headers map[string]string, keys *keyCache) ([]byte, error) {
	data, err := h.get(ctx, s.URI, headers, s.Offset, s.Length)
	if err != nil {
		return nil, err
	}
	if s.Key != nil && s.Key.Method == "AES-128" {
		key, err := keys.get(ctx, h, s.Key.URI, headers)
		if err != nil {
			return nil, err
		}
		return decrypt(key, s, data)
	}
	return data, nil
}

// downloadSegments качает сегменты в N потоков и пишет их строго по порядку.
func (h *HLS) downloadSegments(ctx context.Context, segs []hls.Segment, headers map[string]string, keys *keyCache, out io.Writer, tr *tracker, already int) error {
	if len(segs) == 0 {
		return nil
	}
	workers := h.opts.Fragments
	if workers <= 0 {
		workers = 1
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type result struct {
		data []byte
		err  error
	}
	results := make([]chan result, len(segs))
	for i := range results {
		results[i] = make(chan result, 1)
	}
	sem := make(chan struct{}, workers)
	go func() {
		for i, s := range segs {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i] <- result{err: ctx.Err()}
				continue
			}
			go func(i int, s hls.Segment) {
				data, err := h.fetchSegment(ctx, s, headers, keys)
				results[i] <- result{data, err}
			}(i, s)
		}
	}()

	total := already + len(segs)
	var avg float64
	var bytesDone int64
	for i := range segs {
		r := <-results[i]
		<-sem
		if r.err != nil {
			return fmt.Errorf("hls: fragment %d/%d: %w", already+i+1, total, r.err)
		}
		if _, err := out.Write(r.data); err != nil {
			return err
		}
		bytesDone += int64(len(r.data))
		avg = float64(bytesDone) / float64(i+1)
		tr.setFragment(already+i+1, total)
		tr.estimateRemaining(int64(len(r.data)) + int64(avg*float64(len(segs)-i-1)))
		tr.add(int64(len(r.data)))
	}
	return nil
}

// Fragments — загрузчик фрагментированных форматов (DASH): сегменты из
// Format.Fragments качаются параллельно и склеиваются по порядку
// (init-сегмент + медиасегменты = корректный fMP4/WebM).
type Fragments struct{ opts Options }

// NewFragments создаёт загрузчик фрагментов.
func NewFragments(o Options) *Fragments { return &Fragments{opts: o} }

// Download скачивает f.Fragments в dst.
func (fd *Fragments) Download(ctx context.Context, f *media.Format, headers map[string]string, dst string) error {
	if len(f.Fragments) == 0 {
		return fmt.Errorf("dash: format %s has no fragments", f.FormatID)
	}
	part := PartName(dst)
	out, err := os.Create(part)
	if err != nil {
		return err
	}
	defer out.Close()
	h := &HLS{opts: fd.opts}
	tr := newTracker(fd.opts.Progress, dst, f.FormatID, fd.opts.RateLimit)
	keys := &keyCache{keys: map[string][]byte{}}

	if !f.Live {
		if err := h.downloadSegments(ctx, toSegments(f.Fragments), headers, keys, out, tr, 0); err != nil {
			return err
		}
	} else if err := fd.downloadLive(ctx, h, f, headers, keys, out, tr); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	tr.finish()
	return os.Rename(part, dst)
}

func toSegments(frs []media.Fragment) []hls.Segment {
	segs := make([]hls.Segment, len(frs))
	for i, fr := range frs {
		segs[i] = hls.Segment{URI: fr.URL, Duration: fr.Duration, Seq: int64(i), Offset: fr.Offset, Length: fr.Length}
	}
	return segs
}

// liveIdleLimit — сколько обновлений подряд без новых сегментов считать концом эфира.
const liveIdleLimit = 10

// downloadLive записывает живой DASH: качает доступные сегменты, затем
// перечитывает манифест и докачивает новые, пока эфир не закончится
// (манифест стал static или долго нет новых сегментов) или не отменён ctx.
// Отмена ctx — штатная остановка: уже скачанное сохраняется.
func (fd *Fragments) downloadLive(ctx context.Context, h *HLS, f *media.Format, headers map[string]string, keys *keyCache, out io.Writer, tr *tracker) error {
	seen := map[string]bool{}
	frags := f.Fragments
	update := 2 * time.Second
	idle := 0
	count := 0
	for {
		var todo []media.Fragment
		for _, fr := range frags {
			key := fmt.Sprintf("%s|%d|%d", fr.URL, fr.Offset, fr.Length)
			if seen[key] || (fr.Init && count > 0) {
				continue
			}
			seen[key] = true
			todo = append(todo, fr)
		}
		if len(todo) > 0 {
			idle = 0
			if err := h.downloadSegments(ctx, toSegments(todo), headers, keys, out, tr, count); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			count += len(todo)
			if d := todo[len(todo)-1].Duration; d > 0 {
				update = time.Duration(d * float64(time.Second))
			}
		} else if idle++; idle >= liveIdleLimit {
			return nil
		}
		if err := sleepCtx(ctx, update); err != nil {
			return nil
		}
		body, err := h.get(ctx, f.ManifestURL, headers, 0, 0)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("dash: refresh manifest: %w", err)
		}
		m, err := dash.ParseManifest(body, f.ManifestURL, "dash")
		if err != nil {
			return fmt.Errorf("dash: refresh manifest: %w", err)
		}
		if m.MinimumUpdatePeriod > 0 && m.MinimumUpdatePeriod < update {
			update = m.MinimumUpdatePeriod
		}
		frags = nil
		for _, nf := range m.Formats {
			if nf.DASHRepresentation == f.DASHRepresentation && nf.ACodec == f.ACodec && nf.VCodec == f.VCodec {
				frags = nf.Fragments
				break
			}
		}
		if !m.Dynamic {
			// эфир закончился: докачиваем только сегменты после последнего скачанного
			// (static-манифест может перечислять весь эфир с начала)
			last := -1
			for i, fr := range frags {
				if seen[fmt.Sprintf("%s|%d|%d", fr.URL, fr.Offset, fr.Length)] {
					last = i
				}
			}
			var tail []media.Fragment
			if last >= 0 {
				for _, fr := range frags[last+1:] {
					if !fr.Init {
						tail = append(tail, fr)
					}
				}
			}
			if err := h.downloadSegments(ctx, toSegments(tail), headers, keys, out, tr, count); err != nil && ctx.Err() == nil {
				return err
			}
			return nil
		}
	}
}
