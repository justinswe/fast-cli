package fastcom

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// bareTransport mimics a RoundTripper that fabricates responses without setting Response.Request.
type bareTransport struct {
	status int
	body   string
}

func (t bareTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: t.status, Body: io.NopCloser(strings.NewReader(t.body))}, nil
}

// rewriteTransport routes every request to the test server, keeping path and query.
type rewriteTransport struct{ target *url.URL }

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.URL.Scheme = t.target.Scheme
	r.URL.Host = t.target.Host
	return http.DefaultTransport.RoundTrip(r)
}

// newTestClient serves handler for all fast.com/api.fast.com requests.
func newTestClient(t *testing.T, handler http.Handler) *http.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: rewriteTransport{target: u}}
}

const (
	testHTML   = `<!doctype html><html><head><script src="/app-abc123.js"></script></head></html>`
	testBundle = `!function(){var DEFAULT_PARAMS={https:!0,token:"dGVzdHRva2Vu-_=",urlCount:3}}();`
)

func tokenHandler(t *testing.T, html, bundle string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(html))
	})
	mux.HandleFunc("/app-abc123.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = w.Write([]byte(bundle))
	})
	return mux
}

func TestFetchTokenSuccess(t *testing.T) {
	hc := newTestClient(t, tokenHandler(t, testHTML, testBundle))
	tok, err := FetchToken(context.Background(), hc)
	if err != nil {
		t.Fatalf("FetchToken: %v", err)
	}
	if want := "dGVzdHRva2Vu-_="; tok != want {
		t.Fatalf("token = %q, want %q", tok, want)
	}
}

func TestFetchTokenScriptMiss(t *testing.T) {
	hc := newTestClient(t, tokenHandler(t, `<html><script src="/vendor.js"></script></html>`, testBundle))
	tok, err := FetchToken(context.Background(), hc)
	if !errors.Is(err, ErrAppScriptNotFound) {
		t.Fatalf("err = %v, want ErrAppScriptNotFound", err)
	}
	if tok != "" {
		t.Fatalf("token = %q, want empty", tok)
	}
}

func TestFetchTokenTokenMiss(t *testing.T) {
	hc := newTestClient(t, tokenHandler(t, testHTML, `var x = {urlCount: 3};`))
	tok, err := FetchToken(context.Background(), hc)
	if !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("err = %v, want ErrTokenNotFound", err)
	}
	if tok != "" {
		t.Fatalf("token = %q, want empty", tok)
	}
}

func TestFetchTokenPageNon200(t *testing.T) {
	hc := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone fishing", http.StatusServiceUnavailable)
	}))
	_, err := FetchToken(context.Background(), hc)
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want *StatusError", err)
	}
	if se.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("StatusCode = %d, want 503", se.StatusCode)
	}
	if !strings.Contains(se.Error(), "HTTP 503") || !strings.Contains(se.Error(), "gone fishing") {
		t.Fatalf("Error() = %q", se.Error())
	}
}

func TestFetchTokenBundleNon200(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(testHTML)) })
	mux.HandleFunc("/app-abc123.js", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	hc := newTestClient(t, mux)
	_, err := FetchToken(context.Background(), hc)
	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusNotFound {
		t.Fatalf("err = %v, want *StatusError 404", err)
	}
}

func TestFetchTokenNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	u, _ := url.Parse(srv.URL)
	srv.Close()
	hc := &http.Client{Transport: rewriteTransport{target: u}}
	tok, err := FetchToken(context.Background(), hc)
	if err == nil {
		t.Fatal("expected network error")
	}
	if tok != "" {
		t.Fatalf("token = %q, want empty", tok)
	}
}

