package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func executeArgs(args ...string) (string, string, error) {
	cmd := NewRootCommand()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

// Реальный контракт GLIGO JobArgs, включая вспомогательные сетевые аргументы.
func jobArgs(dest, link string, processing ...string) []string {
	args := []string{"--ignore-config", "--no-playlist", "--newline", "--progress", "--no-colors", "--encoding", "utf-8",
		"-f", "best", "-P", dest, "-o", "%(title).180B [%(id)s].%(ext)s", "--windows-filenames",
		"--print", "after_move:GLIGO_FILE:%(filepath)s", "--progress-template", "download:GLIGO_PROGRESS:%(progress)j"}
	args = append(args, processing...)
	args = append(args, "--socket-timeout", "25", "--retries", "3", "--extractor-retries", "2",
		"--extractor-args", "generic:impersonate", "--js-runtimes", "deno:/unused/deno")
	return append(args, "--", link)
}

func TestJobArgsDownload(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg required for audio/remux integration")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe required for integration verification")
	}
	input := filepath.Join(t.TempDir(), "sample.mp4")
	out, err := exec.Command(ffmpeg, "-v", "error", "-i", "../../postprocess/testdata/vprog.mp4", "-i", "../../postprocess/testdata/aprog.m4a", "-map", "0:v", "-map", "1:a", "-c", "copy", input).CombinedOutput()
	if err != nil {
		t.Fatalf("fixture merge: %v: %s", err, out)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, input)
	}))
	defer server.Close()
	for _, tc := range []struct {
		name, ext, codec string
		args             []string
	}{
		{"video", ".mkv", "", []string{"--merge-output-format", "mkv", "--remux-video", "mkv"}},
		{"mp3", ".mp3", "mp3", []string{"-x", "--audio-format", "mp3"}},
		{"m4a", ".m4a", "aac", []string{"-x", "--audio-format", "m4a"}},
		{"opus", ".opus", "opus", []string{"-x", "--audio-format", "opus"}},
		{"best", ".m4a", "aac", []string{"-x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dest := t.TempDir()
			args := jobArgs(dest, server.URL+"/sample.mp4", tc.args...)
			stdout, stderr, err := executeArgs(args...)
			if err != nil {
				t.Fatalf("CLI: %v\n%s", err, stderr)
			}
			var file string
			var finished bool
			for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
				switch {
				case strings.HasPrefix(line, "GLIGO_PROGRESS:"):
					if file != "" {
						t.Fatal("progress after final file marker")
					}
					var p struct {
						Downloaded int64  `json:"downloaded_bytes"`
						Total      int64  `json:"total_bytes"`
						Status     string `json:"status"`
					}
					if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "GLIGO_PROGRESS:")), &p); err != nil {
						t.Fatalf("invalid progress JSON: %s: %v", line, err)
					}
					if p.Status == "finished" {
						finished = p.Downloaded > 0 && p.Total == p.Downloaded
					}
				case strings.HasPrefix(line, "GLIGO_FILE:"):
					file = strings.TrimPrefix(line, "GLIGO_FILE:")
				default:
					t.Fatalf("unexpected stdout: %q", line)
				}
			}
			if !finished || !filepath.IsAbs(file) || filepath.Ext(file) != tc.ext {
				t.Fatalf("finished=%v file=%q\n%s", finished, file, stdout)
			}
			if _, err := os.Stat(file); err != nil {
				t.Fatal(err)
			}
			probe, err := exec.Command(ffprobe, "-v", "error", "-show_entries", "stream=codec_name,codec_type", "-of", "json", file).Output()
			if err != nil {
				t.Fatal(err)
			}
			var streams struct {
				Streams []struct {
					Codec string `json:"codec_name"`
					Type  string `json:"codec_type"`
				} `json:"streams"`
			}
			if err := json.Unmarshal(probe, &streams); err != nil {
				t.Fatal(err)
			}
			if tc.codec != "" {
				if len(streams.Streams) != 1 || streams.Streams[0].Type != "audio" || streams.Streams[0].Codec != tc.codec {
					t.Fatalf("wrong audio streams: %s", probe)
				}
			} else if len(streams.Streams) != 2 {
				t.Fatalf("expected video and audio: %s", probe)
			}
			files, _ := os.ReadDir(dest)
			if len(files) != 1 {
				t.Fatalf("intermediates left: %v", files)
			}
			// Готовый файл повторно сообщается приложению и не перезаписывается.
			if tc.name != "best" {
				before, _ := os.Stat(file)
				again, stderr, err := executeArgs(args...)
				if err != nil || !strings.Contains(again, "GLIGO_FILE:"+file) {
					t.Fatalf("repeat: %v %s %s", err, again, stderr)
				}
				after, _ := os.Stat(file)
				if !after.ModTime().Equal(before.ModTime()) {
					t.Fatal("existing output was rewritten")
				}
			}
		})
	}
	t.Run("failed-remux", func(t *testing.T) {
		dest := t.TempDir()
		stdout, _, err := executeArgs(jobArgs(dest, server.URL+"/sample.mp4", "--remux-video", "webm")...)
		if err == nil || strings.Contains(stdout, "GLIGO_FILE:") {
			t.Fatalf("failed remux reported success: %v %s", err, stdout)
		}
		files, _ := filepath.Glob(filepath.Join(dest, "*.mp4"))
		all, _ := os.ReadDir(dest)
		if len(files) != 1 || len(all) != 1 {
			t.Fatalf("expected source preserved and temporary files removed: %v", all)
		}
	})
}

