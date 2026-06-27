package serve

import (
	stdctx "context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/go-github/v84/github"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/store"
)

type HostTokenSource interface {
	Token(stdctx.Context) (string, error)
}

type HostRunnerConfig struct {
	Store           store.HostStore
	Repos           []HostRepoConfig
	TokenSources    map[string]HostTokenSource
	Source          pollSource
	Interval        time.Duration
	Dispatcher      Dispatcher
	Logger          *slog.Logger
	ReviewTO        time.Duration
	WorkerID        string
	NewNotifGetter  func(string) notifGetter
	Prune           HostPruneConfig
	JanitorInterval time.Duration
	Now             func() time.Time
}

type HostRepoConfig struct {
	Name          string
	Owner         string
	Repo          string
	Slug          string
	GitURL        string
	DefaultBranch string
	GithubAccount string
	Enabled       bool
	Poll          bool
	ConfigHash    string
	PolicyHash    string
	PromptHash    string
	RulesHash     string
	ReviewTimeout time.Duration
	Review        JobReviewOptions
}

type HostRunner struct {
	store           store.HostStore
	repos           []HostRepoConfig
	tokens          map[string]HostTokenSource
	src             pollSource
	interval        time.Duration
	disp            Dispatcher
	log             *slog.Logger
	reviewTO        time.Duration
	workerID        string
	newGetter       func(string) notifGetter
	prune           HostPruneConfig
	janitorInterval time.Duration
	now             func() time.Time
}

type HostPruneConfig struct {
	ClosedSessionTTL     time.Duration
	CompletedJobTTL      time.Duration
	FinishedAttemptTTL   time.Duration
	InactiveWorkspaceTTL time.Duration
	PollCursorTTL        time.Duration
}

