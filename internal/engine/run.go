package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/jfernbaugh/fast-cli/internal/fastcom"
)

// engine holds what every phase shares: options, the HTTP client, the run clock and the upload blob.
type engine struct {
	opts     Options
	hc       *http.Client
	base     time.Time
	blob     []byte
	progress func(Sample)
}

// now is the run clock in fractional milliseconds, like performance.now().
func (e *engine) now() float64 { return float64(time.Since(e.base).Nanoseconds()) / 1e6 }

// phaseResult is what a completed download/upload attempt yields.
type phaseResult struct {
	speedBps  float64
	latencyMs float64 // loaded latency, 0 if none
	elapsed   time.Duration
}

// Run executes the speed test in fast.com's order (download with loaded pings,
// unloaded latency, then upload with loaded pings when opts.Upload) and calls
// progress on every sampling tick. Targets are fetched from the fast.com API
// on the first worker start, exactly as the web app does.
func Run(ctx context.Context, opts Options, progress func(Sample)) (*Result, error) {
	opts = opts.withDefaults()
	e := &engine{opts: opts, hc: newClient(&opts), base: time.Now(), progress: progress}
	if e.progress == nil {
		e.progress = func(Sample) {}
	}

	token := opts.Token
	if token == "" {
		tok, err := fastcom.FetchToken(ctx, e.hc)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			tok = fastcom.DefaultToken
		}
		token = tok
	}
	t := newTargets(&e.opts, e.hc, token)

	dl, err := e.runTransfer(ctx, PhaseDownload, t)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	unloaded, err := e.runLatency(ctx, t)
	if err != nil {
		return nil, fmt.Errorf("latency: %w", err)
	}
	res := &Result{
		DownloadBps:             dl.speedBps,
		LoadedDownloadLatencyMs: dl.latencyMs,
		DownloadDuration:        dl.elapsed,
		UnloadedLatencyMs:       unloaded,
	}
	if opts.Upload {
		e.blob = genBlob(opts.MaxPayloadBytes, randomFloat)
		up, err := e.runTransfer(ctx, PhaseUpload, t)
		if err != nil {
			return nil, fmt.Errorf("upload: %w", err)
		}
		res.UploadBps, res.LoadedUploadLatencyMs, res.UploadDuration, res.UploadRan = up.speedBps, up.latencyMs, up.elapsed, true
	}
	res.Client, res.Servers = t.info()
	return res, nil
}

// attemptLoop ports tester.test (PROTOCOL.md §c Failure handling): run attempts
// with min(2^attempt, cap) ms backoff until one completes or MaxAttempts is exceeded.
func (e *engine) attemptLoop(ctx context.Context, phase Phase, t *targets, run func(*attempt) error) error {
	var lastErr error
	for n := 1; ; n++ {
		if n > e.opts.MaxAttempts {
			return fmt.Errorf("giving up after %d attempts: %w", e.opts.MaxAttempts, lastErr)
		}
		if n > 1 {
			backoff := time.Duration(math.Min(math.Pow(2, float64(n)), float64(e.opts.RetryBackoffCap.Milliseconds()))) * time.Millisecond
			if !sleepCtx(ctx, backoff) {
				return ctx.Err()
			}
		}
		a := newAttempt(ctx, e, phase, n)
		err := run(a)
		a.stop()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var ce *connError
		if errors.As(err, &ce) {
			t.reportBad(ce.tpl)
		}
		lastErr = err
	}
}

// runTransfer runs the download or upload phase to completion.
func (e *engine) runTransfer(ctx context.Context, phase Phase, t *targets) (phaseResult, error) {
	var res phaseResult
	err := e.attemptLoop(ctx, phase, t, func(a *attempt) error {
		r, err := e.transferAttempt(a, t)
		res = r
		return err
	})
	return res, err
}

