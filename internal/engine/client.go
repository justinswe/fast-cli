package engine

import (
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"time"
)

// newClient returns the client every request goes through, with the
// User-Agent applied by a RoundTripper so target refetches carry it too.
func newClient(opts *Options) *http.Client {
	var c http.Client
	if opts.HTTPClient != nil {
		c = *opts.HTTPClient
	} else {
		c.Transport = &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			DialContext:         (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:   true, // like a browser; live OCAs only accept http/1.1 via ALPN (probed 2026-09-17)
			MaxIdleConns:        4 * opts.MaxConnections,
			MaxIdleConnsPerHost: 2 * opts.MaxConnections, // data + ping stream per worker, all workers on one host worst case
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
		}
	}
	if opts.UserAgent != "" {
		base := c.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		c.Transport = uaTransport{base: base, ua: opts.UserAgent}
	}
	return &c
}

// uaTransport sets User-Agent on every request lacking one.
type uaTransport struct {
	base http.RoundTripper
	ua   string
}

func (t uaTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") == "" {
		req = req.Clone(req.Context())
		req.Header.Set("User-Agent", t.ua)
	}
	return t.base.RoundTrip(req)
}

// genBlob ports utils.genBlob (PROTOCOL.md §f, app.pretty.js L3442–3465): a
// string doubled with a random decimal appended until it exceeds size/2, then
// cut or padded to exactly size bytes of ASCII digits and dots.
func genBlob(size int64, random func() float64) []byte {
	var s []byte
	for int64(2*len(s)) <= size {
		s = append(s, s...)
		s = strconv.AppendFloat(s, random(), 'f', -1, 64)
	}
	if int64(len(s)) > size {
		return s[:size]
	}
	return append(s, s[:size-int64(len(s))]...)
}

// randomFloat is the default genBlob source, Math.random's counterpart.
func randomFloat() float64 { return rand.Float64() }
