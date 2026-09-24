// Package engine runs the fast.com speed-test measurement algorithm.
//
// Every constant below carries fast.com's value in DefaultOptions and cites
// the section of docs/PROTOCOL.md it was taken from.
package engine

import (
	"net/http"
	"time"

	"github.com/jfernbaugh/fast-cli/internal/fastcom"
)

// Phase identifies which measurement a Sample belongs to.
type Phase string

const (
	// PhaseDownload is the download measurement phase.
	PhaseDownload Phase = "download"
	// PhaseUpload is the upload measurement phase.
	PhaseUpload Phase = "upload"
	// PhaseLatency is the dedicated unloaded-latency phase between download and upload.
	PhaseLatency Phase = "latency"
)

// Sample is one progress tick reported during a measurement phase.
type Sample struct {
	Phase       Phase
	Elapsed     time.Duration // fast.com testTime: since the phase's first target URL was obtained
	SpeedBps    float64       // bits/s, the moving-average value fast.com would display (0 for PhaseLatency)
	LatencyMs   float64       // loaded latency so far, 0 if none; the running unloaded estimate for PhaseLatency
	Connections int
	Stable      bool // true on the tick that completed the phase
}

// Result is the final outcome of a speed-test run.
type Result struct {
	DownloadBps             float64
	UploadBps               float64
	UnloadedLatencyMs       float64
	LoadedDownloadLatencyMs float64
	LoadedUploadLatencyMs   float64
	UploadRan               bool
	Client                  fastcom.Client
	Servers                 []fastcom.Target // targets returned by the API for this run (last fetch)
	DownloadDuration        time.Duration    // fast.com testTime of the successful download attempt
	UploadDuration          time.Duration    // fast.com testTime of the successful upload attempt
}

// Options configures a speed-test run. Start from DefaultOptions(); Run fills
// any zero-valued numeric field with its default.
type Options struct {
	HTTPClient *http.Client // nil: a keep-alive Transport sized for MaxConnections is built
	Token      string       // empty: fastcom.FetchToken, falling back to fastcom.DefaultToken
	Upload     bool         // run upload + loaded-upload latency, like fast.com "show more info"
	UserAgent  string       // sent on every request when non-empty

	// Target API.
	URLCount int // targets requested from the API; PROTOCOL.md §b (urlCount)

	// Sampling and aggregation.
	ProgressInterval       time.Duration // tick period; §d (progressFrequencyMs)
	WindowSize             int           // stableMovingAverage window in snapshots; §d
	SnapshotResetThreshold int           // aggregator resets while fewer snapshots exist; §d

	// Duration gates and stability rule.
	MinDuration           time.Duration // stopper consulted only after this; §d (config.duration.min)
	MaxDuration           time.Duration // hard stop of a phase; §d (config.duration.max)
	StableMinDuration     time.Duration // stopper minDuration; §d
	StableMaxDuration     time.Duration // stopper maxDuration; §d
	StabilityDelta        float64       // percent; §d (stabilityDelta)
	MinStableMeasurements int           // §d (minStableMeasurements)

	// Connections and ramp thresholds (bits/s); §c Concurrency.
	MinConnections   int
	MaxConnections   int
	ScaleFromOneBps  float64 // 1 worker -> 3 above this (evaluated last, overrides the chain)
	ScaleTo3Bps      float64 // fewer than 3 workers -> 3
	ScaleTo5Bps      float64 // fewer than 5 workers -> 5
	ScaleToMaxBps    float64 // -> MaxConnections
	MaxBytesInFlight int64   // unranged-URL condition on attempt >= 3; §c (maxBytesInFlight)

	// Request sizes.
	FirstRequestBytes int64 // warm-up request size; §c (firstRequestBytes, §4 item 1)
	MaxPayloadBytes   int64 // full range size, decremented per completed request; §c (MAX_PAYLOAD_BYTES)

	// Latency.
	LatencyWorkers      int           // unloaded-phase workers; §e (min(5, connections.max))
	UnloadedMinDuration time.Duration // §e (testTime > 5 s)
	UnloadedMinSamples  int           // §e (count > 50)
	UnloadedMaxDuration time.Duration // cap absent in fast.com; §4 item 4
	UnloadedPercentile  float64       // §e (0.10)
	LoadedPercentile    float64       // §e (0.75)
	LoadedMinSamples    int           // workers with more samples than this count; §e (> 4)
	LatencyInterval     time.Duration // loaded ping period; §e (latencyMeasurementsFrequencyMs)

	// Timeouts and retries.
	FirstProgressTimeout time.Duration // watchdog until the first progress event; §h (requestTimeoutMs)
	RequestTimeout       time.Duration // whole request; §h (xhr.timeout)
	MaxAttempts          int           // §c Failure handling (maxAttempts)
	RetryBackoffCap      time.Duration // backoff is min(2^attempt ms, cap); §c Failure handling

	// Bad-URL bookkeeping; §b.
	BadOCAFailures  int // ocaFailures >= this marks every URL of the host bad
	BadSiteFailures int // siteFailures >= this marks the site bad
	RefetchBelow    int // refetch targets when fewer usable remain
}

