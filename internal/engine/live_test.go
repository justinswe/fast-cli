package engine

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLive runs the real test against fast.com; run with FASTCLI_LIVE=1.
func TestLive(t *testing.T) {
	if os.Getenv("FASTCLI_LIVE") != "1" {
		t.Skip("set FASTCLI_LIVE=1 to run against fast.com")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	opts := DefaultOptions()
	opts.Upload = true
	opts.UserAgent = "fast-cli/test"

	var last Sample
	start := time.Now()
	res, err := Run(ctx, opts, func(s Sample) {
		if s.Phase != last.Phase || s.Stable || s.Elapsed-last.Elapsed >= time.Second {
			t.Logf("%-8s t=%5.1fs %8.2f Mbps  lat=%6.1f ms  conns=%d stable=%v", s.Phase, s.Elapsed.Seconds(), s.SpeedBps/1e6, s.LatencyMs, s.Connections, s.Stable)
			last = s
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Logf("wall %v", time.Since(start).Round(time.Millisecond))
	t.Logf("download %.2f Mbps over %v, upload %.2f Mbps over %v", res.DownloadBps/1e6, res.DownloadDuration.Round(time.Millisecond), res.UploadBps/1e6, res.UploadDuration.Round(time.Millisecond))
	t.Logf("latency unloaded %.1f ms, loaded download %.1f ms, loaded upload %.1f ms", res.UnloadedLatencyMs, res.LoadedDownloadLatencyMs, res.LoadedUploadLatencyMs)
	t.Logf("client %+v", res.Client)
	for _, s := range res.Servers {
		t.Logf("server %s (%s, %s)", s.URL, s.Location.City, s.Location.Country)
	}

	if res.DownloadBps < 1e5 || res.DownloadBps > 1e11 {
		t.Errorf("download %.0f bit/s implausible", res.DownloadBps)
	}
	if !res.UploadRan || res.UploadBps < 1e5 || res.UploadBps > 1e11 {
		t.Errorf("upload %.0f bit/s implausible (ran=%v)", res.UploadBps, res.UploadRan)
	}
	if res.UnloadedLatencyMs <= 0 || res.UnloadedLatencyMs > 2000 {
		t.Errorf("unloaded latency %.1f ms implausible", res.UnloadedLatencyMs)
	}
	if res.LoadedDownloadLatencyMs <= 0 || res.LoadedDownloadLatencyMs > 5000 {
		t.Errorf("loaded download latency %.1f ms implausible", res.LoadedDownloadLatencyMs)
	}
	for _, d := range []time.Duration{res.DownloadDuration, res.UploadDuration} {
		if d < opts.StableMinDuration || d > opts.MaxDuration+opts.ProgressInterval {
			t.Errorf("duration %v outside [%v, %v]", d, opts.StableMinDuration, opts.MaxDuration)
		}
	}
	if res.Client.IP == "" || len(res.Servers) == 0 {
		t.Errorf("client/servers missing: %+v %d", res.Client, len(res.Servers))
	}
}