func NewHostRunner(cfg HostRunnerConfig) (*HostRunner, error) {
	if cfg.Store == nil {
		return nil, errors.New("host: store is required")
	}
	if cfg.Dispatcher == nil {
		return nil, errors.New("host: dispatcher is required")
	}
	if cfg.NewNotifGetter == nil {
		cfg.NewNotifGetter = NewNotifGetter
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.WorkerID == "" {
		cfg.WorkerID = fmt.Sprintf("host-%d", cfg.Now().UnixNano())
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Minute
	}
	if cfg.Source == "" {
		cfg.Source = sourcePulls
	}
	if cfg.Source != sourcePulls {
		return nil, errors.New("host: only poll_source=pulls is supported in this milestone")
	}
	enabledRepos := 0
	for _, r := range cfg.Repos {
		if !r.Enabled || !r.Poll {
			continue
		}
		enabledRepos++
		if cfg.TokenSources[r.GithubAccount] == nil {
			return nil, fmt.Errorf("host: repo %s references unknown GitHub account %q", r.Slug, r.GithubAccount)
		}
	}
	if enabledRepos == 0 {
		return nil, errors.New("host: at least one enabled polling repo is required")
	}
	return &HostRunner{
		store:           cfg.Store,
		repos:           cfg.Repos,
		tokens:          cfg.TokenSources,
		src:             cfg.Source,
		interval:        cfg.Interval,
		disp:            cfg.Dispatcher,
		log:             cfg.Logger,
		reviewTO:        cfg.ReviewTO,
		workerID:        cfg.WorkerID,
		newGetter:       cfg.NewNotifGetter,
		prune:           cfg.Prune,
		janitorInterval: cfg.JanitorInterval,
		now:             cfg.Now,
	}, nil
}

func (h *HostRunner) Groups() map[string][]string {
	out := map[string][]string{}
	for _, r := range h.repos {
		if !r.Enabled || !r.Poll {
			continue
		}
		out[r.GithubAccount] = append(out[r.GithubAccount], r.Slug)
	}
	return out
}

func (h *HostRunner) Run(ctx stdctx.Context) {
	nextJanitor := time.Time{}
	for {
		start := h.now()
		if !h.janitorIntervalIsOff() && !start.Before(nextJanitor) {
			if err := h.RunJanitor(ctx); err != nil && ctx.Err() == nil {
				h.log.Warn("host: janitor failed", "error", config.RedactString(err.Error()))
			}
			nextJanitor = start.Add(h.janitorInterval)
		}
		if err := h.Tick(ctx); err != nil && ctx.Err() == nil {
			h.log.Warn("host: tick failed", "error", config.RedactString(err.Error()))
		}
		wait := time.Until(start.Add(h.interval))
		if wait < 0 {
			wait = 0
		}
		if sleepCtx(ctx, wait) {
			return
		}
	}
}

func (h *HostRunner) RunJanitor(ctx stdctx.Context) error {
	p := h.prune.policy(h.now().UTC())
	_, err := h.store.PruneHost(ctx, p)
	return err
}

func (h *HostRunner) janitorIntervalIsOff() bool {
	return h.janitorInterval <= 0 || (h.prune == HostPruneConfig{})
}

func (c HostPruneConfig) policy(now time.Time) store.HostPrunePolicy {
	var p store.HostPrunePolicy
	if c.ClosedSessionTTL > 0 {
		p.ClosedSessionsBefore = now.Add(-c.ClosedSessionTTL)
	}
	if c.CompletedJobTTL > 0 {
		p.CompletedJobsBefore = now.Add(-c.CompletedJobTTL)
	}
	if c.FinishedAttemptTTL > 0 {
		p.FinishedAttemptsBefore = now.Add(-c.FinishedAttemptTTL)
	}
	if c.InactiveWorkspaceTTL > 0 {
		p.InactiveWorkspacesBefore = now.Add(-c.InactiveWorkspaceTTL)
	}
	if c.PollCursorTTL > 0 {
		p.PollCursorsBefore = now.Add(-c.PollCursorTTL)
	}
	return p
}

func (h *HostRunner) Tick(ctx stdctx.Context) error {
	reposByID, reposBySlug, err := h.reconcileRepos(ctx)
	if err != nil {
		return err
	}
	for _, repo := range h.repos {
		if !repo.Enabled || !repo.Poll {
			continue
		}
		rec, ok := reposBySlug[repo.Slug]
		if !ok {
			continue
		}
		if err := h.pollRepo(ctx, repo, rec.ID); err != nil {
			h.log.Warn("host: repo poll failed", "repo", repo.Slug, "error", config.RedactString(err.Error()))
		}
	}
	return h.claimReady(ctx, reposByID)
}

func (h *HostRunner) reconcileRepos(ctx stdctx.Context) (map[int64]HostRepoConfig, map[string]store.HostRepo, error) {
	byID := map[int64]HostRepoConfig{}
	bySlug := map[string]store.HostRepo{}
	for _, r := range h.repos {
		if !r.Enabled {
			continue
		}
		rec, err := h.store.ReconcileHostRepo(ctx, store.HostRepoInput{
			Name:          r.Name,
			Owner:         r.Owner,
			Repo:          r.Repo,
			Slug:          r.Slug,
			GitURL:        r.GitURL,
			DefaultBranch: r.DefaultBranch,
			GithubAccount: r.GithubAccount,
			Enabled:       r.Enabled,
			Poll:          r.Poll,
			ConfigHash:    r.ConfigHash,
		})
		if err != nil {
			return nil, nil, err
		}
		byID[rec.ID] = r
		bySlug[r.Slug] = rec
	}
	return byID, bySlug, nil
}

func (h *HostRunner) pollRepo(ctx stdctx.Context, repo HostRepoConfig, repoID int64) error {
	token, err := h.tokens[repo.GithubAccount].Token(ctx)
	if err != nil {
		return err
	}
	getter := h.newGetter(token)
	prs, err := h.listOpenPRs(ctx, getter, repo)
	if err != nil {
		return err
	}
	now := h.now().UTC()
	if err := h.store.UpsertHostPollCursor(ctx, store.HostPollCursorInput{RepoID: repoID, Source: string(sourcePulls), Cursor: now.Format(time.RFC3339Nano), LastPolledAt: now}); err != nil {
		return err
	}
	for _, pr := range prs {
		number := int64(pr.GetNumber())
		head := pr.GetHead().GetSHA()
		if number <= 0 || head == "" {
			continue
		}
		session, err := h.store.UpsertHostPRSession(ctx, store.HostPRSessionInput{
			RepoID:  repoID,
			Number:  number,
			State:   "open",
			HeadSHA: head,
			BaseSHA: pr.GetBase().GetSHA(),
			Branch:  pr.GetHead().GetRef(),
			Title:   pr.GetTitle(),
		})
		if err != nil {
			return err
		}
		_, _, err = h.store.EnqueueHostJob(ctx, store.HostJobInput{
			RepoID:     repoID,
			SessionID:  session.ID,
			Number:     number,
			HeadSHA:    head,
			BaseSHA:    pr.GetBase().GetSHA(),
			PolicyHash: repo.PolicyHash,
			PromptHash: repo.PromptHash,
			RulesHash:  repo.RulesHash,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (h *HostRunner) listOpenPRs(ctx stdctx.Context, getter notifGetter, repo HostRepoConfig) ([]*github.PullRequest, error) {
	opts := &github.PullRequestListOptions{State: "open", ListOptions: github.ListOptions{PerPage: 100}}
	var out []*github.PullRequest
	for {
		prs, resp, err := getter.ListOpenPRs(ctx, repo.Owner, repo.Repo, opts)
		if err != nil {
			return nil, err
		}
		out = append(out, prs...)
		if resp == nil || resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

func (h *HostRunner) claimReady(ctx stdctx.Context, repos map[int64]HostRepoConfig) error {
	for {
		claim, ok, err := h.store.ClaimHostJob(ctx, store.HostJobClaimInput{WorkerID: h.workerID, Now: h.now().UTC(), LeaseDuration: h.reviewTO + time.Minute})
		if err != nil || !ok {
			return err
		}
		repo, ok := repos[claim.Job.RepoID]
		if !ok {
			_ = h.store.CompleteHostJob(ctx, store.HostJobCompleteInput{JobID: claim.Job.ID, AttemptID: claim.AttemptID, Status: "failed", Error: "repo config not loaded", Now: h.now().UTC()})
			continue
		}
		token, err := h.tokens[repo.GithubAccount].Token(ctx)
		if err != nil {
			_ = h.store.CompleteHostJob(ctx, store.HostJobCompleteInput{JobID: claim.Job.ID, AttemptID: claim.AttemptID, Status: "failed", Error: config.RedactString(err.Error()), Now: h.now().UTC()})
			continue
		}
		review := repo.Review
		timeout := repo.ReviewTimeout
		if timeout <= 0 {
			timeout = h.reviewTO
		}
		jobID, attemptID := claim.Job.ID, claim.AttemptID
		job := Job{
			Key:     prKey{Owner: repo.Owner, Repo: repo.Repo, Number: int(claim.Job.Number)},
			Ref:     fmt.Sprintf("%s/%s#%d", repo.Owner, repo.Repo, claim.Job.Number),
			Token:   token,
			Timeout: timeout,
			Review:  &review,
			OnDone: func(runErr error) {
				status := "done"
				msg := ""
				if runErr != nil {
					status = "failed"
					msg = config.RedactString(runErr.Error())
				}
				cctx, cancel := stdctx.WithTimeout(stdctx.Background(), 10*time.Second)
				defer cancel()
				_ = h.store.CompleteHostJob(cctx, store.HostJobCompleteInput{JobID: jobID, AttemptID: attemptID, Status: status, Error: msg, Now: h.now().UTC()})
			},
		}
		if !h.disp.Submit(job) {
			_ = h.store.CompleteHostJob(ctx, store.HostJobCompleteInput{JobID: jobID, AttemptID: attemptID, Status: "failed", Error: "dispatcher rejected job", Now: h.now().UTC()})
		}
	}
}

func RunHost(ctx stdctx.Context, pool *Pool, runner *HostRunner) error {
	done := make(chan struct{})
	go func() {
		runner.Run(ctx)
		close(done)
	}()
	<-ctx.Done()
	select {
	case <-done:
	case <-time.After(drainGrace):
	}
	if pool != nil {
		pool.Drain()
	}
	return nil
}

func HashJSON(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
