package fastcom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// APIURL is the fast.com targets endpoint, as configured in the fast.com web app.
const APIURL = "https://api.fast.com/netflix/speedtest/v2"

// DefaultURLCount is the urlCount the fast.com web app requests.
const DefaultURLCount = 5

// ErrNoTargets means the API answered 2xx but returned zero targets.
var ErrNoTargets = errors.New("fastcom: API returned no targets")

// FetchTargets fetches urlCount speed-test targets from the fast.com API.
//
// It GETs APIURL with exactly the query fast.com sends: https=true, token,
// urlCount. An empty token falls back to DefaultToken and a urlCount <= 0 to
// DefaultURLCount, mirroring the web app. The client object is decoded
// best-effort; targets with an empty url are dropped. Non-2xx responses return
// a *StatusError; a body with no usable targets returns ErrNoTargets. A nil hc
// uses http.DefaultClient.
func FetchTargets(ctx context.Context, hc *http.Client, token string, urlCount int) (*Response, error) {
	if token == "" {
		token = DefaultToken
	}
	if urlCount <= 0 {
		urlCount = DefaultURLCount
	}
	q := url.Values{}
	q.Set("https", "true")
	q.Set("token", token)
	q.Set("urlCount", strconv.Itoa(urlCount))
	reqURL := APIURL + "?" + q.Encode()

	body, err := get(ctx, hc, reqURL, maxBodyBytes)
	if err != nil {
		return nil, err
	}
	var resp Response
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("fastcom: decode targets response: %w", err)
	}
	kept := resp.Targets[:0]
	for _, t := range resp.Targets {
		if t.URL != "" {
			kept = append(kept, t)
		}
	}
	resp.Targets = kept
	if len(resp.Targets) == 0 {
		return nil, ErrNoTargets
	}
	return &resp, nil
}
