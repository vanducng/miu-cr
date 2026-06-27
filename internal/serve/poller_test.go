package serve

import (
	stdctx "context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/go-github/v84/github"
)

// fakeNotifGetter is a serve-local fake of the narrow notifGetter. It records
// call counts and returns scripted responses; no network is touched.
type fakeNotifGetter struct {
	mu        sync.Mutex
	notifs    []*github.Notification
	notifResp *github.Response
	notifErr  error
	prs       map[string][]*github.PullRequest // owner/repo -> open PRs
	prPages   map[string][][]*github.PullRequest
	listErr   map[string]error               // owner/repo -> ListOpenPRs error
	getPR     map[string]*github.PullRequest // owner/repo#n -> PR
	getPRErr  error

	notifCalls int
	getPRCalls int
	listCalls  int
}

func (f *fakeNotifGetter) ListNotifications(_ stdctx.Context, _ *github.NotificationListOptions) ([]*github.Notification, *github.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notifCalls++
	return f.notifs, f.notifResp, f.notifErr
}

func (f *fakeNotifGetter) ListOpenPRs(_ stdctx.Context, owner, repo string, opts *github.PullRequestListOptions) ([]*github.PullRequest, *github.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	if pages := f.prPages[owner+"/"+repo]; len(pages) > 0 {
		page := 0
		if opts != nil {
			page = opts.Page
		}
		if page == 0 {
			page = 1
		}
		if page > len(pages) {
			return nil, &github.Response{}, nil
		}
		resp := &github.Response{}
		if page < len(pages) {
			resp.NextPage = page + 1
		}
		return pages[page-1], resp, f.listErr[owner+"/"+repo]
	}
	return f.prs[owner+"/"+repo], f.notifResp, f.listErr[owner+"/"+repo]
}

func (f *fakeNotifGetter) GetPR(_ stdctx.Context, owner, repo string, number int) (*github.PullRequest, *github.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getPRCalls++
	if f.getPRErr != nil {
		return nil, nil, f.getPRErr
	}
	return f.getPR[key(owner, repo, number).String()], nil, nil
}

// pollDispatcher records submitted jobs and synchronously invokes OnDone with
// the configured result, mimicking the Pool's success/failure seam.
type pollDispatcher struct {
	mu      sync.Mutex
	jobs    []Job
	failErr error // when non-nil, OnDone is called with this (review failed)
	accept  bool
}

func newPollDispatcher() *pollDispatcher { return &pollDispatcher{accept: true} }

func (d *pollDispatcher) Submit(j Job) SubmitResult {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.accept {
		return SubmitFull
	}
	d.jobs = append(d.jobs, j)
	if j.OnDone != nil {
		j.OnDone(d.failErr)
	}
	return SubmitQueued
}

func (d *pollDispatcher) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.jobs)
}

func prNotif(owner, repo string, number int, updated time.Time) *github.Notification {
	url := "https://api.github.com/repos/" + owner + "/" + repo + "/pulls/" + itoa(number)
	return &github.Notification{
		Repository: &github.Repository{
			Name:  github.Ptr(repo),
			Owner: &github.User{Login: github.Ptr(owner)},
		},
		Subject:   &github.NotificationSubject{Type: github.Ptr("PullRequest"), URL: github.Ptr(url)},
		UpdatedAt: &github.Timestamp{Time: updated},
	}
}

func itoa(n int) string { return string(rune('0' + n%10)) } // single-digit test numbers only

func prWithHead(number int, sha string) *github.PullRequest {
	return &github.PullRequest{
		Number: github.Ptr(number),
		Head:   &github.PullRequestBranch{SHA: github.Ptr(sha)},
	}
}

// newTestPoller builds a Poller with a fake gh + disp and a tmp config dir so the
// cursor never touches the real home.
func newTestPoller(t *testing.T, src pollSource, repos []string, gh notifGetter, disp Dispatcher) *Poller {
	t.Helper()
	dir := t.TempDir()
	orig := configDir
	configDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { configDir = orig })
	return &Poller{
		src:          src,
		gh:           gh,
		disp:         disp,
		allow:        newRepoAllowlist(repos),
		interval:     time.Hour,
		resolveToken: func() (string, error) { return "ghp_faketoken", nil },
		log:          discardLog(),
		cursor:       newCursor(),
	}
}

