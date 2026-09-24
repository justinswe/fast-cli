package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"time"
)

// readBufferSize bounds one download read; each read is one progress measurement.
const readBufferSize = 128 << 10

// connError is a non-partial data-connection failure; it restarts the attempt and marks tpl bad.
type connError struct {
	tpl string
	err error
}

func (e *connError) Error() string { return "connection to " + hostOf(e.tpl) + ": " + e.err.Error() }
func (e *connError) Unwrap() error { return e.err }

// worker is one connection pair (data + pinger) to one target, as in startTestWorker.
type worker struct {
	tpl string
	oca string
	buf []byte

	mu        sync.Mutex
	pending   []measurement // recorded since the last tick
	lastEnd   float64
	hasAny    bool
	latencies []float64 // ms
}

func (w *worker) record(m measurement) {
	w.mu.Lock()
	w.pending = append(w.pending, m)
	w.lastEnd, w.hasAny = m.end, true
	w.mu.Unlock()
}

func (w *worker) take() workerFeed {
	w.mu.Lock()
	defer w.mu.Unlock()
	f := workerFeed{items: w.pending, lastEnd: w.lastEnd}
	w.pending = nil
	return f
}

func (w *worker) hasMeasurements() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.hasAny
}

func (w *worker) addLatency(ms float64) {
	w.mu.Lock()
	w.latencies = append(w.latencies, ms)
	w.mu.Unlock()
}

func (w *worker) latencySamples() []float64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]float64(nil), w.latencies...)
}

// attempt is one try of a phase: its workers, per-host request counters and the first fatal failure.
type attempt struct {
	e      *engine
	phase  Phase
	number int
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	workers  []*worker
	ocaCount map[string]int
	start    float64 // ms; fast.com downloadStartTime || startTime

	failCh chan *connError
}

func newAttempt(ctx context.Context, e *engine, phase Phase, number int) *attempt {
	actx, cancel := context.WithCancel(ctx)
	return &attempt{
		e: e, phase: phase, number: number, ctx: actx, cancel: cancel,
		ocaCount: map[string]int{}, start: e.now(), failCh: make(chan *connError, 1),
	}
}

// stop cancels every worker and waits for all goroutines to exit.
func (a *attempt) stop() {
	a.cancel()
	a.wg.Wait()
}

// startWorker takes the next target and launches its loops; the first worker fixes the test clock.
func (a *attempt) startWorker(t *targets) error {
	tpl, err := t.next(a.ctx)
	if err != nil {
		return err
	}
	w := &worker{tpl: tpl, oca: hostOf(tpl)}
	a.mu.Lock()
	if len(a.workers) == 0 {
		a.start = a.e.now()
	}
	a.workers = append(a.workers, w)
	a.mu.Unlock()
	if a.phase == PhaseLatency {
		a.wg.Add(1)
		go a.pingLoop(w, false)
		return nil
	}
	w.buf = make([]byte, readBufferSize)
	a.wg.Add(2)
	go a.pingLoop(w, true)
	go a.dataLoop(w)
	return nil
}

func (a *attempt) workerList() []*worker {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]*worker(nil), a.workers...)
}

func (a *attempt) workerCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.workers)
}

// bumpOCA returns the completed-request count for host and increments it (ocaCount).
func (a *attempt) bumpOCA(host string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	k := a.ocaCount[host]
	a.ocaCount[host] = k + 1
	return k
}

// fail records the first fatal connection failure; the tick loop restarts the attempt.
func (a *attempt) fail(w *worker, err error) {
	select {
	case a.failCh <- &connError{tpl: w.tpl, err: err}:
	default:
	}
}

// dataLoop ports sendDownloadRequest/sendUploadRequest + completeFunc (PROTOCOL.md §c, §f):
// back-to-back requests of FirstRequestBytes, then MaxPayloadBytes-k for the k-th
// completed request to this host; a failure with no bytes ever received restarts the attempt.
func (a *attempt) dataLoop(w *worker) {
	defer a.wg.Done()
	o := &a.e.opts
	size := o.FirstRequestBytes
	maxPayload := o.MaxPayloadBytes
	for a.ctx.Err() == nil {
		var reqURL string
		if a.number < 3 || o.MaxBytesInFlight-1 < int64(a.workerCount())*maxPayload {
			reqURL = rangeURL(w.tpl, size)
		} else {
			reqURL = unrangedURL(w.tpl)
			size = o.MaxPayloadBytes
		}
		var err error
		if a.phase == PhaseUpload {
			err = a.upload(w, reqURL, size)
		} else {
			err = a.download(w, reqURL)
		}
		maxPayload = o.MaxPayloadBytes - int64(a.bumpOCA(w.oca))
		if a.ctx.Err() != nil {
			return
		}
		if err != nil && !w.hasMeasurements() {
			a.fail(w, err)
			return
		}
		size = maxPayload
	}
}

// requestContext bounds a request by RequestTimeout with a FirstProgressTimeout
// watchdog that the caller disarms on the first progress event (§h).
func (a *attempt) requestContext() (context.Context, context.CancelFunc, *time.Timer) {
	ctx, cancel := context.WithTimeout(a.ctx, a.e.opts.RequestTimeout)
	watchdog := time.AfterFunc(a.e.opts.FirstProgressTimeout, cancel)
	return ctx, cancel, watchdog
}

