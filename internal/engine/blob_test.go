package engine

import (
	"math/rand/v2"
	"testing"
)

func TestGenBlobSizeAndCharset(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for _, size := range []int64{1, 7, 2048, 26214400} {
		b := genBlob(size, r.Float64)
		if int64(len(b)) != size {
			t.Fatalf("size %d: got %d bytes", size, len(b))
		}
		for i, c := range b {
			if !(c >= '0' && c <= '9') && c != '.' {
				t.Fatalf("size %d: byte %d = %q, want ASCII digit or dot", size, i, c)
			}
		}
	}
	if string(genBlob(2048, r.Float64)) == string(genBlob(2048, r.Float64)) {
		t.Fatal("blob should be pseudo-random")
	}
}

func TestDesiredWorkers(t *testing.T) {
	e := &engine{opts: DefaultOptions()}
	cases := []struct {
		speed  float64
		active int
		want   int
	}{
		{4e5, 1, 1}, // below every threshold
		{6e5, 1, 3}, // > 0.5 Mbit/s with a single worker jumps to 3
		{2e6, 1, 3}, // > 1 Mbit/s
		{2e6, 3, 3}, // already 3
		{2e7, 1, 3}, // > 10 Mbit/s but the single-worker rule wins: 3, not 5
		{2e7, 3, 5}, // > 10 Mbit/s from 3 -> 5
		{2e7, 5, 5}, // stays
		{6e7, 1, 3}, // > 50 Mbit/s from one worker still goes to 3 first
		{6e7, 3, 8}, // then to max
		{6e7, 5, 8}, // 5 -> 8
		{6e5, 2, 2}, // 2 workers, low speed: nothing
	}
	for _, c := range cases {
		if got := e.desiredWorkers(c.speed, c.active); got != c.want {
			t.Errorf("desiredWorkers(%v, %d) = %d, want %d", c.speed, c.active, got, c.want)
		}
	}
}