func TestPoller_DedupSameHeadAcrossTicks(t *testing.T) {
	gh := &fakeNotifGetter{
		notifs: []*github.Notification{prNotif("octo", "hello", 1, time.Now())},
		getPR:  map[string]*github.PullRequest{"octo/hello#1": prWithHead(1, "sha-A")},
	}
	disp := newPollDispatcher()
	p := newTestPoller(t, sourceNotifications, []string{"octo/hello"}, gh, disp)

	ctx := stdctx.Background()
	if _, err := p.tick(ctx); err != nil {
		t.Fatal(err)
	}
	// second tick: same head SHA but a newer notification updated_at (so the
	// pre-GetPR dedup does not short-circuit), head-SHA dedup must.
	gh.notifs = []*github.Notification{prNotif("octo", "hello", 1, time.Now().Add(time.Minute))}
	if _, err := p.tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := disp.count(); got != 1 {
		t.Errorf("same head across 2 ticks: got %d Submits, want 1", got)
	}
}

func TestPoller_NewHeadReReviews(t *testing.T) {
	gh := &fakeNotifGetter{
		notifs: []*github.Notification{prNotif("octo", "hello", 1, time.Now())},
		getPR:  map[string]*github.PullRequest{"octo/hello#1": prWithHead(1, "sha-A")},
	}
	disp := newPollDispatcher()
	p := newTestPoller(t, sourceNotifications, []string{"octo/hello"}, gh, disp)
	ctx := stdctx.Background()

	if _, err := p.tick(ctx); err != nil {
		t.Fatal(err)
	}
	gh.notifs = []*github.Notification{prNotif("octo", "hello", 1, time.Now().Add(time.Minute))}
	gh.getPR["octo/hello#1"] = prWithHead(1, "sha-B")
	if _, err := p.tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := disp.count(); got != 2 {
		t.Errorf("new head: got %d Submits, want 2", got)
	}
}

func TestPoller_OffAllowlistAndNonPRNeverDispatched(t *testing.T) {
	nonPR := &github.Notification{
		Repository: &github.Repository{Name: github.Ptr("hello"), Owner: &github.User{Login: github.Ptr("octo")}},
		Subject:    &github.NotificationSubject{Type: github.Ptr("Issue"), URL: github.Ptr("https://api.github.com/repos/octo/hello/issues/9")},
		UpdatedAt:  &github.Timestamp{Time: time.Now()},
	}
	gh := &fakeNotifGetter{
		notifs: []*github.Notification{
			prNotif("evil", "repo", 1, time.Now()), // off allowlist
			nonPR,
		},
		getPR: map[string]*github.PullRequest{},
	}
	disp := newPollDispatcher()
	p := newTestPoller(t, sourceNotifications, []string{"octo/hello"}, gh, disp)
	if _, err := p.tick(stdctx.Background()); err != nil {
		t.Fatal(err)
	}
	if disp.count() != 0 {
		t.Errorf("off-allowlist + non-PR should never dispatch, got %d", disp.count())
	}
	if gh.getPRCalls != 0 {
		t.Errorf("no GetPR for filtered candidates, got %d", gh.getPRCalls)
	}
}

func TestPoller_PreGetPRDedupOnUnchangedUpdatedAt(t *testing.T) {
	ts := time.Now()
	gh := &fakeNotifGetter{
		notifs: []*github.Notification{prNotif("octo", "hello", 1, ts)},
		getPR:  map[string]*github.PullRequest{"octo/hello#1": prWithHead(1, "sha-A")},
	}
	disp := newPollDispatcher()
	p := newTestPoller(t, sourceNotifications, []string{"octo/hello"}, gh, disp)
	ctx := stdctx.Background()

	if _, err := p.tick(ctx); err != nil {
		t.Fatal(err)
	}
	first := gh.getPRCalls
	// same updated_at on tick 2 → pre-GetPR dedup, no new GetPR.
	if _, err := p.tick(ctx); err != nil {
		t.Fatal(err)
	}
	if gh.getPRCalls != first {
		t.Errorf("unchanged updated_at should skip GetPR: calls %d -> %d", first, gh.getPRCalls)
	}
}

