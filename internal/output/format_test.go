package output

import "testing"

// Expected values were cross-checked against fast.com's convertSpeed/convertLatency in node.
func TestFormatSpeed(t *testing.T) {
	cases := []struct {
		bps         float64
		value, unit string
	}{
		{0, "0.0", "Kbps"},
		{500e3, "500", "Kbps"},
		{999_949, "1000", "Kbps"},
		{999_999, "1000", "Kbps"},
		{1e6, "1.0", "Mbps"},
		{9.94e6, "9.9", "Mbps"},
		{9.95e6, "10", "Mbps"},
		{99.4e6, "99", "Mbps"},
		{99.5e6, "100", "Mbps"},
		{100e6, "100", "Mbps"},
		{104e6, "100", "Mbps"},
		{105e6, "110", "Mbps"},
		{994e6, "990", "Mbps"},
		{995e6, "1.0", "Gbps"},
		{9.95e9, "10", "Gbps"},
		{10e9, "10", "Gbps"},
	}
	for _, c := range cases {
		v, u := FormatSpeed(c.bps)
		if v != c.value || u != c.unit {
			t.Errorf("FormatSpeed(%v) = %q %q, want %q %q", c.bps, v, u, c.value, c.unit)
		}
	}
	if got := Speed(245e6); got != "250 Mbps" {
		t.Errorf("Speed(245e6) = %q, want %q", got, "250 Mbps")
	}
}

func TestFormatLatency(t *testing.T) {
	cases := []struct {
		ms          float64
		value, unit string
	}{
		{0, "0", "ms"},
		{12.4, "12", "ms"},
		{12.5, "13", "ms"},
		{999.4, "999", "ms"},
		{999.5, "1000", "ms"},
		{1000, "1.0", "s"},
		{1550, "1.6", "s"},
		{1e6, "1000.0", "s"},
		{1e6 + 1, "0", "ms"},
	}
	for _, c := range cases {
		v, u := FormatLatency(c.ms)
		if v != c.value || u != c.unit {
			t.Errorf("FormatLatency(%v) = %q %q, want %q %q", c.ms, v, u, c.value, c.unit)
		}
	}
	if got := Latency(12.4); got != "12 ms" {
		t.Errorf("Latency(12.4) = %q, want %q", got, "12 ms")
	}
}
