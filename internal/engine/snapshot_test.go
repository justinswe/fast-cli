package engine

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestSnapshotUnionOfBusyTime(t *testing.T) {
	s := &snapshotter{}
	// Worker 0 busy 0-100 (1000 B) then 100-200 (1000 B); worker 1 busy 50-150 (500 B) then idle to 200.
	snap, ok := s.compute([]workerFeed{
		{items: []measurement{{1000, 0, 100}, {1000, 100, 200}}, lastEnd: 200},
		{items: []measurement{{500, 50, 150}, {0, 190, 200}}, lastEnd: 200},
	})
	if !ok {
		t.Fatal("expected a snapshot")
	}
	if snap.bytes != 2500 || !near(snap.time, 200) || snap.start != 0 || snap.end != 200 {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestSnapshotGapsExcludedOverlapsOnce(t *testing.T) {
	s := &snapshotter{}
	// 0-10 and 20-30 with a gap; 5-25 overlaps both: union is 0-30 = 30 ms.
	snap, ok := s.compute([]workerFeed{
		{items: []measurement{{10, 0, 10}, {10, 20, 30}}, lastEnd: 30},
		{items: []measurement{{20, 5, 25}, {0, 25, 30}}, lastEnd: 30},
	})
	if !ok || !near(snap.time, 30) || snap.bytes != 40 {
		t.Fatalf("snapshot = %+v ok=%v", snap, ok)
	}
	s = &snapshotter{}
	snap, ok = s.compute([]workerFeed{{items: []measurement{{10, 0, 10}, {10, 20, 30}}, lastEnd: 30}})
	if !ok || !near(snap.time, 20) || snap.start != 0 || snap.end != 30 {
		t.Fatalf("gap snapshot = %+v ok=%v", snap, ok)
	}
}

func TestSnapshotRequiresEveryWorkerAndKeepsItems(t *testing.T) {
	s := &snapshotter{}
	// Worker 1 has nothing yet: no snapshot, and worker 0's items must survive to the next tick.
	if _, ok := s.compute([]workerFeed{{items: []measurement{{100, 0, 10}}, lastEnd: 10}, {}}); ok {
		t.Fatal("expected no snapshot while a worker has no data")
	}
	if len(s.snapshots) != 0 {
		t.Fatal("no snapshot should be recorded")
	}
	snap, ok := s.compute([]workerFeed{
		{items: []measurement{{100, 10, 20}}, lastEnd: 20},
		{items: []measurement{{50, 15, 20}}, lastEnd: 20},
	})
	if !ok || snap.bytes != 250 || !near(snap.time, 20) || snap.start != 0 {
		t.Fatalf("snapshot = %+v ok=%v", snap, ok)
	}
}

func TestSnapshotTrimSplitsAndCarriesOverflow(t *testing.T) {
	s := &snapshotter{}
	// Worker 0 ends at 100; worker 1's second item straddles 100 and its third starts after it.
	snap, ok := s.compute([]workerFeed{
		{items: []measurement{{1000, 0, 100}}, lastEnd: 100},
		{items: []measurement{{400, 0, 80}, {200, 80, 120}, {300, 120, 150}}, lastEnd: 150},
	})
	if !ok {
		t.Fatal("expected a snapshot")
	}
	// Straddling item split proportionally: 200 * (120-100)/(120-80) = 100 bytes carried.
	if snap.end != 100 || !near(snap.bytes, 1000+400+100) || !near(snap.time, 100) {
		t.Fatalf("first snapshot = %+v", snap)
	}
	if got := s.overflow[1]; len(got) != 2 || got[0].start != 120 || got[1].start != 100 || !near(got[1].bytes, 100) {
		t.Fatalf("overflow = %+v", got)
	}
	// Next tick: worker 1 contributes only its carried items; worker 0 continues to 150.
	snap, ok = s.compute([]workerFeed{
		{items: []measurement{{500, 100, 150}}, lastEnd: 150},
		{lastEnd: 150},
	})
	if !ok || snap.start != 100 || snap.end != 150 || !near(snap.bytes, 500+100+300) || !near(snap.time, 50) {
		t.Fatalf("second snapshot = %+v ok=%v", snap, ok)
	}
	if len(s.overflow[1]) != 0 {
		t.Fatalf("overflow should be drained: %+v", s.overflow[1])
	}
}

func TestSnapshotZeroSpanItemIsCarried(t *testing.T) {
	s := &snapshotter{}
	// An item starting at the snapshot end is overflow, leaving nothing to merge: no snapshot, item kept.
	if _, ok := s.compute([]workerFeed{{items: []measurement{{0, 5, 5}}, lastEnd: 5}}); ok {
		t.Fatal("expected no snapshot")
	}
	if len(s.overflow[0]) != 1 {
		t.Fatalf("overflow = %+v", s.overflow[0])
	}
	snap, ok := s.compute([]workerFeed{{items: []measurement{{10, 5, 6}}, lastEnd: 6}})
	if !ok || snap.start != 5 || snap.end != 6 || snap.bytes != 10 {
		t.Fatalf("snapshot = %+v ok=%v", snap, ok)
	}
}
