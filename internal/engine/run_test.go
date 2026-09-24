package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const fakeToken = "dGVzdHRva2Vu"

// fakeOCA is an API + OCA + fast.com stand-in for every host the engine talks to.
type fakeOCA struct {
	t        *testing.T
	api      http.Handler
	throttle time.Duration
	failHost string // GETs to this host answer 500

	mu       sync.Mutex
	gets     map[string][]int64 // host -> requested range sizes in arrival order
	posts    map[string]int
	pings    int
	apiToken string
	badUA    int32
	badPing  []string
	badBody  []string
}

func newFakeOCA(t *testing.T, targets int) *fakeOCA {
	var hits atomic.Int32
	return &fakeOCA{t: t, api: fakeAPI(t, targets, false, &hits), throttle: time.Millisecond, gets: map[string][]int64{}, posts: map[string]int{}}
}

func (f *fakeOCA) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("User-Agent") != "fast-cli/test" {
		f.mu.Lock()
		f.badUA++
		f.mu.Unlock()
	}
	switch {
	case r.URL.Path == "/":
		_, _ = io.WriteString(w, `<html><script src="/app-abc123.js"></script></html>`)
	case r.URL.Path == "/app-abc123.js":
		_, _ = io.WriteString(w, `var p={token:"`+fakeToken+`",urlCount:5}`)
	case r.URL.Path == "/netflix/speedtest/v2":
		f.mu.Lock()
		f.apiToken = r.URL.Query().Get("token")
		f.mu.Unlock()
		f.api.ServeHTTP(w, r)
	case strings.HasPrefix(r.URL.Path, "/speedtest/range/0-"):
		n, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/speedtest/range/0-"), 10, 64)
		if err != nil {
			http.Error(w, "bad range", 400)
			return
		}
		if r.Method == http.MethodPost {
			f.servePost(w, r, n)
			return
		}
		f.serveGet(w, r, n)
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	}
}

func (f *fakeOCA) serveGet(w http.ResponseWriter, r *http.Request, n int64) {
	f.mu.Lock()
	f.gets[r.Host] = append(f.gets[r.Host], n)
	f.mu.Unlock()
	if r.Host == f.failHost {
		http.Error(w, "boom", 500)
		return
	}
	size := n + 1
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	chunk := make([]byte, 64<<10)
	for size > 0 {
		c := chunk
		if size < int64(len(c)) {
			c = c[:size]
		}
		if _, err := w.Write(c); err != nil {
			return
		}
		size -= int64(len(c))
		time.Sleep(f.throttle)
	}
}

func (f *fakeOCA) servePost(w http.ResponseWriter, r *http.Request, n int64) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return // aborted mid-flight when the phase completed
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if n == 0 {
		f.pings++
		if r.ContentLength != 0 || len(body) != 0 || r.Header.Get("Content-Type") != "" {
			f.badPing = append(f.badPing, fmt.Sprintf("len=%d ct=%q", r.ContentLength, r.Header.Get("Content-Type")))
		}
		return
	}
	f.posts[r.Host]++
	if int64(len(body)) != n || r.ContentLength != n || r.Header.Get("Content-Type") != "application/octet-stream" {
		f.badBody = append(f.badBody, fmt.Sprintf("n=%d got=%d cl=%d ct=%q", n, len(body), r.ContentLength, r.Header.Get("Content-Type")))
		return
	}
	for _, c := range body[:min(len(body), 4096)] {
		if !(c >= '0' && c <= '9') && c != '.' {
			f.badBody = append(f.badBody, fmt.Sprintf("byte %q", c))
			break
		}
	}
}

// hostKeepingTransport routes to the test server but keeps the target host in the Host header.
type hostKeepingTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (t hostKeepingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Host = req.URL.Host
	r.URL.Scheme, r.URL.Host = t.target.Scheme, t.target.Host
	return t.base.RoundTrip(r)
}

func fastOptions(hc *http.Client) Options {
	o := DefaultOptions()
	o.HTTPClient = hc
	o.UserAgent = "fast-cli/test"
	o.Upload = true
	o.ProgressInterval = 20 * time.Millisecond
	o.MinDuration = 200 * time.Millisecond
	o.StableMinDuration = 300 * time.Millisecond
	o.MaxDuration = 2 * time.Second
	o.StableMaxDuration = 2 * time.Second
	o.UnloadedMinDuration = 200 * time.Millisecond
	o.UnloadedMinSamples = 20
	o.UnloadedMaxDuration = 3 * time.Second
	o.LatencyInterval = 50 * time.Millisecond
	o.MaxPayloadBytes = 1 << 20
	return o
}

