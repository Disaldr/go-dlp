package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/Disaldr/go-dlp/media"
)

// HTTP — загрузчик прямых ссылок с докачкой .part-файла.
type HTTP struct{ opts Options }

// NewHTTP создаёт HTTP-загрузчик.
func NewHTTP(o Options) *HTTP { return &HTTP{opts: o} }

type retryableError struct{ error }

func (h *HTTP) newRequest(ctx context.Context, u string, headers map[string]string) (*http.Request, error) {
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
	return req, nil
}

// Download скачивает f.URL в dst.
func (h *HTTP) Download(ctx context.Context, f *media.Format, headers map[string]string, dst string) error {
	part := PartName(dst)
	tr := newTracker(h.opts.Progress, dst, f.FormatID, h.opts.RateLimit)
	if size := f.Size(); size > 0 {
		tr.setTotal(size, f.Filesize == 0)
	}

	chunk := f.HTTPChunkSize
	if chunk <= 0 {
		chunk = h.opts.ChunkSize
	}

	out, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()

	pos := fileSize(part)
	if _, err := out.Seek(pos, io.SeekStart); err != nil {
		return err
	}
	tr.setBaseline(pos)

	var total int64 = -1
	if f.Filesize > 0 {
		total = f.Filesize
	}
	for {
		if total >= 0 && pos >= total {
			break
		}
		end := int64(-1)
		if chunk > 0 {
			end = pos + chunk - 1
			if total > 0 && end >= total {
				end = total - 1
			}
		}
		var n int64
		var done bool
		var lastErr error
		for attempt := 0; attempt <= h.opts.retries(); attempt++ {
			if attempt > 0 {
				if err := sleepCtx(ctx, backoff(attempt)); err != nil {
					return err
				}
				// повтор продолжает с того места, где оборвалось
				pos = fileSize(part)
				if _, err := out.Seek(pos, io.SeekStart); err != nil {
					return err
				}
			}
			n, total, done, lastErr = h.fetchRange(ctx, f.URL, headers, out, tr, pos, end, total)
			if lastErr == nil {
				break
			}
			var re retryableError
			if !errors.As(lastErr, &re) || ctx.Err() != nil {
				return lastErr
			}
		}
		if lastErr != nil {
			return lastErr
		}
		if pos, err = out.Seek(0, io.SeekCurrent); err != nil {
			return err
		}
		if total > 0 {
			tr.setTotal(total, false)
		}
		if done || (chunk <= 0) {
			break
		}
		if n == 0 {
			break
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	tr.finish()
	return os.Rename(part, dst)
}

// fetchRange скачивает [start, end] (end<0 — до конца). Возвращает число байт,
// полный размер (если стал известен) и признак, что файл докачан полностью.
func (h *HTTP) fetchRange(ctx context.Context, u string, headers map[string]string, out *os.File, tr *tracker, start, end, total int64) (int64, int64, bool, error) {
	req, err := h.newRequest(ctx, u, headers)
	if err != nil {
		return 0, total, false, err
	}
	if start > 0 || end >= 0 {
		r := fmt.Sprintf("bytes=%d-", start)
		if end >= 0 {
			r += strconv.FormatInt(end, 10)
		}
		req.Header.Set("Range", r)
	}
	resp, err := h.opts.client().Do(req)
	if err != nil {
		return 0, total, false, retryableError{err}
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		// уже всё скачано
		if t := contentRangeTotal(resp.Header.Get("Content-Range")); t > 0 && start >= t {
			return 0, t, true, nil
		}
		return 0, total, false, fmt.Errorf("HTTP 416 for range starting at %d", start)
	case resp.StatusCode == http.StatusOK:
		if start > 0 {
			// сервер проигнорировал Range — начинаем заново
			if err := out.Truncate(0); err != nil {
				return 0, total, false, err
			}
			if _, err := out.Seek(0, io.SeekStart); err != nil {
				return 0, total, false, err
			}
			tr.setBaseline(0)
			start = 0
		}
		if resp.ContentLength > 0 {
			total = resp.ContentLength
		}
	case resp.StatusCode == http.StatusPartialContent:
		if t := contentRangeTotal(resp.Header.Get("Content-Range")); t > 0 {
			total = t
		}
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		return 0, total, false, retryableError{fmt.Errorf("HTTP %d", resp.StatusCode)}
	default:
		return 0, total, false, fmt.Errorf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	if total > 0 {
		tr.setTotal(total, false)
	}
	n, err := io.Copy(&countingWriter{w: out, t: tr}, resp.Body)
	if err != nil {
		return n, total, false, retryableError{err}
	}
	if resp.StatusCode == http.StatusOK {
		return n, total, true, nil
	}
	done := total > 0 && start+n >= total
	if end >= 0 && n < end-start+1 && !done {
		// сервер вернул меньше, чем просили, хотя файл не закончился
		if total < 0 {
			done = true
		}
	}
	return n, total, done, nil
}

func contentRangeTotal(cr string) int64 {
	// bytes 0-99/1234 или bytes */1234
	if i := strings.LastIndexByte(cr, '/'); i >= 0 {
		t, err := strconv.ParseInt(cr[i+1:], 10, 64)
		if err == nil {
			return t
		}
	}
	return -1
}
