// Package output renders speed-test progress and results for humans and machines.
package output

import (
	"fmt"
	"math"
)

// FormatSpeed converts bits/s into fast.com's display value and unit (Kbps, Mbps or Gbps).
// It mirrors fast.com's convertSpeed: scale below 1 Mbps to Kbps and at or above 995 Mbps
// to Gbps, then show one decimal below 9.95, whole numbers below 100, and tens above.
func FormatSpeed(bps float64) (value, unit string) {
	v := bps / 1e3 / 1e3
	if math.IsNaN(v) {
		v = 0
	}
	unit = "Mbps"
	if v < 1 {
		v *= 1e3
		unit = "Kbps"
	} else if v >= 995 {
		v /= 1e3
		unit = "Gbps"
	}
	switch {
	case v < 9.95:
		value = fmt.Sprintf("%.1f", math.Round(10*v)/10)
	case v < 100:
		value = fmt.Sprintf("%.0f", math.Round(v))
	default:
		value = fmt.Sprintf("%.0f", 10*math.Round(v/10))
	}
	return value, unit
}

// FormatLatency converts milliseconds into fast.com's display value and unit (ms or s).
// It mirrors fast.com's convertLatency: whole milliseconds below one second, one decimal
// of seconds from one second up, and "0 ms" for missing or absurd (> 1e6 ms) values.
func FormatLatency(ms float64) (value, unit string) {
	if math.IsNaN(ms) || ms > 1e6 {
		return "0", "ms"
	}
	if ms >= 1e3 {
		return fmt.Sprintf("%.1f", math.Round(ms/1e3*10)/10), "s"
	}
	return fmt.Sprintf("%.0f", math.Round(ms)), "ms"
}

// Speed returns the fast.com display string for bits/s, e.g. "245 Mbps".
func Speed(bps float64) string {
	v, u := FormatSpeed(bps)
	return v + " " + u
}

// Latency returns the fast.com display string for milliseconds, e.g. "12 ms".
func Latency(ms float64) string {
	v, u := FormatLatency(ms)
	return v + " " + u
}
