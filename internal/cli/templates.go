package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Disaldr/go-dlp/media"
	"github.com/Disaldr/go-dlp/outtmpl"
)

type noWarningsHandler struct{ slog.Handler }

func (h *noWarningsHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level != slog.LevelWarn && h.Handler.Enabled(ctx, level)
}

func (h *noWarningsHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &noWarningsHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h *noWarningsHandler) WithGroup(name string) slog.Handler {
	return &noWarningsHandler{Handler: h.Handler.WithGroup(name)}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

type stageTemplate struct {
	stage, template string
}

func (f *flags) prepareOutput() error {
	if !strings.EqualFold(f.encoding, "utf-8") && !strings.EqualFold(f.encoding, "utf8") {
		return fmt.Errorf("unsupported --encoding %q; use utf-8", f.encoding)
	}
	if f.socketTimeout < 0 || math.IsNaN(f.socketTimeout) || math.IsInf(f.socketTimeout, 0) || f.socketTimeout >= float64(math.MaxInt64)/1e9 {
		return fmt.Errorf("invalid --socket-timeout")
	}
	if f.extractorRetries < 0 {
		return fmt.Errorf("--extractor-retries must be non-negative")
	}
	f.printTemplates = nil
	for _, s := range f.prints {
		stage, template := "video", s
		if prefix, rest, ok := strings.Cut(s, ":"); ok {
			// Двоеточие в произвольном шаблоне — обычный текст.
			switch prefix {
			case "video", "before_dl", "after_move", "after_video":
				stage, template = prefix, rest
			case "pre_process", "post_process", "after_filter", "playlist", "before_video":
				return fmt.Errorf("unsupported --print stage %q", prefix)
			}
		}
		if !strings.Contains(template, "%(") {
			// --print title — сокращение для --print '%(title)s'.
			if !strings.ContainsAny(template, " :/\\") {
				template = "%(" + template + ")s"
			}
		}
		f.printTemplates = append(f.printTemplates, stageTemplate{stage, template})
	}
	if len(f.prints) > 0 {
		f.quiet = true
	}
	for _, s := range f.progressTemplates {
		template := s
		if prefix, rest, ok := strings.Cut(s, ":"); ok {
			switch prefix {
			case "download":
				template = rest
			case "postprocess", "download-title", "postprocess-title":
				return fmt.Errorf("unsupported --progress-template type %q (use download)", prefix)
			}
		}
		f.progressTemplate = template
	}
	return nil
}

func (f *flags) printOnly() bool {
	if len(f.printTemplates) == 0 {
		return false
	}
	for _, t := range f.printTemplates {
		if t.stage != "video" {
			return false
		}
	}
	return true
}

func (f *flags) printStage(w io.Writer, stage string, info *media.Info) error {
	fields := outtmpl.Fields(info)
	if stage == "after_move" || stage == "after_video" {
		path, err := filepath.Abs(info.Filename)
		if err != nil {
			return err
		}
		fields["filepath"] = path
	}
	for _, t := range f.printTemplates {
		if t.stage != stage {
			continue
		}
		if _, err := fmt.Fprintln(w, outtmpl.Render(t.template, fields)); err != nil {
			return err
		}
	}
	return nil
}
