// Package downloader скачивает выбранные форматы: прямые HTTP(S)-файлы
// (с докачкой и Range-чанками) и HLS (с параллельной загрузкой фрагментов).
package downloader

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/Disaldr/go-dlp/media"
)

// Status загрузки.
type Status string

const (
	StatusDownloading Status = "downloading"
	StatusFinished    Status = "finished"
)

// Progress — состояние загрузки.
type Progress struct {
	Status         Status
	Filename       string
	FormatID       string
	Downloaded     int64
	Total          int64 // 0 — неизвестно
	TotalEstimated bool
	Speed          float64 // байт/с
	ETA            time.Duration
	Elapsed        time.Duration
	FragmentIndex  int
	FragmentCount  int
}

// Percent — процент выполнения (-1 если неизвестно).
func (p Progress) Percent() float64 {
	if p.Total <= 0 {
		return -1
	}
	return float64(p.Downloaded) * 100 / float64(p.Total)
}

// ProgressFunc получает обновления прогресса.
type ProgressFunc func(Progress)

// Options — настройки загрузки.
type Options struct {
	HTTP      *http.Client
	UserAgent string
	// Retries — повторов на запрос/фрагмент.
	Retries int
	// ChunkSize — размер Range-чанка по умолчанию (0 — одним запросом).
	ChunkSize int64
	// Fragments — число параллельных загрузок HLS-фрагментов.
	Fragments int
	Progress  ProgressFunc
	// RateLimit — байт/с (0 — без ограничения).
	RateLimit int64
}

func (o *Options) retries() int {
	if o.Retries <= 0 {
		return 3
	}
	return o.Retries
}

func (o *Options) client() *http.Client {
	if o.HTTP != nil {
		return o.HTTP
	}
	return http.DefaultClient
}

// Downloader скачивает формат в файл.
type Downloader interface {
	Download(ctx context.Context, f *media.Format, headers map[string]string, dst string) error
}

// For возвращает загрузчик для протокола формата.
func For(f *media.Format, o Options) (Downloader, error) {
	switch f.Protocol {
	case media.ProtoHLS, "m3u8":
		return &HLS{opts: o}, nil
	case media.ProtoDASH:
		return &Fragments{opts: o}, nil
	case media.ProtoHTTPS, media.ProtoHTTP, "":
		return &HTTP{opts: o}, nil
	}
	return nil, fmt.Errorf("downloader: unsupported protocol %q", f.Protocol)
}

// tracker считает скорость и ETA, троттлит колбэк.
type tracker struct {
	mu       sync.Mutex
	fn       ProgressFunc
	p        Progress
	start    time.Time
	last     time.Time
	baseline int64 // уже скачано до старта (докачка)
	limit    int64
}

func newTracker(fn ProgressFunc, filename, formatID string, limit int64) *tracker {
	now := time.Now()
	return &tracker{fn: fn, start: now, limit: limit, p: Progress{Status: StatusDownloading, Filename: filename, FormatID: formatID}}
}

func (t *tracker) setTotal(total int64, estimated bool) {
	t.mu.Lock()
	t.p.Total, t.p.TotalEstimated = total, estimated
	t.mu.Unlock()
}

// estimateRemaining выставляет оценочный размер: скачано + remaining.
func (t *tracker) estimateRemaining(remaining int64) {
	t.mu.Lock()
	t.p.Total, t.p.TotalEstimated = t.p.Downloaded+remaining, true
	t.mu.Unlock()
}

func (t *tracker) setBaseline(n int64) {
	t.mu.Lock()
	t.baseline = n
	t.p.Downloaded = n
	t.mu.Unlock()
}

func (t *tracker) setFragment(i, n int) {
	t.mu.Lock()
	t.p.FragmentIndex, t.p.FragmentCount = i, n
	t.mu.Unlock()
}

func (t *tracker) add(n int64) {
	t.mu.Lock()
	t.p.Downloaded += n
	now := time.Now()
	elapsed := now.Sub(t.start)
	var sleep time.Duration
	if t.limit > 0 {
		want := time.Duration(float64(t.p.Downloaded-t.baseline) / float64(t.limit) * float64(time.Second))
		if want > elapsed {
			sleep = want - elapsed
		}
	}
	emit := t.fn != nil && now.Sub(t.last) >= 200*time.Millisecond
	var snap Progress
	if emit {
		t.last = now
		t.fill(now)
		snap = t.p
	}
	t.mu.Unlock()
	if emit {
		t.fn(snap)
	}
	if sleep > 0 {
		time.Sleep(sleep)
	}
}

func (t *tracker) fill(now time.Time) {
	t.p.Elapsed = now.Sub(t.start)
	if secs := t.p.Elapsed.Seconds(); secs > 0 {
		t.p.Speed = float64(t.p.Downloaded-t.baseline) / secs
	}
	if t.p.Speed > 0 && t.p.Total > t.p.Downloaded {
		t.p.ETA = time.Duration(float64(t.p.Total-t.p.Downloaded) / t.p.Speed * float64(time.Second))
	} else {
		t.p.ETA = 0
	}
}

func (t *tracker) finish() {
	if t.fn == nil {
		return
	}
	t.mu.Lock()
	t.fill(time.Now())
	t.p.Status = StatusFinished
	if t.p.Total < t.p.Downloaded || t.p.TotalEstimated {
		t.p.Total = t.p.Downloaded
	}
	snap := t.p
	t.mu.Unlock()
	t.fn(snap)
}

// countingWriter сообщает трекеру о записанных байтах.
type countingWriter struct {
	w interface{ Write([]byte) (int, error) }
	t *tracker
}

func (c *countingWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.t.add(int64(n))
	return n, err
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func backoff(attempt int) time.Duration {
	return time.Duration(1<<min(attempt, 5)) * 500 * time.Millisecond
}

// PartName — имя временного файла.
func PartName(dst string) string { return dst + ".part" }

func fileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}
