package engine

import (
	"math"
	"testing"
)

// ms of busy time carrying the given bytes/ms speed.
func snapAt(bytesPerMs float64) snapshot { return snapshot{bytes: bytesPerMs * 100, time: 100} }

func TestStableMovingAverageRampThenFixed(t *testing.T) {
	agg := &stableMovingAverage{windowSize: 5, resetThreshold: 5}
	var snaps []snapshot
	feed := func(bpm float64) float64 {
		snaps = append(snaps, snapAt(bpm))
		return agg.aggregate(snaps)
	}
	// Fewer than 5 snapshots: reset each time, value is the plain average so far.
	if got := feed(10); !near(got, 1e3*10*8) {
		t.Fatalf("n=1 got %v", got)
	}
	if got := feed(20); !near(got, 1e3*15*8) {
		t.Fatalf("n=2 got %v", got)
	}
	feed(30)
	feed(40)
	// n=5: window average of 10,20,30,40,50 = 30.
	if got := feed(50); !near(got, 1e3*30*8) {
		t.Fatalf("n=5 got %v", got)
	}
	// Still rising: window 20..60 = 40.
	if got := feed(60); !near(got, 1e3*40*8) {
		t.Fatalf("n=6 got %v", got)
	}
	// Drop: window 30,40,50,60,10 = 38 < 40 -> fixed at the peak window (indices 1..5, sum 200 over 500 ms),
	// cumulative average now includes snapshot 6: (200+10)/6 = 35.
	if got := feed(10); !near(got, 1e3*(210.0/6)*8) {
		t.Fatalf("fixed got %v", got)
	}
	// Later snapshots keep extending the cumulative average from the peak window.
	if got := feed(70); !near(got, 1e3*(280.0/7)*8) {
		t.Fatalf("cumulative got %v", got)
	}
	if !agg.fixed {
		t.Fatal("aggregator should stay fixed")
	}
}

func TestStableMovingAverageZeroTime(t *testing.T) {
	agg := &stableMovingAverage{windowSize: 5, resetThreshold: 5}
	if got := agg.aggregate([]snapshot{{bytes: 10}}); got != 0 {
		t.Fatalf("got %v, want 0 for zero busy time", got)
	}
}

func TestStopperRule(t *testing.T) {
	s := stopper{minDuration: 7, maxDuration: 30, stabilityDelta: 2, minStable: 6}
	flat := []float64{100, 101, 99, 100.5, 100, 99.5}
	if s.completed(6.9, flat, 100) {
		t.Fatal("must not stop before minDuration")
	}
	if !s.completed(7, flat, 100) {
		t.Fatal("six speeds within 2% at 7 s must stop")
	}
	if s.completed(7, flat[1:], 100) {
		t.Fatal("five measurements are not enough")
	}
	if s.completed(7, []float64{100, 100, 100, 100, 100, 97.9}, 100) {
		t.Fatal("a 2.1% deviation must not stop")
	}
	if !s.completed(7, []float64{100, 100, 100, 100, 100, 98.1}, 100) {
		t.Fatal("a 1.9% deviation is within delta")
	}
	// Delta is measured against the current aggregate: 100 vs cur 105 is 4.8%.
	if s.completed(7, flat, 105) {
		t.Fatal("recorded speeds far from the current speed must not stop")
	}
	// Rising over the last three ticks (max is the last) blocks completion.
	if s.completed(7, []float64{100, 100, 100, 100, 100.5, 101}, 101) {
		t.Fatal("rising speed must not stop")
	}
	// Max in the middle of the last three also blocks; max at the third-from-last passes.
	if s.completed(7, []float64{100, 100, 100, 100, 101, 100.5}, 100) {
		t.Fatal("max at n-2 must not stop")
	}
	if !s.completed(7, []float64{100, 100, 100, 101, 100.5, 100.5}, 100) {
		t.Fatal("max at n-3 must stop")
	}
	// Ties go to the earliest index, so a flat tail is not "rising".
	if !s.completed(7, []float64{100, 100, 100, 100, 100, 100}, 100) {
		t.Fatal("flat tail must stop")
	}
	// Hard stop regardless of stability or count.
	if !s.completed(30, []float64{1, 50}, 999) {
		t.Fatal("must stop at maxDuration")
	}
	if !s.completed(30, nil, 0) {
		t.Fatal("must stop at maxDuration with no measurements")
	}
}

func TestLastWindowMaxInd(t *testing.T) {
	if got := lastWindowMaxInd([]float64{1, 5, 3, 3}, 3); got != 1 {
		t.Fatalf("got %d, want 1", got)
	}
	if got := lastWindowMaxInd([]float64{9, 3, 5, 5}, 3); got != 2 {
		t.Fatalf("tie must go to the earliest in-window index: got %d, want 2", got)
	}
	if got := lastWindowMaxInd([]float64{1, 2}, 3); got != 1 {
		t.Fatalf("short list got %d, want 1", got)
	}
}

func TestPercentile(t *testing.T) {
	cases := []struct {
		vals []float64
		p    float64
		want float64
	}{
		{[]float64{42}, 0.10, 42},
		{[]float64{42}, 0.75, 42},
		{[]float64{10, 20}, 0.10, 11},
		{[]float64{20, 10}, 0.75, 17.5},
		{[]float64{5, 1, 3, 2, 4}, 0.10, 1.4},
		{[]float64{5, 1, 3, 2, 4}, 0.75, 4},
		{[]float64{5, 1, 3}, 0, 5}, // p <= 0 returns the first value before sorting, as fast.com does
		{[]float64{5, 1, 3}, 1, 3},
		{[]float64{math.NaN(), 7}, 0.5, 7},
	}
	for _, c := range cases {
		got, ok := percentile(c.vals, c.p)
		if !ok || !near(got, c.want) {
			t.Errorf("percentile(%v, %v) = %v,%v want %v", c.vals, c.p, got, ok, c.want)
		}
	}
	if _, ok := percentile(nil, 0.5); ok {
		t.Error("empty input must report no value")
	}
	if _, ok := percentile([]float64{math.NaN()}, 0.5); ok {
		t.Error("all-NaN input must report no value")
	}
}
