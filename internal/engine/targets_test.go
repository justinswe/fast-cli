package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/justinswe/fast-cli/internal/fastcom"
)

const tpl = "https://ipv4-c204-sea001-ix.1.oca.nflxvideo.net/speedtest/range/?c=us&n=396982&v=319&e=1&t=sig"

func TestURLRewrites(t *testing.T) {
	if got := rangeURL(tpl, 2048); got != "https://ipv4-c204-sea001-ix.1.oca.nflxvideo.net/speedtest/range/0-2048?c=us&n=396982&v=319&e=1&t=sig" {
		t.Errorf("rangeURL = %s", got)
	}
	if got := rangeURL(tpl, 26214399); !strings.Contains(got, "/speedtest/range/0-26214399?") {
		t.Errorf("rangeURL = %s", got)
	}
	if got := pingURL(tpl); got != "https://ipv4-c204-sea001-ix.1.oca.nflxvideo.net/speedtest/range/0-0?c=us&n=396982&v=319&e=1&t=sig" {
		t.Errorf("pingURL = %s", got)
	}
	if got := unrangedURL(tpl); got != "https://ipv4-c204-sea001-ix.1.oca.nflxvideo.net/speedtest?c=us&n=396982&v=319&e=1&t=sig" {
		t.Errorf("unrangedURL = %s", got)
	}
	if got := hostOf(tpl); got != "ipv4-c204-sea001-ix.1.oca.nflxvideo.net" {
		t.Errorf("hostOf = %s", got)
	}
	target := "https://ipv4-c204-sea001-ix.1.oca.nflxvideo.net/speedtest?c=us&n=1"
	if got := strings.Replace(target, "speedtest", "speedtest/range/", 1); got != "https://ipv4-c204-sea001-ix.1.oca.nflxvideo.net/speedtest/range/?c=us&n=1" {
		t.Errorf("template = %s", got)
	}
}

func TestParseTargetURL(t *testing.T) {
	norm, oca, site := parseTargetURL(tpl)
	if norm != "https://ipv4-c204-sea001-ix.1.oca.nflxvideo.net/speedtest?c=us&n=396982&v=319&e=1&t=sig" {
		t.Errorf("normalized = %s", norm)
	}
	if oca != "ipv4-c204-sea001-ix.1.oca.nflxvideo.net" || site != "oca" {
		t.Errorf("oca=%s site=%s", oca, site)
	}
	if norm, _, _ := parseTargetURL(rangeURL(tpl, 5)); !strings.HasSuffix(norm, "/speedtest?c=us&n=396982&v=319&e=1&t=sig") {
		t.Errorf("ranged normalized = %s", norm)
	}
}

func TestURLReporterThresholds(t *testing.T) {
	r := newURLReporter(2, 3)
	a1 := "https://a.1.oca.net/speedtest/range/?t=1"
	a2 := "https://a.1.oca.net/speedtest/range/?t=2"
	b := "https://b.1.oca.net/speedtest/range/?t=3"
	c := "https://c.1.oca.net/speedtest/range/?t=4"
	d := "https://d.1.oca.net/speedtest/range/?t=5"
	r.report(a1)
	if !r.isBad(a1) || r.isBad(a2) {
		t.Fatal("only the exact URL is bad after one failure")
	}
	r.report(a2)
	if !r.isBad("https://a.1.oca.net/speedtest/range/?t=9") {
		t.Fatal("two failures make every URL of the host bad")
	}
	r.report(b)
	if r.isBad(d) {
		t.Fatal("two distinct hosts failing do not blacklist the site")
	}
	r.report(c)
	if !r.isBad(d) {
		t.Fatal("three hosts each failing once mark the shared site bad")
	}
}

// fakeAPI serves the targets endpoint with n targets on distinct hosts; with
// sameSite every host shares the third label like real OCAs (PROTOCOL.md §4 item 6).
func fakeAPI(t *testing.T, n int, sameSite bool, hits *atomic.Int32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/netflix/speedtest/v2" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		resp := fastcom.Response{Client: fastcom.Client{IP: "1.2.3.4", ASN: "1"}}
		for i := 0; i < n; i++ {
			site := fmt.Sprintf("s%d", i)
			if sameSite {
				site = "oca"
			}
			u := fmt.Sprintf("https://oca%d.1.%s.test/speedtest?c=us&n=1&v=1&e=1&t=x", i, site)
			resp.Targets = append(resp.Targets, fastcom.Target{Name: u, URL: u, Location: fastcom.Location{City: fmt.Sprint(i)}})
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
}

// rewriteTransport sends every request to the test server, keeping path and query.
type rewriteTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.URL.Scheme, r.URL.Host = t.target.Scheme, t.target.Host
	return t.base.RoundTrip(r)
}