// DefaultOptions returns Options populated with fast.com's algorithm constants.
func DefaultOptions() Options {
	return Options{
		URLCount:               fastcom.DefaultURLCount, // §b: urlCount=5
		ProgressInterval:       150 * time.Millisecond,  // §d: progressFrequencyMs 150
		WindowSize:             5,                       // §d: stableMovingAverage(5)
		SnapshotResetThreshold: 5,                       // §d: snapshotResetThreshold 5
		MinDuration:            5 * time.Second,         // §d: duration.min 5
		MaxDuration:            30 * time.Second,        // §d: duration.max 30
		StableMinDuration:      7 * time.Second,         // §d: stopper minDuration 7
		StableMaxDuration:      30 * time.Second,        // §d: stopper maxDuration 30
		StabilityDelta:         2,                       // §d: stabilityDelta 2 %
		MinStableMeasurements:  6,                       // §d: minStableMeasurements 6
		MinConnections:         1,                       // §c: connections.min 1
		MaxConnections:         8,                       // §c: connections.max 8
		ScaleFromOneBps:        5e5,                     // §c: speed > 5e5 && active < 2 -> 3
		ScaleTo3Bps:            1e6,                     // §c: speed > 1e6 && active < 3 -> 3
		ScaleTo5Bps:            1e7,                     // §c: speed > 1e7 && active < 5 -> 5
		ScaleToMaxBps:          5e7,                     // §c: speed > 5e7 -> connections.max
		MaxBytesInFlight:       78643200,                // §c: maxBytesInFlight 3 x 25 MiB
		FirstRequestBytes:      2048,                    // §c: firstRequestBytes 2048
		MaxPayloadBytes:        26214400,                // §c: MAX_PAYLOAD_BYTES 25 MiB
		LatencyWorkers:         5,                       // §e: min(5, connections.max)
		UnloadedMinDuration:    5 * time.Second,         // §e: testTime > 5
		UnloadedMinSamples:     50,                      // §e: count > 50
		UnloadedMaxDuration:    30 * time.Second,        // §4 item 4: recommended cap
		UnloadedPercentile:     0.10,                    // §e: percentile(samples, 0.10)
		LoadedPercentile:       0.75,                    // §e: percentile(samples, 0.75)
		LoadedMinSamples:       4,                       // §e: length > 4
		LatencyInterval:        time.Second,             // §e: latencyMeasurementsFrequencyMs 1000
		FirstProgressTimeout:   9 * time.Second,         // §h: requestTimeoutMs 9000
		RequestTimeout:         60 * time.Second,        // §h: xhr.timeout 60000
		MaxAttempts:            10,                      // §c: maxAttempts 10
		RetryBackoffCap:        200 * time.Millisecond,  // §c: min(2^attempt, 200) ms
		BadOCAFailures:         2,                       // §b: ocaFailures >= 2
		BadSiteFailures:        3,                       // §b: siteFailures >= 3
		RefetchBelow:           3,                       // §b: testUrls.length < 3
	}
}

