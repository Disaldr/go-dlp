package dlp

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/media"
)

func TestExtractionRetries(t *testing.T) {
	for _, tc := range []struct {
		name      string
		first     error
		wantCalls int
	}{
		{"server-error", &extractor.HTTPError{StatusCode: 503}, 2},
		{"rate-limit", &extractor.HTTPError{StatusCode: 429}, 2},
		{"truncated-response", io.ErrUnexpectedEOF, 2},
		{"not-found", &extractor.HTTPError{StatusCode: 404}, 1},
		{"login-required", extractor.ErrLoginRequired, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			reg := extractor.NewRegistry()
			reg.Register(&extractor.Func{Key: "retry-test", Match: func(string) bool { return true },
				ExtractF: func(context.Context, *extractor.Env, string) (*media.Info, error) {
					calls++
					if calls == 1 {
						return nil, extractor.Wrap("retry-test", "id", tc.first, "failed")
					}
					return &media.Info{ID: "id", Title: "ok"}, nil
				}})
			c, err := New(Options{Registry: reg, NoBuiltinScripts: true, ExtractorRetries: 1})
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.Extract(context.Background(), "https://test.invalid/video")
			if calls != tc.wantCalls || (err == nil) != (tc.wantCalls == 2) {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
}

func TestExtractionRetryCancellation(t *testing.T) {
	reg := extractor.NewRegistry()
	reg.Register(&extractor.Func{Key: "retry-test", Match: func(string) bool { return true },
		ExtractF: func(context.Context, *extractor.Env, string) (*media.Info, error) { return nil, io.ErrUnexpectedEOF }})
	c, err := New(Options{Registry: reg, NoBuiltinScripts: true, ExtractorRetries: 5})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c.Extract(ctx, "https://test.invalid/video"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation while waiting for retry: %v", err)
	}
}
