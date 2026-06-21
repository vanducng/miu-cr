package serve

import (
	"bytes"
	stdctx "context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/go-github/v84/github"
)

// TestServeSmoke wires a fake reviewFn into a real Pool + Server, serves the mux
// over httptest, POSTs a real-HMAC pull_request "opened" payload, and asserts the
// fake reviewer received Ref=owner/repo#N within a short wait. /healthz→200. No
// network, no LLM.
func TestServeSmoke(t *testing.T) {
	var (
		mu   sync.Mutex
		refs []string
	)
	got := make(chan string, 1)
	reviewFn := func(j Job) error {
		mu.Lock()
		refs = append(refs, j.Ref)
		mu.Unlock()
		got <- j.Ref
		return nil
	}

	srv, pool, err := New(Config{
		Addr:         ":0",
		Secret:       []byte(testSecret),
		Repos:        []string{"octocat/hello"},
		ResolveToken: func() (string, error) { return testToken, nil },
	}, reviewFn)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer pool.Drain()

	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	body := prPayload("opened", "octocat", "hello", 42, false)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/webhook", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-GitHub-Delivery", "smoke-1")
	req.Header.Set("X-Hub-Signature-256", sign([]byte(testSecret), body))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /webhook: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/webhook status = %d, want 200", resp.StatusCode)
	}

	select {
	case ref := <-got:
		if ref != "octocat/hello#42" {
			t.Fatalf("reviewer got Ref = %q, want octocat/hello#42", ref)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reviewer never received the dispatched job")
	}

	mu.Lock()
	n := len(refs)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("reviewFn called %d times, want 1", n)
	}

	hresp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer hresp.Body.Close()
	if hresp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz status = %d, want 200", hresp.StatusCode)
	}
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(hresp.Body)
	if !bytes.Contains(buf.Bytes(), []byte(`"status":"ok"`)) {
		t.Errorf("/healthz body = %s", buf.String())
	}
}

// TestPollSmoke wires a fake reviewFn into a REAL Pool + Poller driven by
// RunPoll, with a serve-local fake notifGetter (no network, no LLM). It asserts
// the full poll trigger end-to-end: a new head SHA dispatches exactly one
// review, a second tick at the SAME head dispatches none, and ctx cancel drains
// the pool exactly once. This is the poll-mode analogue of TestServeSmoke.
func TestPollSmoke(t *testing.T) {
	var (
		mu      sync.Mutex
		reviews []string
	)
	reviewDone := make(chan string, 4)
	reviewFn := func(j Job) error {
		mu.Lock()
		reviews = append(reviews, j.Ref)
		mu.Unlock()
		reviewDone <- j.Ref
		return nil
	}

	pool := NewPool(reviewFn, discardLog())
	gh := &fakeNotifGetter{
		notifs: []*github.Notification{prNotif("octo", "hello", 1, time.Now())},
		getPR:  map[string]*github.PullRequest{"octo/hello#1": prWithHead(1, "sha-A")},
	}
	p := newTestPoller(t, sourceNotifications, []string{"octo/hello"}, gh, pool)

	ctx := stdctx.Background()
	if _, err := p.tick(ctx); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	select {
	case ref := <-reviewDone:
		if ref != "octo/hello#1" {
			t.Fatalf("reviewer got Ref = %q, want octo/hello#1", ref)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("poll never dispatched the new-head review")
	}

	// reviewFn runs on a worker goroutine; OnDone (recordSeen) fires in the
	// worker's deferred path AFTER reviewFn returns. Wait for the cursor to record
	// the reviewed head before tick 2 so we assert dedup, not a race.
	waitForSeen(t, p, "octo/hello#1", "sha-A")

	// Second tick at the SAME head SHA (newer notification updated_at so the
	// pre-GetPR guard does not short-circuit) → head-SHA dedup → no review.
	gh.notifs = []*github.Notification{prNotif("octo", "hello", 1, time.Now().Add(time.Minute))}
	if _, err := p.tick(ctx); err != nil {
		t.Fatalf("tick 2: %v", err)
	}

	// Drain via RunPoll on an already-cancelled ctx: the loop exits immediately
	// and the pool is drained exactly once (no double-drain, no goroutine leak).
	cctx, cancel := stdctx.WithCancel(stdctx.Background())
	cancel()
	if err := RunPoll(cctx, pool, p); err != nil {
		t.Fatalf("RunPoll: %v", err)
	}

	mu.Lock()
	n := len(reviews)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("reviewFn called %d times, want 1 (one review per distinct head SHA)", n)
	}
}

// waitForSeen blocks until the poller cursor records ref at sha (the worker's
// async OnDone), or fails after a short timeout.
func waitForSeen(t *testing.T, p *Poller, ref, sha string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		got := p.cursor.Seen[ref]
		p.mu.Unlock()
		if got == sha {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("cursor never recorded %s at %s (OnDone did not fire)", ref, sha)
}