func TestFetchTokenContextCancelled(t *testing.T) {
	hc := newTestClient(t, tokenHandler(t, testHTML, testBundle))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := FetchToken(ctx, hc); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

const sampleTargets = `{"client":{"ip":"34.19.110.48","asn":"396982","location":{"city":"The Dalles","country":"US"}},
"targets":[
{"name":"https://ipv4-c002-iev003-1-ix-kiev-isp.1.oca.nflxvideo.net/speedtest?c=us&n=396982&v=210&e=1789680514&t=qUCb","url":"https://ipv4-c002-iev003-1-ix-kiev-isp.1.oca.nflxvideo.net/speedtest?c=us&n=396982&v=210&e=1789680514&t=qUCb","location":{"city":"Kiev","country":"UA"}},
{"name":"https://ipv4-c204-sea001-ix.1.oca.nflxvideo.net/speedtest?c=us&n=396982&v=319&e=1789680514&t=xpy_","url":"https://ipv4-c204-sea001-ix.1.oca.nflxvideo.net/speedtest?c=us&n=396982&v=319&e=1789680514&t=xpy_","location":{"city":"Seattle","country":"US"}}
]}`

func TestFetchTargetsSuccess(t *testing.T) {
	var gotPath, gotQuery string
	hc := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleTargets))
	}))
	resp, err := FetchTargets(context.Background(), hc, "tok123", 5)
	if err != nil {
		t.Fatalf("FetchTargets: %v", err)
	}
	if gotPath != "/netflix/speedtest/v2" {
		t.Errorf("path = %q", gotPath)
	}
	if want := "https=true&token=tok123&urlCount=5"; gotQuery != want {
		t.Errorf("query = %q, want %q", gotQuery, want)
	}
	if resp.Client.IP != "34.19.110.48" || resp.Client.ASN != "396982" || resp.Client.ISP != "" {
		t.Errorf("client = %+v", resp.Client)
	}
	if resp.Client.Location != (Location{City: "The Dalles", Country: "US"}) {
		t.Errorf("client location = %+v", resp.Client.Location)
	}
	if len(resp.Targets) != 2 {
		t.Fatalf("targets = %d, want 2", len(resp.Targets))
	}
	tg := resp.Targets[1]
	if !strings.HasPrefix(tg.URL, "https://ipv4-c204-sea001-ix.1.oca.nflxvideo.net/speedtest?") || tg.Name != tg.URL {
		t.Errorf("target = %+v", tg)
	}
	if tg.Location != (Location{City: "Seattle", Country: "US"}) {
		t.Errorf("target location = %+v", tg.Location)
	}
}

func TestFetchTargetsQueryEscapesToken(t *testing.T) {
	var gotQuery string
	hc := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(sampleTargets))
	}))
	if _, err := FetchTargets(context.Background(), hc, "a b&c", 3); err != nil {
		t.Fatal(err)
	}
	if want := "https=true&token=a+b%26c&urlCount=3"; gotQuery != want {
		t.Errorf("query = %q, want %q", gotQuery, want)
	}
}

func TestFetchTargetsDefaultURLCount(t *testing.T) {
	var gotQuery string
	hc := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(sampleTargets))
	}))
	if _, err := FetchTargets(context.Background(), hc, DefaultToken, 0); err != nil {
		t.Fatal(err)
	}
	if want := "https=true&token=" + DefaultToken + "&urlCount=5"; gotQuery != want {
		t.Errorf("query = %q, want %q", gotQuery, want)
	}
}

func TestFetchTargetsNon200(t *testing.T) {
	hc := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Unknown app token: bad"}`))
	}))
	resp, err := FetchTargets(context.Background(), hc, "bad", 5)
	if resp != nil {
		t.Fatalf("resp = %+v, want nil", resp)
	}
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want *StatusError", err)
	}
	if se.StatusCode != http.StatusForbidden {
		t.Errorf("StatusCode = %d, want 403", se.StatusCode)
	}
	if !strings.Contains(se.Message, "Unknown app token") {
		t.Errorf("Message = %q", se.Message)
	}
	if !strings.Contains(se.URL, "/netflix/speedtest/v2?https=true&token=bad&urlCount=5") {
		t.Errorf("URL = %q", se.URL)
	}
}

func TestFetchTargetsEmpty(t *testing.T) {
	for _, body := range []string{`{"client":{"ip":"1.2.3.4"},"targets":[]}`, `{"client":{"ip":"1.2.3.4"}}`} {
		hc := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		resp, err := FetchTargets(context.Background(), hc, DefaultToken, 5)
		if !errors.Is(err, ErrNoTargets) {
			t.Errorf("body %s: err = %v, want ErrNoTargets", body, err)
		}
		if resp != nil {
			t.Errorf("body %s: resp = %+v, want nil", body, resp)
		}
	}
}

