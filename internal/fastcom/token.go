package fastcom

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/justinswe/std/errors"
)

// fastComURL is the page whose app-<hash>.js bundle embeds the API token.
const fastComURL = "https://fast.com/"

// maxBodyBytes bounds how much of the HTML/JS bundle is read while scraping.
const maxBodyBytes = 2 << 20

var (
	// ErrAppScriptNotFound means the fast.com page had no app-<hash>.js reference.
	ErrAppScriptNotFound = errors.New("fastcom: app-<hash>.js reference not found in fast.com page")
	// ErrTokenNotFound means the app bundle had no token:"..." literal.
	ErrTokenNotFound = errors.New("fastcom: token not found in fast.com app bundle")

	appScriptRe = regexp.MustCompile(`app-[a-z0-9]+\.js`)
	tokenRe     = regexp.MustCompile(`token:"([A-Za-z0-9+/=_-]+)"`)
)

// StatusError is returned when fast.com or its API answers with a non-2xx status.
type StatusError struct {
	URL        string
	StatusCode int
	Message    string // trimmed response body excerpt, if any
}

func (e *StatusError) Error() string {
	s := fmt.Sprintf("fastcom: GET %s: HTTP %d", e.URL, e.StatusCode)
	if e.Message != "" {
		s += fmt.Sprintf(": %q", e.Message)
	}
	return s
}

// FetchToken discovers the current API token from the fast.com web app.
//
// It GETs https://fast.com/, locates the app-<hash>.js bundle, GETs it, and
// extracts the token:"..." literal. Any failure (network, non-2xx, regex miss)
// returns ("", err); callers should fall back to DefaultToken in that case.
// A nil hc uses http.DefaultClient.
func FetchToken(ctx context.Context, hc *http.Client) (string, error) {
	page, err := get(ctx, hc, fastComURL, maxBodyBytes)
	if err != nil {
		return "", err
	}
	script := appScriptRe.Find(page)
	if script == nil {
		return "", ErrAppScriptNotFound
	}
	bundle, err := get(ctx, hc, fastComURL+string(script), maxBodyBytes)
	if err != nil {
		return "", err
	}
	m := tokenRe.FindSubmatch(bundle)
	if m == nil {
		return "", ErrTokenNotFound
	}
	return string(m[1]), nil
}

// get performs a context-bound GET and returns at most limit bytes of a 2xx body.
func get(ctx context.Context, hc *http.Client, url string, limit int64) ([]byte, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, errors.Errorf("fastcom: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, newStatusError(resp, url)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, errors.Errorf("fastcom: read %s: %w", url, err)
	}
	return body, nil
}

// newStatusError builds a StatusError carrying a short excerpt of the body.
// url is passed explicitly because custom RoundTrippers may leave resp.Request nil.
func newStatusError(resp *http.Response, url string) *StatusError {
	const excerpt = 512
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, excerpt))
	return &StatusError{
		URL:        url,
		StatusCode: resp.StatusCode,
		Message:    strings.TrimSpace(string(msg)),
	}
}