func testClient(t *testing.T, h http.Handler) (*http.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = 32
	return &http.Client{Transport: rewriteTransport{target: u, base: tr}}, srv
}

func TestTargetsRotationAndRefetch(t *testing.T) {
	var hits atomic.Int32
	hc, srv := testClient(t, fakeAPI(t, 5, false, &hits))
	defer srv.Close()
	opts := DefaultOptions()
	tg := newTargets(&opts, hc, "tok")
	ctx := context.Background()
	var got []string
	for i := 0; i < 7; i++ {
		u, err := tg.next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, hostOf(u))
	}
	want := []string{"oca0.1.s0.test", "oca1.1.s1.test", "oca2.1.s2.test", "oca3.1.s3.test", "oca4.1.s4.test", "oca0.1.s0.test", "oca1.1.s1.test"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("rotation = %v", got)
	}
	if hits.Load() != 1 {
		t.Fatalf("API hits = %d, want 1", hits.Load())
	}
	if !strings.Contains(got[0], "oca0") {
		t.Fatal("first worker must get target 0")
	}
	client, servers := tg.info()
	if client.IP != "1.2.3.4" || len(servers) != 5 {
		t.Fatalf("info = %+v %d", client, len(servers))
	}

	// Two bad reports leave 3 usable: no refetch yet, and the bad hosts are skipped.
	tg.reportBad(tg.templates[0])
	tg.reportBad(tg.templates[0])
	if len(tg.templates) != 3 || hits.Load() != 1 {
		t.Fatalf("templates=%d hits=%d", len(tg.templates), hits.Load())
	}
	if _, servers := tg.info(); len(servers) != 3 || servers[0].Location.City != "2" {
		t.Fatalf("servers = %+v", servers)
	}
	// A third drops below 3: the next call refetches, filters the bad ones again, and returns index 0.
	tg.reportBad(tg.templates[0])
	u, err := tg.next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatalf("API hits = %d, want 2", hits.Load())
	}
	if hostOf(u) != "oca3.1.s3.test" || len(tg.templates) != 2 {
		t.Fatalf("after refetch got %s with %d templates", hostOf(u), len(tg.templates))
	}
}

// TestTargetsSameSiteBlacklist: with real hostnames the site bucket is shared, so
// three hosts failing once each mark everything bad and the refetch keeps all targets.
func TestTargetsSameSiteBlacklist(t *testing.T) {
	var hits atomic.Int32
	hc, srv := testClient(t, fakeAPI(t, 5, true, &hits))
	defer srv.Close()
	opts := DefaultOptions()
	tg := newTargets(&opts, hc, "tok")
	ctx := context.Background()
	if _, err := tg.next(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		tg.reportBad(tg.templates[0])
	}
	if len(tg.templates) != 0 {
		t.Fatalf("site blacklist should empty the list, got %d", len(tg.templates))
	}
	u, err := tg.next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 || len(tg.templates) != 5 || hostOf(u) != "oca0.1.oca.test" {
		t.Fatalf("hits=%d templates=%d got=%s", hits.Load(), len(tg.templates), hostOf(u))
	}
}

func TestTargetsAllBadFallback(t *testing.T) {
	var hits atomic.Int32
	hc, srv := testClient(t, fakeAPI(t, 3, true, &hits))
	defer srv.Close()
	opts := DefaultOptions()
	tg := newTargets(&opts, hc, "tok")
	ctx := context.Background()
	if _, err := tg.next(ctx); err != nil {
		t.Fatal(err)
	}
	for _, u := range append([]string(nil), tg.templates...) {
		tg.reportBad(u)
	}
	u, err := tg.next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tg.templates) != 3 || hostOf(u) != "oca0.1.oca.test" {
		t.Fatalf("all-bad fallback: %d templates, got %s", len(tg.templates), hostOf(u))
	}
}
