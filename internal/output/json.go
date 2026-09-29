package output

import (
	"encoding/json"
	"io"
	"time"

	"github.com/justinswe/fast-cli/internal/engine"
)

// JSONResult is the stable machine-readable schema printed by --json.
// Speeds are raw megabits per second (not fast.com-rounded); latencies are raw milliseconds.
// upload_mbps, latency_ms.loaded_upload and durations.upload_seconds are null when upload did not run.
type JSONResult struct {
	Version      string        `json:"version"`
	Timestamp    string        `json:"timestamp"` // RFC3339
	DownloadMbps float64       `json:"download_mbps"`
	UploadMbps   *float64      `json:"upload_mbps"`
	LatencyMs    JSONLatency   `json:"latency_ms"`
	Client       JSONClient    `json:"client"`
	Servers      []JSONServer  `json:"servers"`
	Durations    JSONDurations `json:"durations"`
}

// JSONLatency holds the unloaded and loaded latency measurements in milliseconds.
type JSONLatency struct {
	Unloaded       float64  `json:"unloaded"`
	LoadedDownload float64  `json:"loaded_download"`
	LoadedUpload   *float64 `json:"loaded_upload"`
}

// JSONClient describes the caller as reported by the fast.com API.
type JSONClient struct {
	IP      string `json:"ip"`
	ASN     string `json:"asn"`
	ISP     string `json:"isp"`
	City    string `json:"city"`
	Country string `json:"country"`
}

// JSONServer describes one speed-test server used for the run.
type JSONServer struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	City    string `json:"city"`
	Country string `json:"country"`
}

// JSONDurations holds how long each measurement phase took, in seconds.
type JSONDurations struct {
	DownloadSeconds float64  `json:"download_seconds"`
	UploadSeconds   *float64 `json:"upload_seconds"`
}

// NewJSONResult converts an engine.Result into the --json schema.
func NewJSONResult(r *engine.Result, version string, now time.Time) JSONResult {
	out := JSONResult{
		Version:      version,
		Timestamp:    now.Format(time.RFC3339),
		DownloadMbps: r.DownloadBps / 1e6,
		LatencyMs: JSONLatency{
			Unloaded:       r.UnloadedLatencyMs,
			LoadedDownload: r.LoadedDownloadLatencyMs,
		},
		Client: JSONClient{
			IP:      r.Client.IP,
			ASN:     r.Client.ASN,
			ISP:     r.Client.ISP,
			City:    r.Client.Location.City,
			Country: r.Client.Location.Country,
		},
		Servers:   make([]JSONServer, 0, len(r.Servers)),
		Durations: JSONDurations{DownloadSeconds: r.DownloadDuration.Seconds()},
	}
	for _, s := range r.Servers {
		out.Servers = append(out.Servers, JSONServer{
			Name:    s.Name,
			URL:     s.URL,
			City:    s.Location.City,
			Country: s.Location.Country,
		})
	}
	if r.UploadRan {
		up := r.UploadBps / 1e6
		loaded := r.LoadedUploadLatencyMs
		secs := r.UploadDuration.Seconds()
		out.UploadMbps = &up
		out.LatencyMs.LoadedUpload = &loaded
		out.Durations.UploadSeconds = &secs
	}
	return out
}

// WriteJSON writes the result as exactly one newline-terminated JSON object.
func WriteJSON(w io.Writer, r *engine.Result, version string, now time.Time) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false) // keep '&' in server URLs readable
	return enc.Encode(NewJSONResult(r, version, now))
}
