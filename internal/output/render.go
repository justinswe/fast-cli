package output

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jfernbaugh/fast-cli/internal/engine"
)

// IsTerminal reports whether f is a character device (an interactive terminal).
func IsTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// Renderer draws a live progress line and a fast.com-style final summary.
// On a TTY the line is redrawn in place; otherwise newline-terminated updates
// are printed at most once per second.
type Renderer struct {
	w          io.Writer
	tty        bool
	singleLine bool
	interval   time.Duration

	mu        sync.Mutex
	download  engine.Sample
	latency   engine.Sample
	upload    engine.Sample
	lastWrite time.Time
	lastLen   int
	pending   bool // a live line is on screen without a trailing newline
}

// NewRenderer returns a Renderer writing to w; tty selects in-place redraws and
// singleLine keeps the final summary on one compact line.
func NewRenderer(w io.Writer, tty, singleLine bool) *Renderer {
	return &Renderer{w: w, tty: tty, singleLine: singleLine, interval: time.Second}
}

// Update records a progress sample and redraws the live line.
func (r *Renderer) Update(s engine.Sample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch s.Phase {
	case engine.PhaseDownload:
		r.download = s
	case engine.PhaseLatency:
		r.latency = s
	case engine.PhaseUpload:
		r.upload = s
	}
	line := r.liveLine()
	now := time.Now()
	if r.tty {
		fmt.Fprintf(r.w, "\r%-*s", r.lastLen, line)
		r.lastLen = len(line)
		r.pending = true
		return
	}
	if !r.lastWrite.IsZero() && now.Sub(r.lastWrite) < r.interval {
		return
	}
	fmt.Fprintln(r.w, line)
	r.lastWrite = now
}

// liveLine formats the current download/latency/upload state on one line, in fast.com's order.
func (r *Renderer) liveLine() string {
	parts := []string{"Download: " + Speed(r.download.SpeedBps)}
	if lat := r.liveLatency(); lat != "" {
		parts = append(parts, lat)
	}
	if r.upload.Phase == engine.PhaseUpload {
		parts = append(parts, "Upload: "+Speed(r.upload.SpeedBps))
	}
	return strings.Join(parts, "   ")
}

// liveLatency formats the unloaded estimate and the most recent loaded latency, whichever are known.
func (r *Renderer) liveLatency() string {
	var segs []string
	if r.latency.LatencyMs > 0 {
		segs = append(segs, "unloaded "+Latency(r.latency.LatencyMs))
	}
	loaded := r.download.LatencyMs
	if r.upload.LatencyMs > 0 {
		loaded = r.upload.LatencyMs
	}
	if loaded > 0 {
		segs = append(segs, "loaded "+Latency(loaded))
	}
	if len(segs) == 0 {
		return ""
	}
	return "Latency: " + strings.Join(segs, ", ")
}

// EndLine terminates a pending live line so later output starts on a fresh line.
func (r *Renderer) EndLine() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending {
		fmt.Fprintln(r.w)
		r.pending = false
	}
}

// Finish clears the live line and prints the final summary.
func (r *Renderer) Finish(res *engine.Result) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending {
		fmt.Fprintf(r.w, "\r%*s\r", r.lastLen, "")
		r.pending = false
	}
	lines := summaryLines(res)
	if r.singleLine {
		fmt.Fprintln(r.w, strings.Join(lines, "  "))
		return
	}
	for _, l := range lines {
		fmt.Fprintln(r.w, l)
	}
	fmt.Fprintln(r.w, clientLine(res))
	fmt.Fprintln(r.w, serversLine(res))
}

// summaryLines returns the measurement lines in fast.com's order: download, latency, upload (when it ran).
func summaryLines(res *engine.Result) []string {
	lat := "Latency: unloaded " + Latency(res.UnloadedLatencyMs) + ", loaded " + Latency(res.LoadedDownloadLatencyMs)
	if res.UploadRan && res.LoadedUploadLatencyMs > 0 {
		lat += " (download), " + Latency(res.LoadedUploadLatencyMs) + " (upload)"
	}
	lines := []string{"Download: " + Speed(res.DownloadBps), lat}
	if res.UploadRan {
		lines = append(lines, "Upload: "+Speed(res.UploadBps))
	}
	return lines
}

// clientLine mirrors fast.com's "Client <location> <ip> <isp>" line.
func clientLine(res *engine.Result) string {
	c := res.Client
	parts := []string{"Client   "}
	if loc := location(c.Location.City, c.Location.Country); loc != "" {
		parts = append(parts, loc)
	}
	if c.IP != "" {
		parts = append(parts, c.IP)
	}
	if c.ISP != "" {
		parts = append(parts, strings.ReplaceAll(c.ISP, "_", " "))
	}
	return strings.Join(parts, "   ")
}

// serversLine mirrors fast.com's "Server(s) <loc> | <loc>" line: unique locations of the first three servers.
func serversLine(res *engine.Result) string {
	var locs []string
	seen := map[string]bool{}
	for i, s := range res.Servers {
		if i == 3 {
			break
		}
		loc := location(s.Location.City, s.Location.Country)
		if loc == "" || seen[loc] {
			continue
		}
		seen[loc] = true
		locs = append(locs, loc)
	}
	return "Server(s)   " + strings.Join(locs, "  |  ")
}

// location joins city and country as "City, Country", dropping empty parts.
func location(city, country string) string {
	switch {
	case city != "" && country != "":
		return city + ", " + country
	case city != "":
		return city
	default:
		return country
	}
}