// withDefaults fills zero-valued numeric fields from DefaultOptions.
func (o Options) withDefaults() Options {
	d := DefaultOptions()
	if o.URLCount <= 0 {
		o.URLCount = d.URLCount
	}
	if o.ProgressInterval <= 0 {
		o.ProgressInterval = d.ProgressInterval
	}
	if o.WindowSize <= 0 {
		o.WindowSize = d.WindowSize
	}
	if o.SnapshotResetThreshold <= 0 {
		o.SnapshotResetThreshold = d.SnapshotResetThreshold
	}
	if o.MinDuration <= 0 {
		o.MinDuration = d.MinDuration
	}
	if o.MaxDuration <= 0 {
		o.MaxDuration = d.MaxDuration
	}
	if o.StableMinDuration <= 0 {
		o.StableMinDuration = d.StableMinDuration
	}
	if o.StableMaxDuration <= 0 {
		o.StableMaxDuration = d.StableMaxDuration
	}
	if o.StabilityDelta <= 0 {
		o.StabilityDelta = d.StabilityDelta
	}
	if o.MinStableMeasurements <= 0 {
		o.MinStableMeasurements = d.MinStableMeasurements
	}
	if o.MinConnections <= 0 {
		o.MinConnections = d.MinConnections
	}
	if o.MaxConnections <= 0 {
		o.MaxConnections = d.MaxConnections
	}
	if o.ScaleFromOneBps <= 0 {
		o.ScaleFromOneBps = d.ScaleFromOneBps
	}
	if o.ScaleTo3Bps <= 0 {
		o.ScaleTo3Bps = d.ScaleTo3Bps
	}
	if o.ScaleTo5Bps <= 0 {
		o.ScaleTo5Bps = d.ScaleTo5Bps
	}
	if o.ScaleToMaxBps <= 0 {
		o.ScaleToMaxBps = d.ScaleToMaxBps
	}
	if o.MaxBytesInFlight <= 0 {
		o.MaxBytesInFlight = d.MaxBytesInFlight
	}
	if o.FirstRequestBytes <= 0 {
		o.FirstRequestBytes = d.FirstRequestBytes
	}
	if o.MaxPayloadBytes <= 0 {
		o.MaxPayloadBytes = d.MaxPayloadBytes
	}
	if o.LatencyWorkers <= 0 {
		o.LatencyWorkers = d.LatencyWorkers
	}
	if o.UnloadedMinDuration <= 0 {
		o.UnloadedMinDuration = d.UnloadedMinDuration
	}
	if o.UnloadedMinSamples <= 0 {
		o.UnloadedMinSamples = d.UnloadedMinSamples
	}
	if o.UnloadedMaxDuration <= 0 {
		o.UnloadedMaxDuration = d.UnloadedMaxDuration
	}
	if o.UnloadedPercentile <= 0 {
		o.UnloadedPercentile = d.UnloadedPercentile
	}
	if o.LoadedPercentile <= 0 {
		o.LoadedPercentile = d.LoadedPercentile
	}
	if o.LoadedMinSamples <= 0 {
		o.LoadedMinSamples = d.LoadedMinSamples
	}
	if o.LatencyInterval <= 0 {
		o.LatencyInterval = d.LatencyInterval
	}
	if o.FirstProgressTimeout <= 0 {
		o.FirstProgressTimeout = d.FirstProgressTimeout
	}
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = d.RequestTimeout
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = d.MaxAttempts
	}
	if o.RetryBackoffCap <= 0 {
		o.RetryBackoffCap = d.RetryBackoffCap
	}
	if o.BadOCAFailures <= 0 {
		o.BadOCAFailures = d.BadOCAFailures
	}
	if o.BadSiteFailures <= 0 {
		o.BadSiteFailures = d.BadSiteFailures
	}
	if o.RefetchBelow <= 0 {
		o.RefetchBelow = d.RefetchBelow
	}
	return o
}
