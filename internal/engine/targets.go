package engine

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/justinswe/fast-cli/internal/fastcom"
)

// targets ports url_getter (PROTOCOL.md §b, app.pretty.js L3045–3312): the
// range templates, their rotation, bad-URL bookkeeping and refetching.
type targets struct {
	opts  *Options
	hc    *http.Client
	token string

	mu        sync.Mutex
	client    fastcom.Client
	servers   []fastcom.Target
	templates []string // https://host/speedtest/range/?query
	idx       int
	fresh     bool // the next caller takes templates[0], as after a JS refresh
	rep       urlReporter
}

func newTargets(opts *Options, hc *http.Client, token string) *targets {
	return &targets{opts: opts, hc: hc, token: token, rep: newURLReporter(opts.BadOCAFailures, opts.BadSiteFailures)}
}

// next returns the template for a new worker: worker k gets template k mod n,
// refetching from the API first when fewer than RefetchBelow usable remain.
func (t *targets) next(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.templates) < t.opts.RefetchBelow {
		if err := t.refreshLocked(ctx); err != nil {
			return "", err
		}
	}
	if t.fresh {
		t.fresh = false
		return t.templates[0], nil
	}
	t.idx = (t.idx + 1) % len(t.templates)
	return t.templates[t.idx], nil
}

// refreshLocked ports parseSpeedTestUrls: bad targets are skipped unless that would leave none.
func (t *targets) refreshLocked(ctx context.Context) error {
	resp, err := fastcom.FetchTargets(ctx, t.hc, t.token, t.opts.URLCount)
	if err != nil {
		return err
	}
	var keep, all []string
	var servers []fastcom.Target
	for _, tg := range resp.Targets {
		tpl := strings.Replace(tg.URL, "speedtest", "speedtest/range/", 1)
		all = append(all, tpl)
		if !t.rep.isBad(tpl) {
			keep = append(keep, tpl)
			servers = append(servers, tg)
		}
	}
	if len(keep) == 0 {
		keep, servers = all, resp.Targets
	}
	t.client, t.servers, t.templates = resp.Client, servers, keep
	t.idx, t.fresh = 0, true
	return nil
}

// reportBad marks a template's URL bad and drops every template that is now bad.
func (t *targets) reportBad(tpl string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rep.report(tpl)
	var keep []string
	var servers []fastcom.Target
	for i, u := range t.templates {
		if !t.rep.isBad(u) {
			keep = append(keep, u)
			if i < len(t.servers) {
				servers = append(servers, t.servers[i])
			}
		}
	}
	t.templates, t.servers = keep, servers
}

// info returns the client and the targets kept from the latest API response.
func (t *targets) info() (fastcom.Client, []fastcom.Target) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.client, append([]fastcom.Target(nil), t.servers...)
}

// urlReporter ports the url_getter urlReporter (app.pretty.js L3112–3181).
type urlReporter struct {
	ocaMax, siteMax int
	bad             map[string]bool
	ocaFailures     map[string]int
	siteFailures    map[string]int
}

func newURLReporter(ocaMax, siteMax int) urlReporter {
	return urlReporter{ocaMax: ocaMax, siteMax: siteMax, bad: map[string]bool{}, ocaFailures: map[string]int{}, siteFailures: map[string]int{}}
}

func (r *urlReporter) isBad(u string) bool {
	norm, oca, site := parseTargetURL(u)
	return r.bad[norm] || r.ocaFailures[oca] >= r.ocaMax || r.siteFailures[site] >= r.siteMax
}

func (r *urlReporter) report(u string) {
	norm, oca, site := parseTargetURL(u)
	r.bad[norm] = true
	if oca != "" {
		r.ocaFailures[oca]++
		if r.ocaFailures[oca] == 1 && site != "" {
			r.siteFailures[site]++
		}
	}
}

// parseTargetURL ports parseUrl: the URL normalized to its /speedtest prefix plus
// query, the OCA host, and the "site" (third host label; "oca" for current hosts).
func parseTargetURL(u string) (normalized, oca, site string) {
	normalized = u
	parts := strings.Split(u, "?")
	endpoint := parts[0]
	if len(parts) == 2 {
		if i := strings.LastIndex(endpoint, "/speedtest"); i >= 0 {
			endpoint = endpoint[:i+len("/speedtest")]
			normalized = endpoint + "?" + parts[1]
		}
	}
	if i := strings.Index(endpoint, "://"); i >= 0 {
		endpoint = endpoint[i+3:]
	}
	oca = strings.Split(endpoint, "/")[0]
	if labels := strings.Split(oca, "."); len(labels) > 2 {
		site = labels[2]
	}
	return normalized, oca, site
}

// rangeURL rewrites a template for a ranged request of size bytes (§c).
func rangeURL(tpl string, size int64) string {
	return strings.Replace(tpl, "/range/", "/range/0-"+strconv.FormatInt(size, 10), 1)
}

// unrangedURL rewrites a template for the whole object (§c, attempt >= 3 with <= 2 workers).
func unrangedURL(tpl string) string {
	return strings.Replace(tpl, "/range/", "", 1)
}

// pingURL rewrites a template for a latency ping (§e).
func pingURL(tpl string) string {
	return strings.Replace(tpl, "/range/", "/range/0-0", 1)
}

// hostOf returns url.split("/")[2], the OCA host used to count completed requests.
func hostOf(tpl string) string {
	parts := strings.SplitN(tpl, "/", 4)
	if len(parts) < 3 {
		return tpl
	}
	return parts[2]
}
