package cli

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/Disaldr/go-dlp/downloader"
	"github.com/Disaldr/go-dlp/media"
	"github.com/Disaldr/go-dlp/outtmpl"
)

// HumanBytes форматирует размер как в yt-dlp: 12.34MiB.
func HumanBytes(n float64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	i := 0
	for n >= 1024 && i < len(units)-1 {
		n /= 1024
		i++
	}
	return fmt.Sprintf("%.2f%s", n, units[i])
}

func printFormats(w io.Writer, info *media.Info) {
	fmt.Fprintf(w, "[info] Available formats for %s:\n", info.ID)
	tw := tabwriter.NewWriter(w, 0, 0, 1, ' ', 0)
	fmt.Fprintln(tw, "ID\tEXT\tRESOLUTION\tFPS\t│\tFILESIZE\tTBR\tPROTO\t│\tVCODEC\tACODEC\tMORE INFO")
	for _, f := range info.Formats {
		size := ""
		if f.Filesize > 0 {
			size = HumanBytes(float64(f.Filesize))
		} else if f.FilesizeApprox > 0 {
			size = "≈" + HumanBytes(float64(f.FilesizeApprox))
		}
		fps := ""
		if f.FPS > 0 {
			fps = fmt.Sprintf("%g", f.FPS)
		}
		tbr := ""
		if f.TBR > 0 {
			tbr = fmt.Sprintf("%.0fk", f.TBR)
		}
		vc, ac := orDash(f.VCodec), orDash(f.ACodec)
		if f.VCodec == media.CodecNone {
			vc = "audio only"
		}
		if f.ACodec == media.CodecNone {
			ac = "video only"
		}
		more := f.FormatNote
		if f.Language != "" {
			more = strings.TrimSpace("[" + f.Language + "] " + more)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t│\t%s\t%s\t%s\t│\t%s\t%s\t%s\n",
			f.FormatID, f.Ext, f.Resolution(), fps, size, tbr, f.Protocol, vc, ac, more)
	}
	tw.Flush()
}

func orDash(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// progressPrinter рисует строку прогресса в стиле yt-dlp.
type progressPrinter struct {
	mu                sync.Mutex
	w                 io.Writer
	open              bool
	newline, noColors bool
	template          string
	info              *media.Info
}

func newProgressPrinter(w io.Writer) *progressPrinter { return &progressPrinter{w: w} }

func (p *progressPrinter) update(pr downloader.Progress) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.template != "" {
		fields := map[string]any{
			"status": string(pr.Status), "filename": pr.Filename,
			"downloaded_bytes": pr.Downloaded, "speed": pr.Speed,
			"eta": pr.ETA.Seconds(), "elapsed": pr.Elapsed.Seconds(),
			"fragment_index": pr.FragmentIndex, "fragment_count": pr.FragmentCount,
			"total_bytes": nil, "total_bytes_estimate": nil,
		}
		if pr.Total > 0 {
			key := "total_bytes"
			if pr.TotalEstimated {
				key = "total_bytes_estimate"
			}
			fields[key] = pr.Total
		}
		fmt.Fprintln(p.w, outtmpl.Render(p.template, map[string]any{"progress": fields, "info": outtmpl.Fields(p.info)}))
		return
	}
	pct := "  ?.?%"
	if v := pr.Percent(); v >= 0 {
		pct = fmt.Sprintf("%5.1f%%", v)
	}
	total := "Unknown"
	if pr.Total > 0 {
		total = HumanBytes(float64(pr.Total))
		if pr.TotalEstimated {
			total = "~" + total
		}
	}
	line := fmt.Sprintf("\r[download] %s of %10s at %10s/s ETA %s", pct, total, HumanBytes(pr.Speed), fmtETA(pr.ETA))
	if pr.FragmentCount > 0 {
		line += fmt.Sprintf(" (frag %d/%d)", pr.FragmentIndex, pr.FragmentCount)
	}
	if pr.Status == downloader.StatusFinished {
		line = fmt.Sprintf("\r[download] 100%% of %10s in %s at %s/s", HumanBytes(float64(pr.Downloaded)), fmtETA(pr.Elapsed), HumanBytes(pr.Speed))
	}
	if p.newline {
		fmt.Fprintln(p.w, strings.TrimPrefix(line, "\r"))
		p.open = false
		return
	}
	if !p.noColors {
		line += "\033[K"
	}
	fmt.Fprint(p.w, line)
	p.open = true
	if pr.Status == downloader.StatusFinished {
		fmt.Fprintln(p.w)
		p.open = false
	}
}

func (p *progressPrinter) done() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.open {
		fmt.Fprintln(p.w)
		p.open = false
	}
}

func fmtETA(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}
