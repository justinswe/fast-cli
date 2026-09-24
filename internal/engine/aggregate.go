package engine

import (
	"math"
	"sort"
)

// stableMovingAverage ports fast.com's aggregator (PROTOCOL.md §d, app.pretty.js L60–111).
// While the windowed speed keeps rising it reports the last-window speed; once
// it first drops, the value becomes the cumulative average from the peak window on.
type stableMovingAverage struct {
	windowSize     int
	resetThreshold int

	startInd int
	curSpeed float64 // bytes per ms of the best window so far
	bytes    float64
	times    float64
	fixed    bool
}

func (a *stableMovingAverage) reset() {
	a.startInd, a.curSpeed, a.bytes, a.times, a.fixed = 0, 0, 0, 0, false
}

// aggregate returns the speed in bits/s for the snapshots recorded so far.
func (a *stableMovingAverage) aggregate(snaps []snapshot) float64 {
	n := len(snaps)
	if n < a.resetThreshold {
		a.reset()
	}
	if !a.fixed {
		var sumBytes, sumTime float64
		start := n - 1
		end := max(0, n-a.windowSize)
		for i := start; i >= end; i-- {
			sumBytes += snaps[i].bytes
			sumTime += snaps[i].time
		}
		speed := 0.0
		if sumTime > 0 {
			speed = sumBytes / sumTime
		}
		if speed >= a.curSpeed {
			a.startInd, a.curSpeed, a.bytes, a.times = start+1, speed, sumBytes, sumTime
		} else {
			a.fixed = true
		}
	}
	for i := a.startInd; i < n; i++ {
		a.bytes += snaps[i].bytes
		a.times += snaps[i].time
	}
	a.startInd = n
	if a.times > 0 {
		return 1e3 * a.bytes * 8 / a.times
	}
	return 0
}

// stopper ports stableDeltaMeasurementsStopper (PROTOCOL.md §d, app.pretty.js L1228–1320).
// Durations are seconds, delta is a percentage.
type stopper struct {
	minDuration    float64
	maxDuration    float64
	stabilityDelta float64
	minStable      int
}

// completed reports whether the phase may stop: at maxDuration, or once
// minStable recorded speeds sit within stabilityDelta % of cur, the speed has
// not risen over the last ceil(minStable/2) ticks, and minDuration has passed.
func (s stopper) completed(testTime float64, speeds []float64, cur float64) bool {
	if testTime >= s.maxDuration {
		return true
	}
	n := len(speeds)
	if n < s.minStable {
		return false
	}
	half := (s.minStable + 1) / 2
	if n-lastWindowMaxInd(speeds, half) < half {
		return false
	}
	if maxInd := max(0, n-s.minStable); n-maxInd < s.minStable {
		return false
	}
	if maxDelta(cur, speeds[n-s.minStable:]) > s.stabilityDelta {
		return false
	}
	return testTime >= s.minDuration
}

// lastWindowMaxInd returns the index of the largest of the last window speeds; ties go to the earliest.
func lastWindowMaxInd(speeds []float64, window int) int {
	var curMax float64
	curMaxInd := 0
	for i := len(speeds) - 1; i >= max(0, len(speeds)-window); i-- {
		if speeds[i] >= curMax {
			curMax, curMaxInd = speeds[i], i
		}
	}
	return curMaxInd
}

// maxDelta returns the largest percentage distance of speeds from value.
func maxDelta(value float64, speeds []float64) float64 {
	var m float64
	for _, s := range speeds {
		if d := 100 * math.Abs(s-value) / value; d > m {
			m = d
		}
	}
	return m
}

// percentile ports utils.percentile (PROTOCOL.md §e, app.pretty.js L3365–3385)
// with linear interpolation; a single sample returns itself and an empty input
// returns false instead of the JS NaN/undefined (§4 item 3).
func percentile(values []float64, p float64) (float64, bool) {
	vals := make([]float64, 0, len(values))
	for _, v := range values {
		if !math.IsNaN(v) {
			vals = append(vals, v)
		}
	}
	if len(vals) == 0 {
		return 0, false
	}
	if p <= 0 {
		return vals[0], true
	}
	if p >= 1 {
		return vals[len(vals)-1], true
	}
	sort.Float64s(vals)
	idx := float64(len(vals)-1) * p
	lo := int(math.Floor(idx))
	hi := lo + 1
	if hi >= len(vals) {
		return vals[lo], true
	}
	w := idx - float64(lo)
	return vals[lo]*(1-w) + vals[hi]*w, true
}