// download GETs one range and records a measurement per body read (§c "What is counted").
func (a *attempt) download(w *worker, reqURL string) error {
	ctx, cancel, watchdog := a.requestContext()
	defer cancel()
	defer watchdog.Stop()
	last := a.e.now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return err
	}
	resp, err := a.e.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotModified {
		return fmt.Errorf("GET %s: HTTP %d", reqURL, resp.StatusCode)
	}
	for {
		n, err := resp.Body.Read(w.buf)
		if n > 0 {
			watchdog.Stop()
			now := a.e.now()
			w.record(measurement{bytes: float64(n), start: last, end: now})
			last = now
		}
		if err == io.EOF {
			w.record(measurement{start: last, end: a.e.now()}) // JS final blob.size-lastLoaded measurement, 0 bytes
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// upload POSTs size pseudo-random bytes, counting them as the transport reads
// them off the body (§f; the browser counts buffered bytes the same way, §4 item 8).
func (a *attempt) upload(w *worker, reqURL string, size int64) error {
	ctx, cancel, watchdog := a.requestContext()
	defer cancel()
	defer watchdog.Stop()
	body := &progressBody{r: bytes.NewReader(a.e.blob[:size]), a: a, w: w, watchdog: watchdog}
	body.last = a.e.now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, body)
	if err != nil {
		return err
	}
	req.ContentLength = size
	if size > 0 {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	resp, err := a.e.hc.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if !(resp.StatusCode >= 200 && resp.StatusCode < 300) && resp.StatusCode != http.StatusNotModified {
		return fmt.Errorf("POST %s: HTTP %d", reqURL, resp.StatusCode)
	}
	body.mu.Lock()
	rest := float64(size) - body.loaded
	last := body.last
	body.mu.Unlock()
	w.record(measurement{bytes: rest, start: last, end: a.e.now()})
	return nil
}

// progressBody records one measurement per transport read of the upload body.
type progressBody struct {
	r        *bytes.Reader
	a        *attempt
	w        *worker
	watchdog *time.Timer

	mu     sync.Mutex
	loaded float64
	last   float64
}

func (b *progressBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if n > 0 {
		b.watchdog.Stop()
		now := b.a.e.now()
		b.mu.Lock()
		b.w.record(measurement{bytes: float64(n), start: b.last, end: now})
		b.last = now
		b.loaded += float64(n)
		b.mu.Unlock()
	}
	return n, err
}

// ping POSTs an empty body to /range/0-0 and returns the TTFB in ms: first
// response byte minus request written, from httptrace (§e), falling back to
// half the wall-clock round trip like the JS when the trace is incomplete.
func (a *attempt) ping(w *worker) (float64, bool) {
	ctx, cancel := context.WithTimeout(a.ctx, a.e.opts.FirstProgressTimeout)
	defer cancel()
	var wrote, first atomic.Int64
	trace := &httptrace.ClientTrace{
		WroteRequest:         func(httptrace.WroteRequestInfo) { wrote.Store(time.Now().UnixNano()) },
		GotFirstResponseByte: func() { first.Store(time.Now().UnixNano()) },
	}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodPost, pingURL(w.tpl), http.NoBody)
	if err != nil {
		return 0, false
	}
	start := time.Now()
	resp, err := a.e.hc.Do(req)
	if err != nil {
		return 0, false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	end := time.Now()
	if !(resp.StatusCode >= 200 && resp.StatusCode < 300) && resp.StatusCode != http.StatusNotModified {
		return 0, false
	}
	if ws, fs := wrote.Load(), first.Load(); ws != 0 && fs > ws {
		return float64(fs-ws) / 1e6, true
	}
	return float64(end.Sub(start).Nanoseconds()) / 1e6 / 2, true
}

// pingLoop ports sendLatencyRequest/onLatencyComplete (§e): back-to-back in the
// unloaded phase; otherwise one ping about every LatencyInterval per worker.
func (a *attempt) pingLoop(w *worker, periodic bool) {
	defer a.wg.Done()
	o := &a.e.opts
	for a.ctx.Err() == nil {
		lat, ok := a.ping(w)
		if a.ctx.Err() != nil {
			return
		}
		if ok {
			w.addLatency(lat)
		}
		if !periodic {
			continue
		}
		delay := o.LatencyInterval
		if ok && lat != 0 {
			delay = max(o.LatencyInterval-time.Duration(lat*float64(time.Millisecond)), 0)
		}
		if !sleepCtx(a.ctx, delay) {
			return
		}
	}
}

// loadedLatency ports computeLoadedLatency (§e): min over workers of the
// LoadedPercentile, using only workers with more than LoadedMinSamples samples
// when any has them. ok is false when no worker has a sample yet.
func (a *attempt) loadedLatency() (value float64, ok bool) {
	o := &a.e.opts
	var enough, all []float64
	for _, w := range a.workerList() {
		lat := w.latencySamples()
		p, has := percentile(lat, o.LoadedPercentile)
		if !has {
			continue
		}
		all = append(all, p)
		if len(lat) > o.LoadedMinSamples {
			enough = append(enough, p)
		}
	}
	if len(enough) > 0 {
		return minOf(enough), true
	}
	if len(all) > 0 {
		return minOf(all), true
	}
	return 0, false
}

// unloadedLatency ports computeUnloadedLatency (§e): min over workers of the
// UnloadedPercentile plus the total sample count.
func (a *attempt) unloadedLatency() (value float64, count int, ok bool) {
	var vals []float64
	for _, w := range a.workerList() {
		lat := w.latencySamples()
		count += len(lat)
		if p, has := percentile(lat, a.e.opts.UnloadedPercentile); has {
			vals = append(vals, p)
		}
	}
	if len(vals) == 0 {
		return 0, count, false
	}
	return minOf(vals), count, true
}

func minOf(vals []float64) float64 {
	m := vals[0]
	for _, v := range vals[1:] {
		m = min(m, v)
	}
	return m
}

// sleepCtx waits for d or until ctx is done, reporting whether the full delay elapsed.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