func TestPrintVideoAndNoSimulate(t *testing.T) {
	data := []byte("local media fixture")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(data)
	}))
	defer server.Close()
	for _, download := range []bool{false, true} {
		dest := t.TempDir()
		args := []string{"--print", "title", "--print", "%(webpage_url)s", "-P", dest}
		if download {
			args = append(args, "--no-simulate")
		}
		args = append(args, "--", server.URL+"/sample.mp4")
		stdout, stderr, err := executeArgs(args...)
		if err != nil || stdout != "sample\n"+server.URL+"/sample.mp4\n" {
			t.Fatalf("print: %v %q %s", err, stdout, stderr)
		}
		files, _ := os.ReadDir(dest)
		if (len(files) == 1) != download {
			t.Fatalf("simulate/download files=%v", files)
		}
	}
}

func TestOutputValidation(t *testing.T) {
	for _, args := range [][]string{
		{"--encoding", "latin1"}, {"--socket-timeout", "-1"}, {"--extractor-retries", "-1"},
		{"--progress-template", "postprocess:%(progress)j"}, {"--print", "playlist:title"},
		{"-x", "--audio-format", "invalid"}, {"--remux-video", "../../mp4"},
	} {
		_, _, err := executeArgs(append(args, "https://example.invalid/video")...)
		if err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestParallelMachineOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(bytes.Repeat([]byte("media"), 10000))
	}))
	defer server.Close()
	dest := t.TempDir()
	args := jobArgs(dest, server.URL+"/one.mp4", "-M", "4")
	args = append(args, server.URL+"/two.mp4", server.URL+"/three.mp4", server.URL+"/four.mp4")
	stdout, stderr, err := executeArgs(args...)
	if err != nil {
		t.Fatalf("parallel CLI: %v %s", err, stderr)
	}
	files := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if encoded, ok := strings.CutPrefix(line, "GLIGO_PROGRESS:"); ok {
			if !json.Valid([]byte(encoded)) {
				t.Fatalf("interleaved progress: %q", line)
			}
		} else if path, ok := strings.CutPrefix(line, "GLIGO_FILE:"); ok {
			if _, err := os.Stat(path); err != nil {
				t.Fatal(err)
			}
			files[path] = true
		} else {
			t.Fatalf("unexpected stdout: %q", line)
		}
	}
	if len(files) != 4 {
		t.Fatalf("got %d files: %s", len(files), stdout)
	}
}
