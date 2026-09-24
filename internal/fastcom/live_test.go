package fastcom

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestLive hits the real fast.com when its Bazel target is selected.
func TestLive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tok, err := FetchToken(ctx, nil)
	if err != nil {
		t.Fatalf("FetchToken: %v", err)
	}
	if tok == "" {
		t.Fatal("empty token")
	}
	t.Logf("token=%s (matches DefaultToken: %v)", tok, tok == DefaultToken)

	resp, err := FetchTargets(ctx, nil, tok, 5)
	if err != nil {
		t.Fatalf("FetchTargets: %v", err)
	}
	if len(resp.Targets) < 1 {
		t.Fatal("no targets")
	}
	for _, tg := range resp.Targets {
		if !strings.Contains(tg.URL, "/speedtest") {
			t.Errorf("target URL %q lacks /speedtest", tg.URL)
		}
	}
	t.Logf("client=%+v targets=%d first=%s", resp.Client, len(resp.Targets), resp.Targets[0].URL)
}