func TestFetchTargetsBadJSON(t *testing.T) {
	hc := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html>not json</html>`))
	}))
	if _, err := FetchTargets(context.Background(), hc, DefaultToken, 5); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestFetchTargetsASNShapes(t *testing.T) {
	const targets = `"targets":[{"name":"n","url":"https://x/speedtest?c=us","location":{"city":"c","country":"cc"}}]`
	cases := map[string]string{
		`"asn":"396982",`:   "396982",
		`"asn":396982,`:     "396982",
		`"asn":"AS396982",`: "AS396982",
		`"asn":null,`:       "",
		``:                  "",
	}
	for asn, want := range cases {
		body := `{"client":{` + asn + `"ip":"1.2.3.4"},` + targets + `}`
		hc := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		resp, err := FetchTargets(context.Background(), hc, DefaultToken, 1)
		if err != nil {
			t.Errorf("%s: %v", asn, err)
			continue
		}
		if resp.Client.ASN != want {
			t.Errorf("%s: ASN = %q, want %q", asn, resp.Client.ASN, want)
		}
		if resp.Client.IP != "1.2.3.4" {
			t.Errorf("%s: IP = %q, other fields must survive custom unmarshal", asn, resp.Client.IP)
		}
	}
}

func TestFetchTargetsNilClientUsesDefault(t *testing.T) {
	// The nil path must reach http.DefaultClient without panicking and surface the ctx error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := FetchTargets(ctx, nil, DefaultToken, 5); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestStatusErrorWithoutResponseRequest(t *testing.T) {
	hc := &http.Client{Transport: bareTransport{status: http.StatusInternalServerError, body: "  boom\n"}}
	_, err := FetchTargets(context.Background(), hc, DefaultToken, 5)
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want *StatusError", err)
	}
	if se.StatusCode != http.StatusInternalServerError || se.Message != "boom" {
		t.Errorf("StatusError = %+v", se)
	}
	if want := APIURL + "?https=true&token=" + DefaultToken + "&urlCount=5"; se.URL != want {
		t.Errorf("URL = %q, want %q", se.URL, want)
	}
	if _, err := FetchToken(context.Background(), hc); !errors.As(err, &se) || se.URL != fastComURL {
		t.Errorf("FetchToken err = %v, want *StatusError for %s", err, fastComURL)
	}
}

func TestFetchTargetsEmptyTokenUsesDefault(t *testing.T) {
	var gotQuery string
	hc := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(sampleTargets))
	}))
	if _, err := FetchTargets(context.Background(), hc, "", 5); err != nil {
		t.Fatal(err)
	}
	if want := "https=true&token=" + DefaultToken + "&urlCount=5"; gotQuery != want {
		t.Errorf("query = %q, want %q", gotQuery, want)
	}
}

func TestFetchTargetsTolerantClientDecode(t *testing.T) {
	const targets = `"targets":[{"name":"n","url":"https://x/speedtest?c=us","location":{"city":"c","country":"cc"}}]`
	cases := []struct {
		client string
		want   Client
	}{
		{`{"ip":123,"asn":"396982"}`, Client{IP: "123", ASN: "396982"}},
		{`{"ip":"1.2.3.4","location":"here"}`, Client{IP: "1.2.3.4"}},
		{`{"ip":"1.2.3.4","location":{"city":123,"country":"US"}}`, Client{IP: "1.2.3.4", Location: Location{Country: "US"}}},
		{`{"ip":{"v4":"1.2.3.4"},"isp":["x"]}`, Client{}},
		{`"not an object"`, Client{}},
		{`null`, Client{}},
	}
	for _, tc := range cases {
		body := `{"client":` + tc.client + `,` + targets + `}`
		hc := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		resp, err := FetchTargets(context.Background(), hc, DefaultToken, 1)
		if err != nil {
			t.Errorf("%s: %v", tc.client, err)
			continue
		}
		if resp.Client != tc.want {
			t.Errorf("%s: client = %+v, want %+v", tc.client, resp.Client, tc.want)
		}
		if len(resp.Targets) != 1 {
			t.Errorf("%s: targets = %d, want 1", tc.client, len(resp.Targets))
		}
	}
}

func TestFetchTargetsDropsBlankURLs(t *testing.T) {
	body := `{"client":{},"targets":[{"name":"a","url":""},{"name":"b","url":"https://x/speedtest?c=us"},{"name":"c"}]}`
	hc := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	resp, err := FetchTargets(context.Background(), hc, DefaultToken, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Targets) != 1 || resp.Targets[0].Name != "b" {
		t.Errorf("targets = %+v, want only b", resp.Targets)
	}

	hc = newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"client":{},"targets":[{"name":"a","url":""}]}`))
	}))
	if _, err := FetchTargets(context.Background(), hc, DefaultToken, 3); !errors.Is(err, ErrNoTargets) {
		t.Errorf("err = %v, want ErrNoTargets", err)
	}
}