func TestPoller_OnDoneRecordsSeenOnlyOnSuccess(t *testing.T) {
	// A FIXED updated_at throughout: proves a failed review is retried even when
	// the notification's updated_at hasn't changed (NotifSeen is success-gated, so
	// a failed review records neither Seen nor NotifSeen → the pre-GetPR guard
	// can't suppress the retry).
	t0 := time.Now()
	gh := &fakeNotifGetter{
		notifs: []*github.Notification{prNotif("octo", "hello", 1, t0)},
		getPR:  map[string]*github.PullRequest{"octo/hello#1": prWithHead(1, "sha-A")},
	}
	disp := newPollDispatcher()
	disp.failErr = errors.New("review boom") // review fails → nothing recorded
	p := newTestPoller(t, sourceNotifications, []string{"octo/hello"}, gh, disp)
	ctx := stdctx.Background()

	if _, err := p.tick(ctx); err != nil { // tick 1: fails
		t.Fatal(err)
	}
	if _, err := p.tick(ctx); err != nil { // tick 2: SAME updated_at → must retry
		t.Fatal(err)
	}
	if disp.count() != 2 {
		t.Errorf("failed review must retry with unchanged updated_at: got %d Submits, want 2", disp.count())
	}

	// Succeed → records Seen + NotifSeen.
	disp.failErr = nil
	if _, err := p.tick(ctx); err != nil { // tick 3: succeeds → Submit==3
		t.Fatal(err)
	}
	if _, err := p.tick(ctx); err != nil { // tick 4: SAME updated_at → deduped (pre-GetPR)
		t.Fatal(err)
	}
	if disp.count() != 3 {
		t.Errorf("successful review must dedupe at unchanged updated_at: got %d Submits, want 3", disp.count())
	}
}

func TestPoller_PullsSourceUsesHeadFromList(t *testing.T) {
	gh := &fakeNotifGetter{
		prs: map[string][]*github.PullRequest{
			"octo/hello": {prWithHead(1, "sha-A"), prWithHead(2, "sha-B")},
		},
	}
	disp := newPollDispatcher()
	p := newTestPoller(t, sourcePulls, []string{"octo/hello"}, gh, disp)
	if _, err := p.tick(stdctx.Background()); err != nil {
		t.Fatal(err)
	}
	if disp.count() != 2 {
		t.Errorf("pulls source: got %d Submits, want 2", disp.count())
	}
	if gh.getPRCalls != 0 {
		t.Errorf("pulls source must not call GetPR (head in list), got %d", gh.getPRCalls)
	}
	if gh.listCalls != 1 {
		t.Errorf("pulls source should ListOpenPRs once per repo, got %d", gh.listCalls)
	}
}

// TestPoller_PullsSourceContinuesPastRepoError proves one repo's ListOpenPRs
// failure is logged + skipped, not allowed to abort the whole tick: the healthy
// repo's PRs are still enumerated. Sorted iteration means "octo/bad" is tried
// (and fails) before "octo/good".
func TestPoller_PullsSourceContinuesPastRepoError(t *testing.T) {
	gh := &fakeNotifGetter{
		prs:     map[string][]*github.PullRequest{"octo/good": {prWithHead(1, "sha-A")}},
		listErr: map[string]error{"octo/bad": errors.New("boom")},
	}
	disp := newPollDispatcher()
	p := newTestPoller(t, sourcePulls, []string{"octo/bad", "octo/good"}, gh, disp)

	cands, _, err := p.enumeratePulls(stdctx.Background())
	if err != nil {
		t.Fatalf("a single repo error must not fail the tick: %v", err)
	}
	if len(cands) != 1 || cands[0].repo != "good" || cands[0].number != 1 {
		t.Fatalf("healthy repo PRs must still be enumerated, got %+v", cands)
	}
}

