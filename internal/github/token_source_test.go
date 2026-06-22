package github

import (
	stdctx "context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStaticTokenSource(t *testing.T) {
	ctx := stdctx.Background()
	for _, tok := range []string{"", "ghp_pat123"} {
		got, err := NewStaticTokenSource(tok).Token(ctx)
		if err != nil {
			t.Fatalf("Token(%q): %v", tok, err)
		}
		if got != tok {
			t.Fatalf("Token(%q) = %q, want %q", tok, got, tok)
		}
	}
}

// fakeExchanger records calls and returns a fixed token+expiry; an optional err
// is returned and the secret token is embedded so we can assert non-leakage.
type fakeExchanger struct {
	calls  atomic.Int64
	token  string
	expiry time.Time
	err    error
	block  chan struct{} // when non-nil, CreateInstallationToken waits on it
}

func (f *fakeExchanger) CreateInstallationToken(_ stdctx.Context, _ string, _ int64) (string, time.Time, error) {
	f.calls.Add(1)
	if f.block != nil {
		<-f.block
	}
	if f.err != nil {
		return "", time.Time{}, f.err
	}
	return f.token, f.expiry, nil
}

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func TestAppTokenSourceMintAndCache(t *testing.T) {
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	clock := now
	fx := &fakeExchanger{token: "ghs_install_tok", expiry: now.Add(time.Hour)}
	ts := NewAppTokenSource("12345", 99, testKey(t), fx, func() time.Time { return clock })

	got, err := ts.Token(stdctx.Background())
	if err != nil {
		t.Fatalf("first Token: %v", err)
	}
	if got != "ghs_install_tok" {
		t.Fatalf("token = %q", got)
	}
	if fx.calls.Load() != 1 {
		t.Fatalf("expected 1 exchange, got %d", fx.calls.Load())
	}

	// Second call well within validity → served from cache, no re-mint.
	clock = now.Add(10 * time.Minute)
	if _, err := ts.Token(stdctx.Background()); err != nil {
		t.Fatalf("cached Token: %v", err)
	}
	if fx.calls.Load() != 1 {
		t.Fatalf("expected cache hit (1 exchange), got %d", fx.calls.Load())
	}
}

func TestAppTokenSourceRefreshNearExpiry(t *testing.T) {
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	clock := now
	fx := &fakeExchanger{token: "tok_a", expiry: now.Add(time.Hour)}
	ts := NewAppTokenSource("app", 7, testKey(t), fx, func() time.Time { return clock })

	if _, err := ts.Token(stdctx.Background()); err != nil {
		t.Fatalf("mint: %v", err)
	}

	// Advance to within the 5-minute refresh margin → re-mint with a fresh token.
	fx.token = "tok_b"
	fx.expiry = now.Add(2 * time.Hour)
	clock = now.Add(time.Hour - 4*time.Minute)
	got, err := ts.Token(stdctx.Background())
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got != "tok_b" {
		t.Fatalf("token = %q, want refreshed tok_b", got)
	}
	if fx.calls.Load() != 2 {
		t.Fatalf("expected 2 exchanges (mint+refresh), got %d", fx.calls.Load())
	}
}

func TestAppTokenSourceSingleFlight(t *testing.T) {
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	fx := &fakeExchanger{token: "tok", expiry: now.Add(time.Hour), block: make(chan struct{})}
	ts := NewAppTokenSource("app", 1, testKey(t), fx, func() time.Time { return now })

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	errs := make([]error, n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			_, errs[i] = ts.Token(stdctx.Background())
		}(i)
	}
	// Let all goroutines reach the blocked exchange, then release.
	time.Sleep(50 * time.Millisecond)
	close(fx.block)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	if got := fx.calls.Load(); got != 1 {
		t.Fatalf("single-flight: expected 1 exchange for %d concurrent Token(), got %d", n, got)
	}
}

func TestAppTokenSourceErrorRedaction(t *testing.T) {
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	// The exchange fails with an error that does NOT contain the secret; assert
	// the surfaced error carries no token material.
	fx := &fakeExchanger{err: errors.New("401 Unauthorized")}
	ts := NewAppTokenSource("app", 1, testKey(t), fx, func() time.Time { return now })

	_, err := ts.Token(stdctx.Background())
	if err == nil {
		t.Fatal("expected error on failed exchange")
	}
	if strings.Contains(err.Error(), "ghs_") || strings.Contains(err.Error(), "PRIVATE KEY") {
		t.Fatalf("error leaks secret material: %q", err)
	}
}

func TestAppTokenSourceEmptyToken(t *testing.T) {
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	fx := &fakeExchanger{token: "", expiry: now.Add(time.Hour)}
	ts := NewAppTokenSource("app", 1, testKey(t), fx, func() time.Time { return now })

	if _, err := ts.Token(stdctx.Background()); err == nil {
		t.Fatal("expected error for empty installation token")
	}
}
