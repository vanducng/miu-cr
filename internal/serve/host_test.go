package serve

import (
	stdctx "context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-github/v84/github"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/store"
)

func TestHostRunnerGroupsReposByAccountAndFailsUnknown(t *testing.T) {
	cfg := hostRunnerConfig(t)
	cfg.TokenSources = map[string]HostTokenSource{}
	if _, err := NewHostRunner(cfg); err == nil {
		t.Fatal("expected unknown account error")
	}
	cfg.TokenSources["main"] = &countTokenSource{token: "tok"}
	r, err := NewHostRunner(cfg)
	if err != nil {
		t.Fatalf("NewHostRunner: %v", err)
	}
	groups := r.Groups()
	if len(groups["main"]) != 1 || groups["main"][0] != "octo/hello" {
		t.Fatalf("groups = %+v", groups)
	}
}

func TestHostRunnerRequiresEnabledPollingRepo(t *testing.T) {
	cfg := hostRunnerConfig(t)
	cfg.Repos[0].Poll = false
	if _, err := NewHostRunner(cfg); err == nil {
		t.Fatal("expected no enabled polling repo error")
	}
}

func TestHostRunnerPollsWithStoreCursorAndNoFileCursor(t *testing.T) {
	cfg := hostRunnerConfig(t)
	dir := t.TempDir()
	orig := configDir
	configDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { configDir = orig })
	st := cfg.Store.(*fakeHostStore)
	disp := cfg.Dispatcher.(*pollDispatcher)
	r, err := NewHostRunner(cfg)
	if err != nil {
		t.Fatalf("NewHostRunner: %v", err)
	}
	if err := r.Tick(stdctx.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if st.cursorWrites != 1 {
		t.Fatalf("cursor writes = %d, want 1", st.cursorWrites)
	}
	if disp.count() != 1 {
		t.Fatalf("submitted jobs = %d, want 1", disp.count())
	}
	path, err := cursorPath()
	if err != nil {
		t.Fatalf("cursorPath: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file cursor was touched: %v", err)
	}
}

