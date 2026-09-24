package output

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jfernbaugh/fast-cli/internal/engine"
	"github.com/jfernbaugh/fast-cli/internal/fastcom"
)

func sampleResult(upload bool) *engine.Result {
	return &engine.Result{
		DownloadBps:             245_600_000,
		UploadBps:               12_300_000,
		UnloadedLatencyMs:       12.4,
		LoadedDownloadLatencyMs: 45.1,
		LoadedUploadLatencyMs:   60.2,
		UploadRan:               upload,
		Client: fastcom.Client{
			IP: "34.19.110.48", ASN: "396982", ISP: "Google_LLC",
			Location: fastcom.Location{City: "The Dalles", Country: "US"},
		},
		Servers: []fastcom.Target{
			{Name: "n1", URL: "https://a.example/speedtest", Location: fastcom.Location{City: "Seattle", Country: "US"}},
			{Name: "n2", URL: "https://b.example/speedtest", Location: fastcom.Location{City: "San Jose", Country: "US"}},
		},
		DownloadDuration: 10*time.Second + 500*time.Millisecond,
		UploadDuration:   8 * time.Second,
	}
}

func TestWriteJSONShape(t *testing.T) {
	now := time.Date(2026, 9, 17, 20, 40, 0, 0, time.UTC)
	var buf bytes.Buffer
	if err := WriteJSON(&buf, sampleResult(true), "1.2.3", now); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(buf.String(), "\n"); n != 1 || !strings.HasSuffix(buf.String(), "\n") {
		t.Fatalf("want exactly one newline-terminated object, got %q", buf.String())
	}

	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"version", "timestamp", "download_mbps", "upload_mbps", "latency_ms", "client", "servers", "durations"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing top-level key %q", k)
		}
	}
	if m["version"] != "1.2.3" || m["timestamp"] != "2026-09-17T20:40:00Z" {
		t.Errorf("version/timestamp = %v/%v", m["version"], m["timestamp"])
	}
	if m["download_mbps"] != 245.6 || m["upload_mbps"] != 12.3 {
		t.Errorf("speeds = %v/%v", m["download_mbps"], m["upload_mbps"])
	}
	lat := m["latency_ms"].(map[string]any)
	if lat["unloaded"] != 12.4 || lat["loaded_download"] != 45.1 || lat["loaded_upload"] != 60.2 {
		t.Errorf("latency_ms = %v", lat)
	}
	client := m["client"].(map[string]any)
	for _, k := range []string{"ip", "asn", "isp", "city", "country"} {
		if _, ok := client[k]; !ok {
			t.Errorf("missing client key %q", k)
		}
	}
	servers := m["servers"].([]any)
	if len(servers) != 2 {
		t.Fatalf("servers len = %d", len(servers))
	}
	for _, k := range []string{"name", "url", "city", "country"} {
		if _, ok := servers[0].(map[string]any)[k]; !ok {
			t.Errorf("missing server key %q", k)
		}
	}
	dur := m["durations"].(map[string]any)
	if dur["download_seconds"] != 10.5 || dur["upload_seconds"] != 8.0 {
		t.Errorf("durations = %v", dur)
	}
}

func TestWriteJSONNoUpload(t *testing.T) {
	var buf bytes.Buffer
	r := sampleResult(false)
	r.Servers = nil
	if err := WriteJSON(&buf, r, "dev", time.Now()); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if v, ok := m["upload_mbps"]; !ok || v != nil {
		t.Errorf("upload_mbps = %v (present=%v), want null", v, ok)
	}
	if v := m["latency_ms"].(map[string]any)["loaded_upload"]; v != nil {
		t.Errorf("loaded_upload = %v, want null", v)
	}
	if v := m["durations"].(map[string]any)["upload_seconds"]; v != nil {
		t.Errorf("upload_seconds = %v, want null", v)
	}
	if v, ok := m["servers"].([]any); !ok || len(v) != 0 {
		t.Errorf("servers = %v, want empty array", m["servers"])
	}
}

func TestJSONRoundTrip(t *testing.T) {
	want := NewJSONResult(sampleResult(true), "dev", time.Now())
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got JSONResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("round trip mismatch:\n want %+v\n got  %+v", want, got)
	}
}