// startFake serves f and returns a client routed to it plus a leak checker.
func startFake(t *testing.T, f *fakeOCA) (*http.Client, func()) {
	t.Helper()
	before := runtime.NumGoroutine()
	srv := httptest.NewServer(f)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = 32
	hc := &http.Client{Transport: hostKeepingTransport{target: u, base: tr}}
	checkLeaks := func() {
		t.Helper()
		tr.CloseIdleConnections()
		srv.Close()
		deadline := time.Now().Add(5 * time.Second)
		for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if n := runtime.NumGoroutine(); n > before {
			buf := make([]byte, 1<<20)
			t.Fatalf("goroutines %d > %d before Run:\n%s", n, before, buf[:runtime.Stack(buf, true)])
		}
	}
	return hc, checkLeaks
}

func TestRunEndToEnd(t *testing.T) {
	f := newFakeOCA(t, 5)
	hc, checkLeaks := startFake(t, f)
	opts := fastOptions(hc)

	var mu sync.Mutex
	var samples []Sample
	res, err := Run(context.Background(), opts, func(s Sample) {
		mu.Lock()
		samples = append(samples, s)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	checkLeaks()
	t.Logf("result: %+v", *res)

	if res.DownloadBps <= 0 || res.UploadBps <= 0 || !res.UploadRan {
		t.Errorf("speeds: down=%v up=%v ran=%v", res.DownloadBps, res.UploadBps, res.UploadRan)
	}
	if res.UnloadedLatencyMs <= 0 || res.LoadedDownloadLatencyMs <= 0 || res.LoadedUploadLatencyMs <= 0 {
		t.Errorf("latencies: unloaded=%v loadedDown=%v loadedUp=%v", res.UnloadedLatencyMs, res.LoadedDownloadLatencyMs, res.LoadedUploadLatencyMs)
	}
	if res.Client.IP != "1.2.3.4" || len(res.Servers) != 5 {
		t.Errorf("client=%+v servers=%d", res.Client, len(res.Servers))
	}
	for _, d := range []time.Duration{res.DownloadDuration, res.UploadDuration} {
		if d < opts.StableMinDuration || d > opts.MaxDuration+200*time.Millisecond {
			t.Errorf("duration %v outside [%v, %v]", d, opts.StableMinDuration, opts.MaxDuration)
		}
	}

	// Phase order and per-phase sample invariants.
	order := []Phase{PhaseDownload, PhaseLatency, PhaseUpload}
	pi := 0
	var last Sample
	for i, s := range samples {
		if s.Phase != order[pi] {
			if i == 0 || !last.Stable || pi+1 >= len(order) || s.Phase != order[pi+1] {
				t.Fatalf("sample %d: phase %s after %s (stable=%v)", i, s.Phase, last.Phase, last.Stable)
			}
			pi++
		} else if i > 0 && last.Stable {
			t.Fatalf("sample %d: tick after the completing tick of %s", i, s.Phase)
		}
		if s.Connections < 1 || s.Connections > opts.MaxConnections {
			t.Fatalf("sample %d: connections %d", i, s.Connections)
		}
		if s.Phase == last.Phase && s.Elapsed < last.Elapsed {
			t.Fatalf("sample %d: elapsed went backwards", i)
		}
		last = s
	}
	if pi != 2 || !last.Stable {
		t.Fatalf("phases seen %d, last stable %v", pi+1, last.Stable)
	}
	final := map[Phase]Sample{}
	for _, s := range samples {
		if s.Stable {
			final[s.Phase] = s
		}
	}
	if final[PhaseDownload].SpeedBps != res.DownloadBps || final[PhaseUpload].SpeedBps != res.UploadBps || final[PhaseLatency].LatencyMs != res.UnloadedLatencyMs {
		t.Errorf("completing samples %+v do not match result", final)
	}
	if final[PhaseDownload].Connections < 3 {
		t.Errorf("download never ramped: %d connections", final[PhaseDownload].Connections)
	}

	// Wire-level checks.
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.apiToken != fakeToken {
		t.Errorf("API token = %q, want the one scraped from the bundle", f.apiToken)
	}
	if f.badUA != 0 {
		t.Errorf("%d requests without the User-Agent", f.badUA)
	}
	if f.pings == 0 || len(f.badPing) > 0 {
		t.Errorf("pings=%d bad=%v", f.pings, f.badPing)
	}
	if len(f.badBody) > 0 {
		t.Errorf("bad upload bodies: %v", f.badBody)
	}
	if len(f.gets) != 5 || len(f.posts) < 3 {
		t.Errorf("hosts: gets=%d posts=%d", len(f.gets), len(f.posts))
	}
	for host, sizes := range f.gets {
		if sizes[0] != opts.FirstRequestBytes {
			t.Errorf("%s: first request %d, want %d", host, sizes[0], opts.FirstRequestBytes)
		}
		seen := map[int64]bool{}
		for _, n := range sizes {
			if n == opts.FirstRequestBytes {
				continue
			}
			if n > opts.MaxPayloadBytes || n <= opts.MaxPayloadBytes-int64(len(sizes)) || seen[n] {
				t.Errorf("%s: unexpected range size %d in %v", host, n, sizes)
			}
			seen[n] = true
		}
	}
}

func TestRunRestartsAfterConnectionFailure(t *testing.T) {
	f := newFakeOCA(t, 5)
	f.failHost = "oca0.1.s0.test"
	hc, checkLeaks := startFake(t, f)
	opts := fastOptions(hc)
	opts.Upload = false
	opts.Token = "tok"

	res, err := Run(context.Background(), opts, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	checkLeaks()
	if res.DownloadBps <= 0 || res.UploadRan || res.UploadBps != 0 {
		t.Errorf("result %+v", *res)
	}
	if len(res.Servers) != 4 {
		t.Errorf("servers = %d, want the failing host dropped", len(res.Servers))
	}
	for _, s := range res.Servers {
		if strings.Contains(s.URL, f.failHost) {
			t.Errorf("failing host still listed: %s", s.URL)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if n := len(f.gets[f.failHost]); n != 1 {
		t.Errorf("failing host got %d requests, want 1 (bad URL after the first failure)", n)
	}
	if f.apiToken != "tok" {
		t.Errorf("token = %q", f.apiToken)
	}
}

func TestRunGivesUpAfterMaxAttempts(t *testing.T) {
	f := newFakeOCA(t, 5)
	f.failHost = "*"
	f.api = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	})
	hc, checkLeaks := startFake(t, f)
	opts := fastOptions(hc)
	opts.Token = "tok"
	opts.MaxAttempts = 3

	start := time.Now()
	_, err := Run(context.Background(), opts, nil)
	checkLeaks()
	if err == nil || !strings.Contains(err.Error(), "giving up after 3 attempts") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
}

func TestRunCancel(t *testing.T) {
	f := newFakeOCA(t, 5)
	hc, checkLeaks := startFake(t, f)
	opts := fastOptions(hc)
	opts.Token = "tok"
	opts.MaxDuration, opts.StableMaxDuration = time.Minute, time.Minute
	opts.StableMinDuration = time.Minute

	ctx, cancel := context.WithCancel(context.Background())
	var ticks atomic.Int32
	go func() {
		for ticks.Load() < 5 {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	start := time.Now()
	res, err := Run(ctx, opts, func(Sample) { ticks.Add(1) })
	if !errors.Is(err, context.Canceled) || res != nil {
		t.Fatalf("Run = %v, %v", res, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("cancel took %v", time.Since(start))
	}
	checkLeaks()
}

func TestRunNilProgressAndZeroOptions(t *testing.T) {
	// Only checks that zero-valued options are defaulted and a nil callback is tolerated;
	// the run itself is cut short by the cancelled context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := newFakeOCA(t, 5)
	hc, checkLeaks := startFake(t, f)
	_, err := Run(ctx, Options{HTTPClient: hc, Token: "tok"}, nil)
	checkLeaks()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if o := (Options{}).withDefaults(); o.ProgressInterval != 150*time.Millisecond || o.MaxConnections != 8 || o.MaxPayloadBytes != 26214400 {
		t.Fatalf("defaults not applied: %+v", o)
	}
}