// TestPoller_RunStopsOnCancelDuringRateLimitSleep proves a ctx cancel during the
// rate-limit backoff sleep (inside handleErr) actually stops Run, rather than
// looping into one extra tick after shutdown.
func TestPoller_RunStopsOnCancelDuringRateLimitSleep(t *testing.T) {
	reset := time.Now().Add(time.Hour) // long sleep; cancel must cut it short
	rlErr := &github.RateLimitError{Rate: github.Rate{Reset: github.Timestamp{Time: reset}}}
	gh := &fakeNotifGetter{notifErr: rlErr}
	p := newTestPoller(t, sourceNotifications, []string{"octo/hello"}, gh, newPollDispatcher())
	p.interval = time.Hour

	ctx, cancel := stdctx.WithCancel(stdctx.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()

	time.Sleep(20 * time.Millisecond) // let Run enter the rate-limit sleep
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop when ctx cancelled during rate-limit sleep")
	}
}

func TestPoller_EffectiveIntervalFromHeader(t *testing.T) {
	hdr := http.Header{}
	hdr.Set("X-Poll-Interval", "120")
	gh := &fakeNotifGetter{
		notifs:    nil,
		notifResp: &github.Response{Response: &http.Response{Header: hdr}},
	}
	disp := newPollDispatcher()
	p := newTestPoller(t, sourceNotifications, []string{"octo/hello"}, gh, disp)
	p.interval = 30 * time.Second // configured floor below header

	wait, err := p.tick(stdctx.Background())
	if err != nil {
		t.Fatal(err)
	}
	if wait != 120*time.Second {
		t.Errorf("X-Poll-Interval = %v, want 120s", wait)
	}
}

func TestPoller_TokenErrorSkipsTick(t *testing.T) {
	gh := &fakeNotifGetter{notifs: []*github.Notification{prNotif("octo", "hello", 1, time.Now())}}
	disp := newPollDispatcher()
	p := newTestPoller(t, sourceNotifications, []string{"octo/hello"}, gh, disp)
	p.resolveToken = func() (string, error) { return "", errors.New("no token") }

	if _, err := p.tick(stdctx.Background()); err != nil {
		t.Fatalf("token error should be swallowed (skip tick), got %v", err)
	}
	if gh.notifCalls != 0 || disp.count() != 0 {
		t.Errorf("token error must skip enumerate+dispatch: notifCalls=%d submits=%d", gh.notifCalls, disp.count())
	}
}

func TestPoller_TransientErrorBackoffGrowsNoAdvance(t *testing.T) {
	gh := &fakeNotifGetter{notifErr: errors.New("connection reset")}
	disp := newPollDispatcher()
	p := newTestPoller(t, sourceNotifications, []string{"octo/hello"}, gh, disp)
	before := p.cursor.Since

	_, err := p.tick(stdctx.Background())
	if err == nil {
		t.Fatal("transient error should propagate from tick")
	}
	if !p.cursor.Since.Equal(before) {
		t.Error("cursor Since must NOT advance on error")
	}

	b1 := p.handleErr(stdctx.Background(), err, 0)
	b2 := p.handleErr(stdctx.Background(), err, b1)
	if b1 <= 0 || b2 <= b1 {
		t.Errorf("backoff should grow: b1=%v b2=%v", b1, b2)
	}
	if disp.count() != 0 {
		t.Errorf("no dispatch on error, got %d", disp.count())
	}
}

func TestPoller_RateLimitWaitsUntilReset(t *testing.T) {
	reset := time.Now().Add(40 * time.Millisecond)
	rlErr := &github.RateLimitError{Rate: github.Rate{Reset: github.Timestamp{Time: reset}}}
	p := newTestPoller(t, sourceNotifications, []string{"octo/hello"}, &fakeNotifGetter{}, newPollDispatcher())

	start := time.Now()
	got := p.handleErr(stdctx.Background(), rlErr, 0)
	if got != 0 {
		t.Errorf("rate-limit handleErr should return 0 (slept), got %v", got)
	}
	if time.Since(start) < 30*time.Millisecond {
		t.Errorf("should have slept until Reset, slept only %v", time.Since(start))
	}
}

func TestPoller_RunStopsOnContextCancel(t *testing.T) {
	gh := &fakeNotifGetter{}
	p := newTestPoller(t, sourceNotifications, []string{"octo/hello"}, gh, newPollDispatcher())
	p.interval = time.Hour
	ctx, cancel := stdctx.WithCancel(stdctx.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop on ctx cancel")
	}
}
