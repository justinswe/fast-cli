package engine

import "math"

// measurement is one progress interval of a single connection; times are ms on the run clock.
type measurement struct {
	bytes      float64
	start, end float64
}

// snapshot is one tick's merge of every connection: bytes over the union of busy time (ms).
type snapshot struct {
	bytes, time, start, end float64
}

// workerFeed is what one worker hands the snapshotter per tick.
type workerFeed struct {
	items   []measurement // recorded since the previous tick
	lastEnd float64       // end of the worker's most recent measurement ever
}

// snapshotter ports fast.com's snapshot module (PROTOCOL.md §d, app.pretty.js L1032–1226).
type snapshotter struct {
	snapshots []snapshot
	overflow  [][]measurement // per worker: items carried past the previous snapshot end
}

// compute merges this tick's measurements into one snapshot. It returns false,
// producing no snapshot, when any worker contributed nothing new and carries
// no overflow (§d step 1). Unlike the JS, nothing is dropped in that case
// (§4 item 2): the items are kept for the next tick.
func (s *snapshotter) compute(feeds []workerFeed) (snapshot, bool) {
	for len(s.overflow) < len(feeds) {
		s.overflow = append(s.overflow, nil)
	}
	for i, f := range feeds {
		if len(f.items) == 0 && len(s.overflow[i]) == 0 {
			for j := range feeds {
				s.overflow[j] = append(s.overflow[j], feeds[j].items...)
			}
			return snapshot{}, false
		}
	}

	lists := make([][]measurement, len(feeds))
	end := math.Inf(1)
	for i, f := range feeds {
		lists[i] = append(s.overflow[i], f.items...)
		s.overflow[i] = nil
		if f.lastEnd < end {
			end = f.lastEnd
		}
	}

	// Trim (L1087–1135): items starting at or after end go to overflow, in pop
	// order as the JS pushes them; the straddling item is split proportionally.
	for i, list := range lists {
		j := len(list) - 1
		for ; j >= 0 && list[j].start >= end; j-- {
			s.overflow[i] = append(s.overflow[i], list[j])
		}
		list = list[:j+1]
		if j >= 0 && list[j].end > end {
			m := &list[j]
			ov := measurement{start: end, end: m.end}
			ov.bytes = m.bytes * (m.end - end) / (m.end - m.start)
			m.bytes -= ov.bytes
			m.end = end
			s.overflow[i] = append(s.overflow[i], ov)
		}
		lists[i] = list
	}

	// Merge (L1136–1197): k-way by ascending start over per-worker cursors.
	var snap snapshot
	cursors := make([]int, len(lists))
	first := true
	var cur float64
	for {
		best := -1
		for i, list := range lists {
			if cursors[i] >= len(list) {
				continue
			}
			if best < 0 || list[cursors[i]].start < lists[best][cursors[best]].start {
				best = i
			}
		}
		if best < 0 {
			break
		}
		m := lists[best][cursors[best]]
		cursors[best]++
		if first {
			snap.start, cur, first = m.start, m.start, false
		}
		if m.start > cur {
			cur = m.start
		}
		if m.end > cur {
			snap.time += m.end - cur
			cur = m.end
		}
		snap.bytes += m.bytes
	}
	if first {
		return snapshot{}, false
	}
	snap.end = cur
	s.snapshots = append(s.snapshots, snap)
	return snap, true
}
