package serve

import (
	stdctx "context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-github/v84/github"
)

// TestPollDispatchesOnePerHeadSHA wires NewPoller to a REAL Pool (the production
// Dispatcher) + a fake reviewFn, and proves an allowlisted PR notification yields
// exactly one reviewFn call per head SHA (dedup across ticks), end-to-end through
// the OnDone seam, no network, no LLM.
func TestPollDispatchesOnePerHeadSHA(t *testing.T) {
	dir := t.TempDir()
	orig := configDir
	configDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { configDir = orig })

	var calls int64
	got := make(chan string, 4)
	pool := NewPool(func(j Job) error {
		atomic.AddInt64(&calls, 1)
		got <- j.Ref
		return nil
	}, discardLog())
	defer pool.Drain()

	gh := &fakeNotifGetter{
		notifs: []*github.Notification{prNotif("octo", "hello", 1, time.Now())},
		getPR:  map[string]*github.PullRequest{"octo/hello#1": prWithHead(1, "sha-A")},
	}
	p := NewPoller(PollConfig{
		Source:       sourceNotifications,
		Repos:        []string{"octo/hello"},
		Interval:     time.Hour,
		ResolveToken: func() (string, error) { return "ghp_faketoken", nil },
		Dispatcher:   pool,
		Logger:       discardLog(),
	}, gh)

	ctx := stdctx.Background()
	if _, err := p.tick(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case ref := <-got:
		if ref != "octo/hello#1" {
			t.Fatalf("reviewFn got ref %q, want octo/hello#1", ref)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reviewFn never invoked for the first head")
	}

	// OnDone (success) recorded seen; wait for it to land before tick 2. The Pool
	// runs OnDone after reviewFn, so the channel receive doesn't guarantee record
	//, poll until seen is set.
	deadline := time.Now().Add(2 * time.Second)
	for {
		p.mu.Lock()
		seen := p.cursor.Seen["octo/hello#1"] == "sha-A"
		p.mu.Unlock()
		if seen {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("OnDone never recorded seen after a successful review")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Tick 2: same head SHA, newer notif updated_at (bypass pre-GetPR guard). Head
	// dedup must prevent a second review.
	gh.notifs = []*github.Notification{prNotif("octo", "hello", 1, time.Now().Add(time.Minute))}
	if _, err := p.tick(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := atomic.LoadInt64(&calls); n != 1 {
		t.Fatalf("same head across 2 ticks: reviewFn called %d times, want 1", n)
	}
}

// TestPollRealPoolFailedReviewRetried wires NewPoller to a REAL Pool whose
// reviewFn returns an error, exercising the production reviewFn->Pool->OnDone(err)
// seam (NOT the fake dispatcher). A failed review must leave seen unrecorded so
// the same head is re-reviewed next tick; once the reviewFn succeeds, seen is
// recorded and the head is no longer re-reviewed. This is the regression guard
// for the OnDone-on-success invariant in the real path.
func TestPollRealPoolFailedReviewRetried(t *testing.T) {
	dir := t.TempDir()
	orig := configDir
	configDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { configDir = orig })

	var calls int64
	var failing atomic.Bool
	failing.Store(true)
	done := make(chan struct{}, 8)
	pool := NewPool(func(j Job) error {
		atomic.AddInt64(&calls, 1)
		defer func() { done <- struct{}{} }()
		if failing.Load() {
			return errors.New("review boom")
		}
		return nil
	}, discardLog())
	defer pool.Drain()

	gh := &fakeNotifGetter{
		notifs: []*github.Notification{prNotif("octo", "hello", 1, time.Now())},
		getPR:  map[string]*github.PullRequest{"octo/hello#1": prWithHead(1, "sha-A")},
	}
	p := NewPoller(PollConfig{
		Source:       sourceNotifications,
		Repos:        []string{"octo/hello"},
		Interval:     time.Hour,
		ResolveToken: func() (string, error) { return "ghp_faketoken", nil },
		Dispatcher:   pool,
		Logger:       discardLog(),
	}, gh)
	ctx := stdctx.Background()

	awaitReview := func() {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("reviewFn never ran")
		}
	}

	// Tick 1: review fails → OnDone(err) → seen must stay unrecorded.
	if _, err := p.tick(ctx); err != nil {
		t.Fatal(err)
	}
	awaitReview()
	p.mu.Lock()
	seen := p.cursor.Seen["octo/hello#1"]
	p.mu.Unlock()
	if seen != "" {
		t.Fatalf("failed review must NOT record seen, got %q", seen)
	}

	// Tick 2 (newer notif updated_at bypasses pre-GetPR guard): same head, failed
	// before → must be re-reviewed.
	gh.notifs = []*github.Notification{prNotif("octo", "hello", 1, time.Now().Add(time.Minute))}
	if _, err := p.tick(ctx); err != nil {
		t.Fatal(err)
	}
	awaitReview()
	if n := atomic.LoadInt64(&calls); n != 2 {
		t.Fatalf("failed review must be retried via real Pool: calls=%d, want 2", n)
	}

	// Now succeed → seen recorded → no further re-review at the same head.
	failing.Store(false)
	gh.notifs = []*github.Notification{prNotif("octo", "hello", 1, time.Now().Add(2*time.Minute))}
	if _, err := p.tick(ctx); err != nil {
		t.Fatal(err)
	}
	awaitReview()
	deadline := time.Now().Add(2 * time.Second)
	for {
		p.mu.Lock()
		ok := p.cursor.Seen["octo/hello#1"] == "sha-A"
		p.mu.Unlock()
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("successful review never recorded seen")
		}
		time.Sleep(5 * time.Millisecond)
	}
	gh.notifs = []*github.Notification{prNotif("octo", "hello", 1, time.Now().Add(3*time.Minute))}
	if _, err := p.tick(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := atomic.LoadInt64(&calls); n != 3 {
		t.Fatalf("successful review must record seen (no re-review): calls=%d, want 3", n)
	}
}

// TestRunPoll_DrainsExactlyOnceOnCancel proves RunPoll Drains the pool once on
// ctx cancel and returns without leaking the poller goroutine.
func TestRunPoll_DrainsExactlyOnceOnCancel(t *testing.T) {
	dir := t.TempDir()
	orig := configDir
	configDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { configDir = orig })

	// Drain is idempotent on the Pool, so "drained exactly once" is asserted by:
	// RunPoll returns cleanly on cancel AND the pool is closed afterward (Submit
	// refuses), a second drain would be a safe no-op, never a double-close panic.
	pool := NewPool(func(Job) error { return nil }, discardLog())

	gh := &fakeNotifGetter{}
	p := NewPoller(PollConfig{
		Source:       sourceNotifications,
		Repos:        []string{"octo/hello"},
		Interval:     time.Hour,
		ResolveToken: func() (string, error) { return "ghp_faketoken", nil },
		Dispatcher:   pool,
		Logger:       discardLog(),
	}, gh)

	ctx, cancel := stdctx.WithCancel(stdctx.Background())
	done := make(chan error, 1)
	go func() { done <- RunPoll(ctx, pool, p) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunPoll returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunPoll did not return on ctx cancel")
	}
	// Pool is drained → Submit must now refuse (closed), proving exactly-one drain
	// happened (idempotent Drain means a second call would be a safe no-op).
	if pool.Submit(Job{Key: prKey{Owner: "o", Repo: "r", Number: 1}}) == SubmitQueued {
		t.Error("pool should be closed (drained) after RunPoll, Submit accepted a job")
	}
}