func TestHostRunnerPollsAllPRPages(t *testing.T) {
	cfg := hostRunnerConfig(t)
	gh := &fakeNotifGetter{
		prPages: map[string][][]*github.PullRequest{
			"octo/hello": {
				{prWithHead(1, "sha-A")},
				{prWithHead(2, "sha-B")},
			},
		},
	}
	cfg.NewNotifGetter = func(token string) notifGetter { return gh }
	disp := cfg.Dispatcher.(*pollDispatcher)
	r, err := NewHostRunner(cfg)
	if err != nil {
		t.Fatalf("NewHostRunner: %v", err)
	}
	if err := r.Tick(stdctx.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if gh.listCalls != 2 {
		t.Fatalf("ListOpenPRs calls = %d, want 2", gh.listCalls)
	}
	if disp.count() != 2 {
		t.Fatalf("submitted jobs = %d, want 2", disp.count())
	}
}

func TestHostRunnerSkipsIgnoredPRsBeforeQueue(t *testing.T) {
	cfg := hostRunnerConfig(t)
	cfg.Repos[0].PRFilter = config.HostPRFilter{Rules: []config.HostPRFilterRule{{
		Action:       "exclude",
		TitleRegexes: []string{`^chore\(deps\):`},
	}}}
	cfg.NewNotifGetter = func(string) notifGetter {
		return &fakeNotifGetter{prs: map[string][]*github.PullRequest{"octo/hello": {
			prWithMeta(1, "sha-A", "chore(deps): update redis", "renovate[bot]", "Bot", nil, nil, false),
			prWithMeta(2, "sha-B", "fix: keep service alive", "vanducng", "User", nil, nil, false),
		}}}
	}
	disp := cfg.Dispatcher.(*pollDispatcher)
	r, err := NewHostRunner(cfg)
	if err != nil {
		t.Fatalf("NewHostRunner: %v", err)
	}
	if err := r.Tick(stdctx.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if disp.count() != 1 {
		t.Fatalf("submitted jobs = %d, want 1", disp.count())
	}
}

func TestHostRunnerSkipsDraftPRsByDefault(t *testing.T) {
	cfg := hostRunnerConfig(t)
	cfg.NewNotifGetter = func(string) notifGetter {
		return &fakeNotifGetter{prs: map[string][]*github.PullRequest{"octo/hello": {
			prWithMeta(1, "sha-A", "fix: still drafting", "vanducng", "User", nil, nil, true),
			prWithMeta(2, "sha-B", "fix: ready", "vanducng", "User", nil, nil, false),
		}}}
	}
	disp := cfg.Dispatcher.(*pollDispatcher)
	r, err := NewHostRunner(cfg)
	if err != nil {
		t.Fatalf("NewHostRunner: %v", err)
	}
	if err := r.Tick(stdctx.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if disp.count() != 1 {
		t.Fatalf("submitted jobs = %d, want 1", disp.count())
	}
}

func TestHostPRFilterLastMatchWins(t *testing.T) {
	filter := config.HostPRFilter{Rules: []config.HostPRFilterRule{
		{Action: "exclude", AuthorTypes: []string{"Bot"}},
		{Action: "include", Authors: []string{"renovate[bot]"}, Labels: []string{"review"}, BaseBranches: []string{"main"}},
	}}
	if !hostPRFilterAllows(filter, prWithMeta(1, "sha-A", "chore(deps): update redis", "renovate[bot]", "Bot", nil, []string{"review"}, false)) {
		t.Fatal("repo include rule should override prior bot exclude")
	}
	if hostPRFilterAllows(filter, prWithMeta(2, "sha-B", "chore(deps): update redis", "renovate[bot]", "Bot", nil, nil, false)) {
		t.Fatal("bot exclude should hold when later include does not match")
	}
}

func TestHostPRFilterCanIncludeByAuthorOrRequestedReviewer(t *testing.T) {
	filter := config.HostPRFilter{
		DefaultAction: "exclude",
		Rules: []config.HostPRFilterRule{
			{Action: "include", Authors: []string{"vanducng"}},
			{Action: "include", RequestedReviewers: []string{"vanducng"}},
		},
	}
	if !hostPRFilterAllows(filter, prWithMeta(1, "sha-A", "fix: mine", "vanducng", "User", nil, nil, false)) {
		t.Fatal("author include should review")
	}
	if !hostPRFilterAllows(filter, prWithMeta(2, "sha-B", "fix: please review", "teammate", "User", []string{"vanducng"}, nil, false)) {
		t.Fatal("requested reviewer include should review")
	}
	if hostPRFilterAllows(filter, prWithMeta(3, "sha-C", "fix: unrelated", "teammate", "User", nil, nil, false)) {
		t.Fatal("unmatched PR should stay excluded")
	}
}

func TestHostPRFilterProbeCases(t *testing.T) {
	includeDrafts := true
	cases := []struct {
		name   string
		filter config.HostPRFilter
		pr     *github.PullRequest
		want   bool
	}{
		{
			name: "default includes ready PR",
			pr:   prWithMeta(1, "sha-A", "fix: ready", "vanducng", "User", nil, nil, false),
			want: true,
		},
		{
			name: "default skips draft PR",
			pr:   prWithMeta(1, "sha-A", "fix: draft", "vanducng", "User", nil, nil, true),
		},
		{
			name:   "include drafts allows draft PR",
			filter: config.HostPRFilter{IncludeDrafts: &includeDrafts},
			pr:     prWithMeta(1, "sha-A", "fix: draft", "vanducng", "User", nil, nil, true),
			want:   true,
		},
		{
			name:   "default action exclude blocks unmatched PR",
			filter: config.HostPRFilter{DefaultAction: "exclude"},
			pr:     prWithMeta(1, "sha-A", "fix: unrelated", "teammate", "User", nil, nil, false),
		},
		{
			name:   "title regex excludes release PR",
			filter: config.HostPRFilter{Rules: []config.HostPRFilterRule{{Action: "exclude", TitleRegexes: []string{`^chore\(main\): release `}}}},
			pr:     prWithMeta(1, "sha-A", "chore(main): release 0.57.0", "app/munmiu", "Bot", nil, nil, false),
		},
		{
			name:   "author type excludes bot",
			filter: config.HostPRFilter{Rules: []config.HostPRFilterRule{{Action: "exclude", AuthorTypes: []string{"Bot"}}}},
			pr:     prWithMeta(1, "sha-A", "chore: generated", "renovate[bot]", "Bot", nil, nil, false),
		},
		{
			name:   "author association includes owner",
			filter: config.HostPRFilter{DefaultAction: "exclude", Rules: []config.HostPRFilterRule{{Action: "include", AuthorAssociations: []string{"OWNER"}}}},
			pr:     prWithAssociation(prWithMeta(1, "sha-A", "fix: owner", "vanducng", "User", nil, nil, false), "OWNER"),
			want:   true,
		},
		{
			name:   "label includes PR",
			filter: config.HostPRFilter{DefaultAction: "exclude", Rules: []config.HostPRFilterRule{{Action: "include", Labels: []string{"miucr-review"}}}},
			pr:     prWithMeta(1, "sha-A", "fix: label", "teammate", "User", nil, []string{"miucr-review"}, false),
			want:   true,
		},
		{
			name:   "requested reviewer includes PR",
			filter: config.HostPRFilter{DefaultAction: "exclude", Rules: []config.HostPRFilterRule{{Action: "include", RequestedReviewers: []string{"vanducng"}}}},
			pr:     prWithMeta(1, "sha-A", "fix: review me", "teammate", "User", []string{"vanducng"}, nil, false),
			want:   true,
		},
		{
			name:   "base and head branches include PR",
			filter: config.HostPRFilter{DefaultAction: "exclude", Rules: []config.HostPRFilterRule{{Action: "include", BaseBranches: []string{"main"}, HeadBranches: []string{"release-please--branches--main"}}}},
			pr:     prWithBranches(prWithMeta(1, "sha-A", "chore(main): release 0.57.0", "app/munmiu", "Bot", nil, nil, false), "main", "release-please--branches--main"),
			want:   true,
		},
		{
			name:   "rule matchers are conjunctive",
			filter: config.HostPRFilter{DefaultAction: "exclude", Rules: []config.HostPRFilterRule{{Action: "include", Authors: []string{"vanducng"}, Labels: []string{"miucr-review"}}}},
			pr:     prWithMeta(1, "sha-A", "fix: mine without label", "vanducng", "User", nil, nil, false),
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := hostPRFilterAllows(tt.filter, tt.pr); got != tt.want {
				t.Fatalf("hostPRFilterAllows = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHostRunnerTickReturnsPollIntervalFloor(t *testing.T) {
	cfg := hostRunnerConfig(t)
	resp := &github.Response{Response: &http.Response{Header: make(http.Header)}}
	resp.Header.Set("X-Poll-Interval", "120")
	gh := &fakeNotifGetter{
		prs:       map[string][]*github.PullRequest{"octo/hello": {prWithHead(1, "sha-A")}},
		notifResp: resp,
	}
	cfg.NewNotifGetter = func(string) notifGetter { return gh }
	r, err := NewHostRunner(cfg)
	if err != nil {
		t.Fatalf("NewHostRunner: %v", err)
	}
	wait, err := r.tick(stdctx.Background(), r.snapshot())
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if wait != 120*time.Second {
		t.Fatalf("poll wait = %v, want 120s", wait)
	}
}

func TestHostRunnerRepeatedPollSameHeadDoesNotDuplicate(t *testing.T) {
	cfg := hostRunnerConfig(t)
	disp := cfg.Dispatcher.(*pollDispatcher)
	r, err := NewHostRunner(cfg)
	if err != nil {
		t.Fatalf("NewHostRunner: %v", err)
	}
	if err := r.Tick(stdctx.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	if err := r.Tick(stdctx.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if disp.count() != 1 {
		t.Fatalf("submitted jobs = %d, want 1", disp.count())
	}
}

func TestHostRunnerPollCancelsQueuedJobsForClosedPRs(t *testing.T) {
	cfg := hostRunnerConfig(t)
	gh := &fakeNotifGetter{prs: map[string][]*github.PullRequest{
		"octo/hello": {prWithHead(1, "sha-A"), prWithHead(2, "sha-B")},
	}}
	cfg.NewNotifGetter = func(string) notifGetter { return gh }
	disp := cfg.Dispatcher.(*pollDispatcher)
	disp.results = []SubmitResult{SubmitDuplicate, SubmitDuplicate}
	st := cfg.Store.(*fakeHostStore)
	r, err := NewHostRunner(cfg)
	if err != nil {
		t.Fatalf("NewHostRunner: %v", err)
	}
	if err := r.Tick(stdctx.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	// PR #2 closes: it disappears from the open list before the duplicate retry window.
	gh.prs["octo/hello"] = []*github.PullRequest{prWithHead(1, "sha-A")}
	if err := r.Tick(stdctx.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	if got := st.lastReconcile.OpenNumbers; len(got) != 1 || got[0] != 1 {
		t.Fatalf("reconcile open numbers = %v, want [1]", got)
	}
	if st.sessions[2].State != "closed" {
		t.Fatalf("session #2 state = %q, want closed", st.sessions[2].State)
	}
	if st.sessions[1].State != "open" {
		t.Fatalf("session #1 state = %q, want open", st.sessions[1].State)
	}
	for _, job := range st.jobs {
		switch job.Number {
		case 2:
			if job.Status != "canceled" {
				t.Fatalf("closed PR #2 job status = %q, want canceled", job.Status)
			}
		case 1:
			if job.Status != "queued" {
				t.Fatalf("open PR #1 job status = %q, want queued", job.Status)
			}
		}
	}
}

func TestHostRunnerReloadsBeforeEachTick(t *testing.T) {
	cfg := hostRunnerConfig(t)
	oldRepo := cfg.Repos[0]
	oldRepo.PromptHash = "prompt-old"
	oldRepo.RulesHash = "rules-old"
	oldRepo.Review.OperatorPrompt = "old prompt"
	newRepo := oldRepo
	newRepo.PromptHash = "prompt-new"
	newRepo.RulesHash = "rules-new"
	newRepo.Review.OperatorPrompt = "new prompt"
	cfg.Repos = []HostRepoConfig{oldRepo}
	reloads := []HostReload{
		{Repos: []HostRepoConfig{oldRepo}, TokenSources: cfg.TokenSources, Interval: cfg.Interval, ReviewTO: cfg.ReviewTO},
		{Repos: []HostRepoConfig{newRepo}, TokenSources: cfg.TokenSources, Interval: cfg.Interval, ReviewTO: cfg.ReviewTO},
	}
	cfg.Reload = func(stdctx.Context) (HostReload, error) {
		next := reloads[0]
		if len(reloads) > 1 {
			reloads = reloads[1:]
		}
		return next, nil
	}
	disp := cfg.Dispatcher.(*pollDispatcher)
	r, err := NewHostRunner(cfg)
	if err != nil {
		t.Fatalf("NewHostRunner: %v", err)
	}
	if err := r.Tick(stdctx.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	if err := r.Tick(stdctx.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	disp.mu.Lock()
	defer disp.mu.Unlock()
	if len(disp.jobs) != 2 {
		t.Fatalf("submitted jobs = %d, want 2", len(disp.jobs))
	}
	if disp.jobs[0].Review.OperatorPrompt != "old prompt" || disp.jobs[1].Review.OperatorPrompt != "new prompt" {
		t.Fatalf("operator prompts = %q, %q", disp.jobs[0].Review.OperatorPrompt, disp.jobs[1].Review.OperatorPrompt)
	}
}

func TestHostRunnerTickKeepsPreviousConfigAfterReloadError(t *testing.T) {
	cfg := hostRunnerConfig(t)
	gh := &fakeNotifGetter{prs: map[string][]*github.PullRequest{
		"octo/hello": {prWithHead(1, "sha-A")},
	}}
	cfg.NewNotifGetter = func(string) notifGetter { return gh }
	repo := cfg.Repos[0]
	repo.PromptHash = "prompt-new"
	repo.RulesHash = "rules-new"
	repo.Review.OperatorPrompt = "new prompt"
	reloads := []struct {
		next HostReload
		err  error
	}{
		{next: HostReload{Repos: []HostRepoConfig{repo}, TokenSources: cfg.TokenSources, Interval: cfg.Interval, ReviewTO: cfg.ReviewTO}},
		{err: errors.New("reload failed")},
	}
	cfg.Reload = func(stdctx.Context) (HostReload, error) {
		next := reloads[0]
		if len(reloads) > 1 {
			reloads = reloads[1:]
		}
		return next.next, next.err
	}
	disp := cfg.Dispatcher.(*pollDispatcher)
	r, err := NewHostRunner(cfg)
	if err != nil {
		t.Fatalf("NewHostRunner: %v", err)
	}
	if err := r.Tick(stdctx.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	gh.prs["octo/hello"] = []*github.PullRequest{prWithHead(2, "sha-B")}
	if err := r.Tick(stdctx.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	disp.mu.Lock()
	defer disp.mu.Unlock()
	if len(disp.jobs) != 2 {
		t.Fatalf("submitted jobs = %d, want 2", len(disp.jobs))
	}
	if disp.jobs[1].Review.OperatorPrompt != "new prompt" {
		t.Fatalf("operator prompt = %q, want previous reload config", disp.jobs[1].Review.OperatorPrompt)
	}
}

func TestHostRunnerFailedReviewRetriesSameHead(t *testing.T) {
	cfg := hostRunnerConfig(t)
	now := time.Date(2026, 6, 27, 10, 30, 0, 0, time.UTC)
	cfg.Now = func() time.Time { return now }
	disp := cfg.Dispatcher.(*pollDispatcher)
	disp.failErr = errors.New("review failed")
	r, err := NewHostRunner(cfg)
	if err != nil {
		t.Fatalf("NewHostRunner: %v", err)
	}
	if err := r.Tick(stdctx.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	if err := r.Tick(stdctx.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if disp.count() != 1 {
		t.Fatalf("submitted jobs = %d, want no retry before failed backoff", disp.count())
	}
	now = now.Add(hostFailedRetryDelay(1))
	if err := r.Tick(stdctx.Background()); err != nil {
		t.Fatalf("third tick: %v", err)
	}
	if disp.count() != 2 {
		t.Fatalf("submitted jobs = %d, want retry after failed backoff", disp.count())
	}
}

func TestHostRunnerTokenFailureBacksOffSameHead(t *testing.T) {
	cfg := hostRunnerConfig(t)
	now := time.Date(2026, 6, 27, 10, 45, 0, 0, time.UTC)
	cfg.Now = func() time.Time { return now }
	cfg.TokenSources["main"] = &sequenceTokenSource{results: []tokenResult{
		{token: "tok"},
		{err: errors.New("quota")},
	}}
	st := cfg.Store.(*fakeHostStore)
	r, err := NewHostRunner(cfg)
	if err != nil {
		t.Fatalf("NewHostRunner: %v", err)
	}
	if err := r.Tick(stdctx.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	var failed store.HostJob
	for _, job := range st.jobs {
		if job.Status == "failed" {
			failed = job
			break
		}
	}
	if failed.ID == 0 {
		t.Fatal("expected failed job")
	}
	if want := now.Add(hostFailedRetryDelay(1)); failed.AvailableAt.Before(want) {
		t.Fatalf("failed token retry available_at = %v, want >= %v", failed.AvailableAt, want)
	}
}

func TestHostRunnerRejectedSubmitContinuesClaimBatch(t *testing.T) {
	for _, result := range []SubmitResult{SubmitDuplicate, SubmitCoalesced, SubmitFull} {
		t.Run(result.String(), func(t *testing.T) {
			cfg := hostRunnerConfig(t)
			cfg.NewNotifGetter = func(string) notifGetter {
				return &fakeNotifGetter{prs: map[string][]*github.PullRequest{
					"octo/hello": {prWithHead(1, "sha-A"), prWithHead(2, "sha-B")},
				}}
			}
			disp := cfg.Dispatcher.(*pollDispatcher)
			disp.results = []SubmitResult{result, SubmitQueued}
			st := cfg.Store.(*fakeHostStore)
			r, err := NewHostRunner(cfg)
			if err != nil {
				t.Fatalf("NewHostRunner: %v", err)
			}
			if err := r.Tick(stdctx.Background()); err != nil {
				t.Fatalf("Tick: %v", err)
			}
			if st.releaseCount != 1 {
				t.Fatalf("release count = %d, want 1", st.releaseCount)
			}
			if disp.count() != 1 {
				t.Fatalf("submitted jobs = %d, want second job submitted", disp.count())
			}
		})
	}
}

func TestHostRunnerDuplicateSubmitDelaysRetryUntilReviewTimeout(t *testing.T) {
	now := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
	cfg := hostRunnerConfig(t)
	cfg.Now = func() time.Time { return now }
	cfg.Interval = 30 * time.Second
	cfg.ReviewTO = 5 * time.Minute
	disp := cfg.Dispatcher.(*pollDispatcher)
	disp.results = []SubmitResult{SubmitDuplicate}
	st := cfg.Store.(*fakeHostStore)
	r, err := NewHostRunner(cfg)
	if err != nil {
		t.Fatalf("NewHostRunner: %v", err)
	}
	if err := r.Tick(stdctx.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	wantMin := now.Add(6 * time.Minute)
	if st.lastRelease.AvailableAt.Before(wantMin) {
		t.Fatalf("duplicate retry available_at = %v, want >= %v", st.lastRelease.AvailableAt, wantMin)
	}
}

func TestHostRunnerDispatcherRejectReleasesJob(t *testing.T) {
	cfg := hostRunnerConfig(t)
	disp := cfg.Dispatcher.(*pollDispatcher)
	disp.accept = false
	st := cfg.Store.(*fakeHostStore)
	r, err := NewHostRunner(cfg)
	if err != nil {
		t.Fatalf("NewHostRunner: %v", err)
	}
	if err := r.Tick(stdctx.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if disp.count() != 0 {
		t.Fatalf("submitted jobs = %d, want none", disp.count())
	}
	if st.releaseCount != 1 {
		t.Fatalf("release count = %d, want 1", st.releaseCount)
	}
	for _, job := range st.jobs {
		if job.Status != "queued" {
			t.Fatalf("job status = %s, want queued", job.Status)
		}
	}
}

func TestHostRunnerClaimsAreBoundedPerTick(t *testing.T) {
	cfg := hostRunnerConfig(t)
	prs := make([]*github.PullRequest, 0, maxHostClaimsPerTick+3)
	for i := 1; i <= maxHostClaimsPerTick+3; i++ {
		prs = append(prs, prWithHead(i, "sha-"+itoa(i)))
	}
	cfg.NewNotifGetter = func(string) notifGetter {
		return &fakeNotifGetter{prs: map[string][]*github.PullRequest{"octo/hello": prs}}
	}
	disp := cfg.Dispatcher.(*pollDispatcher)
	r, err := NewHostRunner(cfg)
	if err != nil {
		t.Fatalf("NewHostRunner: %v", err)
	}
	if err := r.Tick(stdctx.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if disp.count() != maxHostClaimsPerTick {
		t.Fatalf("submitted jobs = %d, want %d", disp.count(), maxHostClaimsPerTick)
	}
}

func TestHostRunnerSinglePRStoreErrorDoesNotAbortRepo(t *testing.T) {
	cfg := hostRunnerConfig(t)
	cfg.NewNotifGetter = func(string) notifGetter {
		return &fakeNotifGetter{prs: map[string][]*github.PullRequest{
			"octo/hello": {prWithHead(1, "bad"), prWithHead(2, "good")},
		}}
	}
	st := cfg.Store.(*fakeHostStore)
	st.sessionErrFor = map[int64]error{1: errors.New("session write failed")}
	disp := cfg.Dispatcher.(*pollDispatcher)
	r, err := NewHostRunner(cfg)
	if err != nil {
		t.Fatalf("NewHostRunner: %v", err)
	}
	if err := r.Tick(stdctx.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if disp.count() != 1 {
		t.Fatalf("submitted jobs = %d, want remaining PR submitted", disp.count())
	}
}

func TestHostRunnerTokenSourceCalledAcrossTicks(t *testing.T) {
	cfg := hostRunnerConfig(t)
	src := cfg.TokenSources["main"].(*countTokenSource)
	r, err := NewHostRunner(cfg)
	if err != nil {
		t.Fatalf("NewHostRunner: %v", err)
	}
	if err := r.Tick(stdctx.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	first := src.calls()
	if err := r.Tick(stdctx.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if src.calls() <= first {
		t.Fatalf("token source calls did not increase: first=%d now=%d", first, src.calls())
	}
}

func TestHostRunnerJanitorBuildsPrunePolicy(t *testing.T) {
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	cfg := hostRunnerConfig(t)
	cfg.Now = func() time.Time { return now }
	cfg.Prune = HostPruneConfig{ClosedSessionTTL: time.Hour, CompletedJobTTL: 2 * time.Hour, FinishedAttemptTTL: 3 * time.Hour, InactiveWorkspaceTTL: 4 * time.Hour, PollCursorTTL: 5 * time.Hour}
	st := cfg.Store.(*fakeHostStore)
	r, err := NewHostRunner(cfg)
	if err != nil {
		t.Fatalf("NewHostRunner: %v", err)
	}
	if err := r.RunJanitor(stdctx.Background()); err != nil {
		t.Fatalf("RunJanitor: %v", err)
	}
	if st.lastPrune.ClosedSessionsBefore != now.Add(-time.Hour) || st.lastPrune.InactiveWorkspacesBefore != now.Add(-4*time.Hour) {
		t.Fatalf("wrong prune policy: %+v", st.lastPrune)
	}
}

func TestRunHostReturnsWhenRunnerDoesNotStop(t *testing.T) {
	oldGrace := runHostDrainGrace
	runHostDrainGrace = 5 * time.Millisecond
	t.Cleanup(func() { runHostDrainGrace = oldGrace })

	cfg := hostRunnerConfig(t)
	block := make(chan struct{})
	cfg.Prune = HostPruneConfig{CompletedJobTTL: time.Hour}
	cfg.Store.(*fakeHostStore).pruneBlock = block
	r, err := NewHostRunner(cfg)
	if err != nil {
		t.Fatalf("NewHostRunner: %v", err)
	}
	ctx, cancel := stdctx.WithCancel(stdctx.Background())
	cancel()
	err = RunHost(ctx, nil, r)
	close(block)
	if !errors.Is(err, ErrHostRunnerStopTimeout) || !strings.Contains(err.Error(), "did not stop") {
		t.Fatalf("RunHost error = %v, want stop deadline", err)
	}
}

func hostRunnerConfig(t *testing.T) HostRunnerConfig {
	t.Helper()
	gh := &fakeNotifGetter{
		prs: map[string][]*github.PullRequest{
			"octo/hello": {prWithHead(1, "sha-A")},
		},
	}
	return HostRunnerConfig{
		Store:           newFakeHostStore(),
		Repos:           []HostRepoConfig{{Name: "hello", Owner: "octo", Repo: "hello", Slug: "octo/hello", GitURL: "https://github.com/octo/hello.git", DefaultBranch: "main", GithubAccount: "main", Enabled: true, Poll: true, PolicyHash: "p", PromptHash: "prompt", RulesHash: "rules", Review: JobReviewOptions{Post: true, Gate: "high"}}},
		TokenSources:    map[string]HostTokenSource{"main": &countTokenSource{token: "tok"}},
		Source:          sourcePulls,
		Interval:        time.Hour,
		Dispatcher:      newPollDispatcher(),
		Logger:          discardLog(),
		ReviewTO:        time.Minute,
		WorkerID:        "worker",
		JanitorInterval: time.Hour,
		NewNotifGetter: func(token string) notifGetter {
			return gh
		},
		Now: time.Now,
	}
}

func prWithMeta(number int, sha, title, login, userType string, reviewers, labels []string, draft bool) *github.PullRequest {
	pr := prWithHead(number, sha)
	pr.Title = github.Ptr(title)
	pr.User = &github.User{Login: github.Ptr(login), Type: github.Ptr(userType)}
	pr.AuthorAssociation = github.Ptr("MEMBER")
	pr.Draft = github.Ptr(draft)
	pr.Base = &github.PullRequestBranch{Ref: github.Ptr("main"), SHA: github.Ptr("base")}
	pr.Head = &github.PullRequestBranch{Ref: github.Ptr("branch"), SHA: github.Ptr(sha)}
	for _, reviewer := range reviewers {
		pr.RequestedReviewers = append(pr.RequestedReviewers, &github.User{Login: github.Ptr(reviewer)})
	}
	for _, label := range labels {
		pr.Labels = append(pr.Labels, &github.Label{Name: github.Ptr(label)})
	}
	return pr
}

func prWithAssociation(pr *github.PullRequest, association string) *github.PullRequest {
	pr.AuthorAssociation = github.Ptr(association)
	return pr
}

func prWithBranches(pr *github.PullRequest, base, head string) *github.PullRequest {
	pr.Base.Ref = github.Ptr(base)
	pr.Head.Ref = github.Ptr(head)
	return pr
}

type countTokenSource struct {
	mu    sync.Mutex
	token string
	n     int
}

func (s *countTokenSource) Token(stdctx.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return s.token, nil
}

func (s *countTokenSource) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

type tokenResult struct {
	token string
	err   error
}

type sequenceTokenSource struct {
	mu      sync.Mutex
	results []tokenResult
}

func (s *sequenceTokenSource) Token(stdctx.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.results) == 0 {
		return "", errors.New("no token result")
	}
	r := s.results[0]
	s.results = s.results[1:]
	return r.token, r.err
}

type fakeHostStore struct {
	mu            sync.Mutex
	nextRepo      int64
	nextSession   int64
	nextJob       int64
	nextAttempt   int64
	repos         map[string]store.HostRepo
	jobs          map[string]store.HostJob
	sessions      map[int64]store.HostPRSession
	queued        []string
	cursorWrites  int
	releaseCount  int
	lastRelease   store.HostJobReleaseInput
	lastPrune     store.HostPrunePolicy
	lastReconcile store.HostClosedPRsInput
	pruneBlock    <-chan struct{}
	sessionErrFor map[int64]error
}

func newFakeHostStore() *fakeHostStore {
	return &fakeHostStore{nextRepo: 1, nextSession: 1, nextJob: 1, nextAttempt: 1, repos: map[string]store.HostRepo{}, jobs: map[string]store.HostJob{}, sessions: map[int64]store.HostPRSession{}}
}

func (s *fakeHostStore) ReconcileHostRepo(_ stdctx.Context, in store.HostRepoInput) (store.HostRepo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.repos[in.Slug]; ok {
		r.HostRepoInput = in
		s.repos[in.Slug] = r
		return r, nil
	}
	r := store.HostRepo{ID: s.nextRepo, HostRepoInput: in}
	s.nextRepo++
	s.repos[in.Slug] = r
	return r, nil
}

func (s *fakeHostStore) UpsertHostPRSession(_ stdctx.Context, in store.HostPRSessionInput) (store.HostPRSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.sessionErrFor[in.Number]; err != nil {
		return store.HostPRSession{}, err
	}
	session := store.HostPRSession{ID: s.nextSession, HostPRSessionInput: in}
	s.nextSession++
	s.sessions[in.Number] = session
	return session, nil
}

func (s *fakeHostStore) ReconcileHostClosedPRs(_ stdctx.Context, in store.HostClosedPRsInput) (store.HostClosedPRsResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastReconcile = in
	open := map[int64]bool{}
	for _, n := range in.OpenNumbers {
		open[n] = true
	}
	var out store.HostClosedPRsResult
	for number, session := range s.sessions {
		if session.State == "open" && !open[number] {
			session.State = "closed"
			s.sessions[number] = session
			out.SessionsClosed++
		}
	}
	canceled := map[string]bool{}
	for key, job := range s.jobs {
		if job.Status == "queued" && !open[job.Number] {
			job.Status = "canceled"
			job.Error = "PR no longer open"
			s.jobs[key] = job
			canceled[key] = true
			out.JobsCanceled++
		}
	}
	kept := s.queued[:0]
	for _, key := range s.queued {
		if !canceled[key] {
			kept = append(kept, key)
		}
	}
	s.queued = kept
	return out, nil
}

func (s *fakeHostStore) EnqueueHostJob(_ stdctx.Context, in store.HostJobInput) (store.HostJob, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in.DedupeKey = store.HostJobDedupeKey(in)
	if j, ok := s.jobs[in.DedupeKey]; ok {
		if j.Status == "failed" && j.AvailableAt.After(in.Now) {
			return j, false, nil
		}
		if j.Status == "failed" || j.Status == "canceled" {
			j.Status = "queued"
			j.Error = ""
			s.jobs[in.DedupeKey] = j
			s.queued = append(s.queued, in.DedupeKey)
			return j, true, nil
		}
		return j, false, nil
	}
	j := store.HostJob{ID: s.nextJob, HostJobInput: in, Status: "queued"}
	if j.AvailableAt.IsZero() {
		j.AvailableAt = time.Now()
	}
	s.nextJob++
	s.jobs[in.DedupeKey] = j
	s.queued = append(s.queued, in.DedupeKey)
	return j, true, nil
}

func (s *fakeHostStore) ClaimHostJob(_ stdctx.Context, in store.HostJobClaimInput) (store.HostJobClaim, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queued) == 0 {
		return store.HostJobClaim{}, false, nil
	}
	key := s.queued[0]
	s.queued = s.queued[1:]
	job := s.jobs[key]
	job.Status = "running"
	job.LeaseOwner = in.WorkerID
	job.Attempts++
	s.jobs[key] = job
	claim := store.HostJobClaim{Job: job, AttemptID: s.nextAttempt}
	s.nextAttempt++
	return claim, true, nil
}

func (s *fakeHostStore) CompleteHostJob(_ stdctx.Context, in store.HostJobCompleteInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, job := range s.jobs {
		if job.ID == in.JobID {
			job.Status = in.Status
			if !in.AvailableAt.IsZero() {
				job.AvailableAt = in.AvailableAt
			}
			s.jobs[key] = job
			return nil
		}
	}
	return errors.New("job not found")
}

func (s *fakeHostStore) ReleaseHostJob(_ stdctx.Context, in store.HostJobReleaseInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, job := range s.jobs {
		if job.ID == in.JobID {
			job.Status = "queued"
			job.Error = in.Error
			job.LeaseOwner = ""
			job.AvailableAt = in.AvailableAt
			if job.Attempts > 0 {
				job.Attempts--
			}
			s.jobs[key] = job
			if in.AvailableAt.IsZero() || !in.AvailableAt.After(time.Now()) {
				s.queued = append(s.queued, key)
			}
			s.releaseCount++
			s.lastRelease = in
			return nil
		}
	}
	return errors.New("job not found")
}

func (s *fakeHostStore) UpsertHostWorkspace(stdctx.Context, store.HostWorkspaceInput) (store.HostWorkspace, error) {
	return store.HostWorkspace{}, nil
}

func (s *fakeHostStore) UpsertHostPollCursor(stdctx.Context, store.HostPollCursorInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursorWrites++
	return nil
}

func (s *fakeHostStore) GetHostPollCursor(stdctx.Context, int64, string) (store.HostPollCursor, bool, error) {
	return store.HostPollCursor{}, false, nil
}

func (s *fakeHostStore) PruneHost(_ stdctx.Context, p store.HostPrunePolicy) (store.HostPruneResult, error) {
	if s.pruneBlock != nil {
		<-s.pruneBlock
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastPrune = p
	return store.HostPruneResult{}, nil
}