// transferAttempt ports reportMetrics for download/upload (§c, §d): MinConnections
// workers, a snapshot + aggregate + stopper pass every ProgressInterval, and the
// connection ramp. The result is the aggregator value at the completing tick.
func (e *engine) transferAttempt(a *attempt, t *targets) (phaseResult, error) {
	o := &e.opts
	for i := 0; i < o.MinConnections; i++ {
		if err := a.startWorker(t); err != nil {
			return phaseResult{}, err
		}
	}
	agg := &stableMovingAverage{windowSize: o.WindowSize, resetThreshold: o.SnapshotResetThreshold}
	stop := stopper{
		minDuration: o.StableMinDuration.Seconds(), maxDuration: o.StableMaxDuration.Seconds(),
		stabilityDelta: o.StabilityDelta, minStable: o.MinStableMeasurements,
	}
	snapper := &snapshotter{}
	var recorded []float64 // progressMeasurements: the aggregate speed of every active tick
	var speed float64
	timer := time.NewTimer(o.ProgressInterval)
	defer timer.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return phaseResult{}, a.ctx.Err()
		case ce := <-a.failCh:
			return phaseResult{}, ce
		case <-timer.C:
		}
		testTime := (e.now() - a.start) / 1e3
		workers := a.workerList()
		feeds := make([]workerFeed, len(workers))
		for i, w := range workers {
			feeds[i] = w.take()
		}
		snap, ok := snapper.compute(feeds)
		active := ok && snap.end-snap.start > 0
		haveSpeed, complete := false, false
		if active {
			speed, haveSpeed = agg.aggregate(snapper.snapshots), true
			complete = testTime > o.MinDuration.Seconds() && stop.completed(testTime, recorded, speed)
		}
		if testTime > o.MaxDuration.Seconds() {
			speed, haveSpeed, complete = agg.aggregate(snapper.snapshots), true, true
		}
		latency, _ := a.loadedLatency()
		elapsed := time.Duration(testTime * float64(time.Second))
		e.progress(Sample{Phase: a.phase, Elapsed: elapsed, SpeedBps: speed, LatencyMs: latency, Connections: len(workers), Stable: complete})
		if complete {
			return phaseResult{speedBps: speed, latencyMs: latency, elapsed: elapsed}, nil
		}
		if active {
			recorded = append(recorded, speed)
		}
		if n := len(workers); haveSpeed && n < o.MaxConnections {
			for i := n; i < e.desiredWorkers(speed, n); i++ {
				if err := a.startWorker(t); err != nil {
					return phaseResult{}, err
				}
			}
		}
		timer.Reset(o.ProgressInterval)
	}
}

// desiredWorkers ports the scale-up chain of reportMetrics (§c Concurrency); connections never shrink.
func (e *engine) desiredWorkers(speed float64, active int) int {
	o := &e.opts
	desired := active
	switch {
	case speed > o.ScaleToMaxBps:
		desired = o.MaxConnections
	case speed > o.ScaleTo5Bps && active < 5:
		desired = 5
	case speed > o.ScaleTo3Bps && active < 3:
		desired = 3
	}
	if speed > o.ScaleFromOneBps && active < 2 {
		desired = 3
	}
	return min(desired, o.MaxConnections)
}

// runLatency ports the unloaded-latency phase (§e): LatencyWorkers pinging
// back-to-back until UnloadedMinDuration has passed and more than
// UnloadedMinSamples samples exist, capped at UnloadedMaxDuration (§4 item 4).
func (e *engine) runLatency(ctx context.Context, t *targets) (float64, error) {
	o := &e.opts
	var value float64
	var ok bool
	err := e.attemptLoop(ctx, PhaseLatency, t, func(a *attempt) error {
		for i := 0; i < min(o.LatencyWorkers, o.MaxConnections); i++ {
			if err := a.startWorker(t); err != nil {
				return err
			}
		}
		timer := time.NewTimer(o.ProgressInterval)
		defer timer.Stop()
		for {
			select {
			case <-a.ctx.Done():
				return a.ctx.Err()
			case <-timer.C:
			}
			testTime := (e.now() - a.start) / 1e3
			v, count, has := a.unloadedLatency()
			complete := testTime > o.UnloadedMinDuration.Seconds() && count > o.UnloadedMinSamples
			if testTime > o.UnloadedMaxDuration.Seconds() {
				complete = true
			}
			e.progress(Sample{Phase: PhaseLatency, Elapsed: time.Duration(testTime * float64(time.Second)), LatencyMs: v, Connections: a.workerCount(), Stable: complete})
			if complete {
				value, ok = v, has
				return nil
			}
			timer.Reset(o.ProgressInterval)
		}
	})
	if err == nil && !ok {
		return 0, errors.New("no latency samples")
	}
	return value, err
}
