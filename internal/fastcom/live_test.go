package fastcom

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLive hits the real fast.com; run with FASTCLI_LIVE=1.
func TestLive(t *testing.T) {
	if os.Getenv("FASTCLI_LIVE") != "1" {
		t.Skip("set FASTCLI_LIVE=1 to run against fast.com")
	}
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
