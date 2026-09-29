package output

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/justinswe/fast-cli/internal/engine"
)

func TestFinishSummary(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, false, false)
	r.Finish(sampleResult(true))
	want := "Download: 250 Mbps\n" +
		"Latency: unloaded 12 ms, loaded 45 ms (download), 60 ms (upload)\n" +
		"Upload: 12 Mbps\n" +
		"Client      The Dalles, US   34.19.110.48   Google LLC\n" +
		"Server(s)   Seattle, US  |  San Jose, US\n"
	if buf.String() != want {
		t.Errorf("summary:\n%s\nwant:\n%s", buf.String(), want)
	}

	buf.Reset()
	NewRenderer(&buf, false, false).Finish(sampleResult(false))
	if got := buf.String(); strings.Contains(got, "Upload") || !strings.Contains(got, "Latency: unloaded 12 ms, loaded 45 ms\n") {
		t.Errorf("no-upload summary must keep latency and omit upload:\n%s", got)
	}

	buf.Reset()
	NewRenderer(&buf, false, true).Finish(sampleResult(true))
	if n := strings.Count(buf.String(), "\n"); n != 1 || strings.Contains(buf.String(), "Client") {
		t.Errorf("single-line summary must be one line without client info:\n%q", buf.String())
	}
}

func TestUpdateTTY(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, true, false)
	r.Update(engine.Sample{Phase: engine.PhaseDownload, SpeedBps: 245e6, LatencyMs: 45})
	r.Update(engine.Sample{Phase: engine.PhaseLatency, LatencyMs: 12})
	r.Update(engine.Sample{Phase: engine.PhaseUpload, SpeedBps: 12e6, LatencyMs: 60})
	got := buf.String()
	if strings.Contains(got, "\n") || !strings.HasPrefix(got, "\rDownload: 250 Mbps   Latency: loaded 45 ms") {
		t.Errorf("tty updates must redraw in place: %q", got)
	}
	if !strings.Contains(got, "\rDownload: 250 Mbps   Latency: unloaded 12 ms, loaded 45 ms") {
		t.Errorf("latency phase must keep the download figure: %q", got)
	}
	if !strings.Contains(got, "\rDownload: 250 Mbps   Latency: unloaded 12 ms, loaded 60 ms   Upload: 12 Mbps") {
		t.Errorf("upload phase line missing: %q", got)
	}
	r.Finish(sampleResult(true))
	if !strings.Contains(buf.String(), "\r"+strings.Repeat(" ", r.lastLen)+"\rDownload: 250 Mbps\n") {
		t.Errorf("finish must clear the live line before the summary: %q", buf.String())
	}
}

func TestUpdateNonTTYThrottles(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, false, false)
	r.interval = time.Hour
	for i := 0; i < 5; i++ {
		r.Update(engine.Sample{Phase: engine.PhaseDownload, SpeedBps: float64(i) * 1e6})
	}
	if got := buf.String(); got != "Download: 0.0 Kbps\n" {
		t.Errorf("non-tty must print once per interval, got %q", got)
	}
	r.EndLine()
	if strings.HasSuffix(buf.String(), "\n\n") {
		t.Errorf("EndLine must not add a blank line when nothing is pending")
	}
}
