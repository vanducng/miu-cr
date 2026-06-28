package serve

import (
	stdctx "context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v84/github"

	"github.com/vanducng/miu-cr/internal/config"
	mgithub "github.com/vanducng/miu-cr/internal/github"
	"github.com/vanducng/miu-cr/internal/store"
)

const maxHostClaimsPerTick = 32
const defaultHostJobLeaseDuration = 2 * time.Minute
const hostJobHeartbeatInterval = 30 * time.Second
const hostFailedRetryBase = 5 * time.Minute
const hostFailedRetryCap = time.Hour
const defaultThreadResolutionSyncInterval = 5 * time.Minute
const maxThreadResolutionSyncWorkers = 2

var runHostDrainGrace = 10 * time.Second
var hostPRFilterRegexCache sync.Map

var ErrHostRunnerStopTimeout = errors.New("host runner did not stop before drain deadline")

type HostTokenSource interface {
	Token(stdctx.Context) (string, error)
}

type HostRunnerConfig struct {
	Store           store.HostStore
	Repos           []HostRepoConfig
	TokenSources    map[string]HostTokenSource
	Reload          HostReloadFunc
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

type HostReloadFunc func(stdctx.Context) (HostReload, error)

type HostReload struct {
	Repos           []HostRepoConfig
	TokenSources    map[string]HostTokenSource
	Interval        time.Duration
	ReviewTO        time.Duration
	Prune           HostPruneConfig
	JanitorInterval time.Duration
}

type HostThreadResolutionSync struct {
	Mode     string
	Interval time.Duration
}

func (c HostThreadResolutionSync) Enabled() bool {
	return c.Mode != "" && c.Mode != "off"
}

type HostRepoConfig struct {
	Name                 string
	Owner                string
	Repo                 string
	Slug                 string
	GitURL               string
	DefaultBranch        string
	GithubAccount        string
	Enabled              bool
	Poll                 bool
	ConfigHash           string
	PolicyHash           string
	PromptHash           string
	RulesHash            string
	ReviewTimeout        time.Duration
	ThreadResolutionSync HostThreadResolutionSync
	Review               JobReviewOptions
	PRFilter             config.HostPRFilter
}

type HostRunner struct {
	mu               sync.Mutex
	store            store.HostStore
	repos            []HostRepoConfig
	tokens           map[string]HostTokenSource
	reload           HostReloadFunc
	src              pollSource
	interval         time.Duration
	disp             Dispatcher
	log              *slog.Logger
	reviewTO         time.Duration
	workerID         string
	newGetter        func(string) notifGetter
	prune            HostPruneConfig
	janitorInterval  time.Duration
	now              func() time.Time
	threadSyncLast   map[string]time.Time
	threadSyncActive int
	threadSyncDone   chan struct{}
	threadSyncStop   bool
}

type hostRunnerSnapshot struct {
	repos           []HostRepoConfig
	tokens          map[string]HostTokenSource
	interval        time.Duration
	reviewTO        time.Duration
	prune           HostPruneConfig
	janitorInterval time.Duration
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
	if err := validateHostRunnerRepos(cfg.Repos, cfg.TokenSources); err != nil {
		return nil, err
	}
	return &HostRunner{
		store:           cfg.Store,
		repos:           cfg.Repos,
		tokens:          cfg.TokenSources,
		reload:          cfg.Reload,
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
		threadSyncLast:  map[string]time.Time{},
	}, nil
}

func validateHostRunnerRepos(repos []HostRepoConfig, tokens map[string]HostTokenSource) error {
	enabledRepos := 0
	for _, r := range repos {
		if !r.Enabled {
			continue
		}
		if tokens[r.GithubAccount] == nil {
			return fmt.Errorf("host: repo %s references unknown GitHub account %q", r.Slug, r.GithubAccount)
		}
		if r.Poll {
			enabledRepos++
		}
	}
	if enabledRepos == 0 {
		return errors.New("host: at least one enabled polling repo is required")
	}
	return nil
}

func (h *HostRunner) Reload(ctx stdctx.Context) error {
	next, err := h.loadReload(ctx)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.applyReloadLocked(next)
	return nil
}

func (h *HostRunner) loadReload(ctx stdctx.Context) (HostReload, error) {
	h.mu.Lock()
	reload := h.reload
	h.mu.Unlock()
	if reload == nil {
		return HostReload{}, nil
	}
	next, err := reload(ctx)
	if err != nil {
		return HostReload{}, err
	}
	if next.Repos == nil && next.TokenSources == nil {
		return next, nil
	}
	if err := validateHostRunnerRepos(next.Repos, next.TokenSources); err != nil {
		return HostReload{}, err
	}
	return next, nil
}

func (h *HostRunner) applyReloadLocked(next HostReload) {
	if next.Repos == nil && next.TokenSources == nil {
		return
	}
	h.repos = next.Repos
	h.tokens = next.TokenSources
	h.interval = next.Interval
	if h.interval <= 0 {
		h.interval = time.Minute
	}
	h.reviewTO = next.ReviewTO
	h.prune = next.Prune
	h.janitorInterval = next.JanitorInterval
}

func (h *HostRunner) snapshotLocked() hostRunnerSnapshot {
	return hostRunnerSnapshot{
		repos:           h.repos,
		tokens:          h.tokens,
		interval:        h.interval,
		reviewTO:        h.reviewTO,
		prune:           h.prune,
		janitorInterval: h.janitorInterval,
	}
}

func (h *HostRunner) snapshot() hostRunnerSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snapshotLocked()
}

func (h *HostRunner) Groups() map[string][]string {
	h.mu.Lock()
	defer h.mu.Unlock()
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
		next, err := h.loadReload(ctx)
		if err != nil && ctx.Err() == nil {
			h.log.Warn("host: config reload failed; keeping previous config", "error", config.RedactString(err.Error()))
		}
		h.mu.Lock()
		oldJanitor := h.janitorInterval
		if err == nil {
			h.applyReloadLocked(next)
		}
		snap := h.snapshotLocked()
		h.mu.Unlock()
		if oldJanitor != snap.janitorInterval {
			nextJanitor = time.Time{}
		}
		if !snap.janitorIntervalIsOff() && !start.Before(nextJanitor) {
			if err := h.runJanitor(ctx, snap); err != nil && ctx.Err() == nil {
				h.log.Warn("host: janitor failed", "error", config.RedactString(err.Error()))
			}
			if ctx.Err() != nil {
				return
			}
			nextJanitor = start.Add(snap.janitorInterval)
		}
		pollFloor, err := h.tick(ctx, snap)
		if err != nil && ctx.Err() == nil {
			h.log.Warn("host: tick failed", "error", config.RedactString(err.Error()))
		}
		if ctx.Err() != nil {
			return
		}
		eff := snap.interval
		if pollFloor > eff {
			eff = pollFloor
		}
		wait := time.Until(start.Add(eff))
		if wait < 0 {
			wait = 0
		}
		if sleepCtx(ctx, wait) {
			return
		}
	}
}

func (h *HostRunner) RunJanitor(ctx stdctx.Context) error {
	return h.runJanitor(ctx, h.snapshot())
}

func (h *HostRunner) runJanitor(ctx stdctx.Context, snap hostRunnerSnapshot) error {
	p := snap.prune.policy(h.now().UTC())
	_, err := h.store.PruneHost(ctx, p)
	return err
}

func (s hostRunnerSnapshot) janitorIntervalIsOff() bool {
	return s.janitorInterval <= 0 || s.prune.isZero()
}

func (c HostPruneConfig) isZero() bool {
	return c.ClosedSessionTTL <= 0 &&
		c.CompletedJobTTL <= 0 &&
		c.FinishedAttemptTTL <= 0 &&
		c.InactiveWorkspaceTTL <= 0 &&
		c.PollCursorTTL <= 0
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
	next, err := h.loadReload(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		h.log.Warn("host: config reload failed; keeping previous config", "error", config.RedactString(err.Error()))
	}
	h.mu.Lock()
	if err == nil {
		h.applyReloadLocked(next)
	}
	snap := h.snapshotLocked()
	h.mu.Unlock()
	_, err = h.tick(ctx, snap)
	return err
}

func (h *HostRunner) tick(ctx stdctx.Context, snap hostRunnerSnapshot) (time.Duration, error) {
	reposByID, reposBySlug, err := h.reconcileRepos(ctx, snap.repos)
	if err != nil {
		return 0, err
	}
	var pollFloor time.Duration
	for _, repo := range snap.repos {
		if !repo.Enabled || !repo.Poll {
			continue
		}
		rec, ok := reposBySlug[repo.Slug]
		if !ok {
			continue
		}
		wait, err := h.pollRepo(ctx, snap, repo, rec.ID)
		if wait > pollFloor {
			pollFloor = wait
		}
		if err != nil {
			h.log.Warn("host: repo poll failed", "repo", repo.Slug, "error", config.RedactString(err.Error()))
		}
	}
	return pollFloor, h.claimReady(ctx, snap, reposByID)
}

func (h *HostRunner) reconcileRepos(ctx stdctx.Context, repos []HostRepoConfig) (map[int64]HostRepoConfig, map[string]store.HostRepo, error) {
	byID := map[int64]HostRepoConfig{}
	bySlug := map[string]store.HostRepo{}
	for _, r := range repos {
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

func (h *HostRunner) pollRepo(ctx stdctx.Context, snap hostRunnerSnapshot, repo HostRepoConfig, repoID int64) (time.Duration, error) {
	token, err := snap.tokens[repo.GithubAccount].Token(ctx)
	if err != nil {
		return 0, err
	}
	getter := h.newGetter(token)
	prs, pollFloor, err := h.listOpenPRs(ctx, getter, repo)
	if err != nil {
		return pollFloor, err
	}
	var threadSyncClient mgithub.Client
	now := h.now().UTC()
	if err := h.store.UpsertHostPollCursor(ctx, store.HostPollCursorInput{RepoID: repoID, Source: string(sourcePulls), Cursor: now.Format(time.RFC3339Nano), LastPolledAt: now}); err != nil {
		return pollFloor, err
	}
	openNumbers := make([]int64, 0, len(prs))
	for _, pr := range prs {
		number := int64(pr.GetNumber())
		head := pr.GetHead().GetSHA()
		if number <= 0 {
			continue
		}
		if decision := hostPRFilterAllows(repo.PRFilter, pr); !decision.Allowed {
			h.log.Debug("host: PR ignored by filter", hostPRFilterLogAttrs(repo, pr, decision)...)
			continue
		}
		if head == "" {
			openNumbers = append(openNumbers, number)
			continue
		}
		openNumbers = append(openNumbers, number)
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
			h.log.Warn("host: failed to upsert PR session", "repo", repo.Slug, "pr", number, "error", config.RedactString(err.Error()))
			continue
		}
		_, _, err = h.store.EnqueueHostJob(ctx, store.HostJobInput{
			RepoID:      repoID,
			SessionID:   session.ID,
			Number:      number,
			HeadSHA:     head,
			BaseSHA:     pr.GetBase().GetSHA(),
			PolicyHash:  repo.PolicyHash,
			PromptHash:  repo.PromptHash,
			RulesHash:   repo.RulesHash,
			AvailableAt: now,
			Now:         now,
		})
		if err != nil {
			h.log.Warn("host: failed to enqueue PR review", "repo", repo.Slug, "pr", number, "error", config.RedactString(err.Error()))
			continue
		}
		if repo.ThreadResolutionSync.Enabled() {
			if threadSyncClient == nil {
				threadSyncClient = mgithub.NewClient(token)
			}
			h.enqueueThreadResolutionSync(ctx, threadSyncClient, repo, pr, now)
		}
	}
	res, err := h.store.ReconcileHostClosedPRs(ctx, store.HostClosedPRsInput{RepoID: repoID, OpenNumbers: openNumbers, Now: now})
	if err != nil {
		h.log.Warn("host: failed to reconcile closed PRs", "repo", repo.Slug, "error", config.RedactString(err.Error()))
	} else if res.SessionsClosed > 0 || res.JobsCanceled > 0 {
		h.log.Info("host: reconciled closed PRs", "repo", repo.Slug, "sessions_closed", res.SessionsClosed, "jobs_canceled", res.JobsCanceled)
	}
	h.pruneThreadResolutionSync(repo.Slug, openNumbers)
	return pollFloor, nil
}

func (h *HostRunner) reserveThreadResolutionSync(slug string, number int64, interval time.Duration, now time.Time) (bool, string) {
	if interval <= 0 {
		interval = defaultThreadResolutionSyncInterval
	}
	key := fmt.Sprintf("%s#%d", slug, number)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.threadSyncStop {
		delete(h.threadSyncLast, key)
		return false, "stopping"
	}
	if last, ok := h.threadSyncLast[key]; ok && now.Sub(last) < interval {
		return false, "throttled"
	}
	if h.threadSyncActive >= maxThreadResolutionSyncWorkers {
		delete(h.threadSyncLast, key)
		return false, "workers_busy"
	}
	h.threadSyncLast[key] = now
	if h.threadSyncActive == 0 {
		h.threadSyncDone = make(chan struct{})
	}
	h.threadSyncActive++
	return true, ""
}

func (h *HostRunner) pruneThreadResolutionSync(slug string, openNumbers []int64) {
	open := make(map[int64]struct{}, len(openNumbers))
	for _, number := range openNumbers {
		open[number] = struct{}{}
	}
	prefix := slug + "#"
	h.mu.Lock()
	defer h.mu.Unlock()
	for key := range h.threadSyncLast {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		number, err := strconv.ParseInt(strings.TrimPrefix(key, prefix), 10, 64)
		if err != nil {
			delete(h.threadSyncLast, key)
			continue
		}
		if _, ok := open[number]; !ok {
			delete(h.threadSyncLast, key)
		}
	}
}

func (h *HostRunner) enqueueThreadResolutionSync(ctx stdctx.Context, client mgithub.Client, repo HostRepoConfig, pr *github.PullRequest, now time.Time) {
	snap := threadResolutionSyncPR{
		Number:   pr.GetNumber(),
		HeadSHA:  pr.GetHead().GetSHA(),
		BaseSHA:  pr.GetBase().GetSHA(),
		HTMLBase: pr.GetBase().GetRepo().GetHTMLURL(),
	}
	if snap.HTMLBase == "" && repo.Slug != "" {
		snap.HTMLBase = "https://github.com/" + repo.Slug
	}
	ok, reason := h.reserveThreadResolutionSync(repo.Slug, int64(snap.Number), repo.ThreadResolutionSync.Interval, now)
	if !ok {
		if reason == "stopping" {
			h.log.Warn("host: conversation resolution sync dropped; host stopping", "repo", repo.Slug, "pr", snap.Number, "head_sha", snap.HeadSHA)
		}
		if reason == "workers_busy" {
			h.log.Warn("host: conversation resolution sync dropped; workers busy", "repo", repo.Slug, "pr", snap.Number, "head_sha", snap.HeadSHA)
		}
		return
	}
	go func() {
		defer h.finishThreadResolutionSync()
		syncCtx, cancel := stdctx.WithTimeout(stdctx.WithoutCancel(ctx), runHostDrainGrace)
		defer cancel()
		h.syncThreadResolution(syncCtx, client, repo, snap, now)
	}()
}

type threadResolutionSyncPR struct {
	Number   int
	HeadSHA  string
	BaseSHA  string
	HTMLBase string
}

func (h *HostRunner) finishThreadResolutionSync() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.threadSyncActive <= 0 {
		return
	}
	h.threadSyncActive--
	if h.threadSyncActive == 0 && h.threadSyncDone != nil {
		close(h.threadSyncDone)
		h.threadSyncDone = nil
	}
}

func (h *HostRunner) syncThreadResolution(ctx stdctx.Context, client mgithub.Client, repo HostRepoConfig, pr threadResolutionSyncPR, now time.Time) {
	info := &mgithub.PRInfo{
		Owner:    repo.Owner,
		Repo:     repo.Repo,
		Number:   pr.Number,
		HeadSHA:  pr.HeadSHA,
		BaseSHA:  pr.BaseSHA,
		HTMLBase: pr.HTMLBase,
	}
	res, err := mgithub.SyncSummaryConversationResolved(ctx, client, info, now)
	attrs := []any{"repo", repo.Slug, "pr", pr.Number, "head_sha", info.HeadSHA, "reason", res.Reason, "resolved", res.Resolved, "reopened", res.Reopened, "entries", res.Entries}
	if err != nil {
		attrs = append(attrs, "error", config.RedactString(err.Error()))
		h.log.Warn("host: conversation resolution sync failed", attrs...)
		return
	}
	if res.Action == mgithub.UpsertEdited {
		h.log.Info("host: conversation resolution sync updated summary", attrs...)
		return
	}
	h.log.Debug("host: conversation resolution sync skipped", attrs...)
}

func hostPRFilterLogAttrs(repo HostRepoConfig, pr *github.PullRequest, decision hostPRFilterDecision) []any {
	attrs := []any{
		"repo", repo.Slug,
		"pr", pr.GetNumber(),
		"title", pr.GetTitle(),
		"draft", pr.GetDraft(),
		"author", pr.GetUser().GetLogin(),
		"author_type", pr.GetUser().GetType(),
		"author_association", pr.GetAuthorAssociation(),
		"base_branch", pr.GetBase().GetRef(),
		"head_branch", pr.GetHead().GetRef(),
		"base_sha", pr.GetBase().GetSHA(),
		"head_sha", pr.GetHead().GetSHA(),
		"labels", githubLabelNames(pr.Labels),
		"requested_reviewers", githubUserLogins(pr.RequestedReviewers),
	}
	attrs = append(attrs, decision.logAttrs()...)
	return attrs
}

type hostPRFilterDecision struct {
	Allowed    bool
	Reason     string
	ReasonCode string
	RuleIndex  int
	RuleAction string
	Rule       config.HostPRFilterRule
}

func (d hostPRFilterDecision) logAttrs() []any {
	attrs := []any{"reason", d.Reason, "reason_code", d.ReasonCode}
	if d.RuleIndex >= 0 {
		attrs = append(attrs, "rule_index", d.RuleIndex, "rule_action", d.RuleAction)
		attrs = appendHostPRFilterRuleAttrs(attrs, d.Rule)
	}
	return attrs
}

func appendHostPRFilterRuleAttrs(attrs []any, rule config.HostPRFilterRule) []any {
	for _, field := range hostPRFilterRuleFields(rule) {
		if len(field.values) > 0 {
			attrs = append(attrs, field.attrKey, field.values)
		}
	}
	return attrs
}

func hostPRFilterAllows(filter config.HostPRFilter, pr *github.PullRequest) hostPRFilterDecision {
	if pr.GetDraft() && (filter.IncludeDrafts == nil || !*filter.IncludeDrafts) {
		return hostPRFilterDecision{Allowed: false, Reason: "draft PR and include_drafts=false", ReasonCode: "draft", RuleIndex: -1}
	}
	allow := filter.DefaultAction != "exclude"
	decision := hostPRFilterDecision{Allowed: allow, Reason: "default_action=exclude", ReasonCode: "default_action_exclude", RuleIndex: -1}
	for i, rule := range filter.Rules {
		if hostPRFilterRuleMatches(rule, pr) {
			allow = rule.Action == "include"
			decision = hostPRFilterDecision{
				Allowed:    allow,
				Reason:     hostPRFilterRuleReason(i, rule),
				ReasonCode: "rule_" + rule.Action,
				RuleIndex:  i,
				RuleAction: rule.Action,
				Rule:       rule,
			}
		}
	}
	if allow {
		return hostPRFilterDecision{Allowed: true, RuleIndex: -1}
	}
	return decision
}

func hostPRFilterRuleMatches(rule config.HostPRFilterRule, pr *github.PullRequest) bool {
	return anyEqualFold(rule.Authors, pr.GetUser().GetLogin()) &&
		anyEqualFold(rule.AuthorTypes, pr.GetUser().GetType()) &&
		anyEqualFold(rule.AuthorAssociations, pr.GetAuthorAssociation()) &&
		anyRegexp(rule.TitleRegexes, pr.GetTitle()) &&
		anyLabel(rule.Labels, pr.Labels) &&
		anyUser(rule.RequestedReviewers, pr.RequestedReviewers) &&
		anyEqualFold(rule.BaseBranches, pr.GetBase().GetRef()) &&
		anyEqualFold(rule.HeadBranches, pr.GetHead().GetRef())
}

func hostPRFilterRuleReason(i int, rule config.HostPRFilterRule) string {
	parts := []string{fmt.Sprintf("rule[%d].%s", i, rule.Action)}
	for _, field := range hostPRFilterRuleFields(rule) {
		if len(field.values) > 0 {
			parts = append(parts, field.reasonKey+"="+strings.Join(field.values, ","))
		}
	}
	return strings.Join(parts, " ")
}

type hostPRFilterRuleField struct {
	reasonKey string
	attrKey   string
	values    []string
}

func hostPRFilterRuleFields(rule config.HostPRFilterRule) []hostPRFilterRuleField {
	return []hostPRFilterRuleField{
		{"authors", "rule_authors", rule.Authors},
		{"author_types", "rule_author_types", rule.AuthorTypes},
		{"author_associations", "rule_author_associations", rule.AuthorAssociations},
		{"title_regexes", "rule_title_regexes", rule.TitleRegexes},
		{"labels", "rule_labels", rule.Labels},
		{"requested_reviewers", "rule_requested_reviewers", rule.RequestedReviewers},
		{"base_branches", "rule_base_branches", rule.BaseBranches},
		{"head_branches", "rule_head_branches", rule.HeadBranches},
	}
}

func anyEqualFold(wants []string, got string) bool {
	if len(wants) == 0 {
		return true
	}
	for _, want := range wants {
		if strings.EqualFold(want, got) {
			return true
		}
	}
	return false
}

func anyRegexp(patterns []string, got string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		re, err := cachedHostPRFilterRegex(pattern)
		if err != nil {
			continue
		}
		if re.MatchString(got) {
			return true
		}
	}
	return false
}

func cachedHostPRFilterRegex(pattern string) (*regexp.Regexp, error) {
	if cached, ok := hostPRFilterRegexCache.Load(pattern); ok {
		return cached.(*regexp.Regexp), nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	actual, _ := hostPRFilterRegexCache.LoadOrStore(pattern, re)
	return actual.(*regexp.Regexp), nil
}

func anyLabel(wants []string, labels []*github.Label) bool {
	if len(wants) == 0 {
		return true
	}
	for _, label := range labels {
		for _, want := range wants {
			if strings.EqualFold(want, label.GetName()) {
				return true
			}
		}
	}
	return false
}

func githubLabelNames(labels []*github.Label) []string {
	out := make([]string, 0, len(labels))
	for _, label := range labels {
		if name := label.GetName(); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func anyUser(wants []string, users []*github.User) bool {
	if len(wants) == 0 {
		return true
	}
	for _, user := range users {
		for _, want := range wants {
			if strings.EqualFold(want, user.GetLogin()) {
				return true
			}
		}
	}
	return false
}

func githubUserLogins(users []*github.User) []string {
	out := make([]string, 0, len(users))
	for _, user := range users {
		if login := user.GetLogin(); login != "" {
			out = append(out, login)
		}
	}
	return out
}

func (h *HostRunner) listOpenPRs(ctx stdctx.Context, getter notifGetter, repo HostRepoConfig) ([]*github.PullRequest, time.Duration, error) {
	opts := &github.PullRequestListOptions{State: "open", ListOptions: github.ListOptions{PerPage: 100}}
	var out []*github.PullRequest
	var pollFloor time.Duration
	for {
		prs, resp, err := getter.ListOpenPRs(ctx, repo.Owner, repo.Repo, opts)
		if wait := pollIntervalOf(resp); wait > pollFloor {
			pollFloor = wait
		}
		if err != nil {
			return nil, pollFloor, err
		}
		out = append(out, prs...)
		if resp == nil || resp.NextPage == 0 {
			return out, pollFloor, nil
		}
		opts.Page = resp.NextPage
	}
}

func (h *HostRunner) claimReady(ctx stdctx.Context, snap hostRunnerSnapshot, repos map[int64]HostRepoConfig) error {
	for claims := 0; claims < maxHostClaimsPerTick; claims++ {
		claimLease := hostJobLeaseDuration(snap.reviewTO)
		claim, ok, err := h.store.ClaimHostJob(ctx, store.HostJobClaimInput{WorkerID: h.workerID, Now: h.now().UTC(), LeaseDuration: claimLease})
		if err != nil || !ok {
			return err
		}
		repo, ok := repos[claim.Job.RepoID]
		if !ok {
			_ = h.store.CompleteHostJob(ctx, store.HostJobCompleteInput{JobID: claim.Job.ID, AttemptID: claim.AttemptID, Status: "failed", Error: "repo config not loaded", Now: h.now().UTC()})
			continue
		}
		token, err := snap.tokens[repo.GithubAccount].Token(ctx)
		if err != nil {
			now := h.now().UTC()
			_ = h.store.CompleteHostJob(ctx, store.HostJobCompleteInput{JobID: claim.Job.ID, AttemptID: claim.AttemptID, Status: "failed", Error: config.RedactString(err.Error()), Now: now, AvailableAt: now.Add(hostFailedRetryDelay(claim.Job.Attempts))})
			continue
		}
		review := repo.Review
		timeout := repo.ReviewTimeout
		if timeout <= 0 {
			timeout = snap.reviewTO
		}
		if claim.Job.Number <= 0 || claim.Job.Number > int64(math.MaxInt) {
			_ = h.store.CompleteHostJob(ctx, store.HostJobCompleteInput{JobID: claim.Job.ID, AttemptID: claim.AttemptID, Status: "failed", Error: "invalid PR number", Now: h.now().UTC()})
			continue
		}
		jobID, attemptID, attempts := claim.Job.ID, claim.AttemptID, claim.Job.Attempts
		heartbeatLease := hostJobLeaseDuration(timeout)
		stopHeartbeat := h.startHostJobHeartbeat(ctx, jobID, attemptID, attempts, claim.Job.HeadSHA, heartbeatLease, fmt.Sprintf("%s/%s#%d", repo.Owner, repo.Repo, claim.Job.Number))
		job := Job{
			Key:           prKey{Owner: repo.Owner, Repo: repo.Repo, Number: int(claim.Job.Number)},
			Ref:           fmt.Sprintf("%s/%s#%d", repo.Owner, repo.Repo, claim.Job.Number),
			Token:         token,
			Timeout:       timeout,
			Review:        &review,
			HeadSHA:       claim.Job.HeadSHA,
			HostJobID:     jobID,
			HostAttemptID: attemptID,
			HostAttempt:   attempts,
			OnDone: func(runErr error) {
				stopHeartbeat()
				status := "done"
				msg := ""
				if runErr != nil {
					status = "failed"
					msg = config.RedactString(runErr.Error())
				}
				cctx, cancel := stdctx.WithTimeout(stdctx.WithoutCancel(ctx), 10*time.Second)
				defer cancel()
				now := h.now().UTC()
				availableAt := now
				if runErr != nil {
					availableAt = now.Add(hostFailedRetryDelay(attempts))
				}
				if err := h.store.CompleteHostJob(cctx, store.HostJobCompleteInput{JobID: jobID, AttemptID: attemptID, Status: status, Error: msg, Now: now, AvailableAt: availableAt}); err != nil {
					h.log.Warn("host: failed to complete job", "job", jobID, "error", config.RedactString(err.Error()))
				}
			},
		}
		switch h.disp.Submit(job) {
		case SubmitQueued:
			h.log.Debug("host: review job submitted", "ref", job.Ref, "job_id", jobID, "attempt_id", attemptID, "attempt", attempts, "head_sha", claim.Job.HeadSHA, "lease_seconds", int(heartbeatLease.Seconds()))
		case SubmitDuplicate:
			stopHeartbeat()
			now := h.now().UTC()
			_ = h.store.ReleaseHostJob(ctx, store.HostJobReleaseInput{JobID: jobID, AttemptID: attemptID, Error: "duplicate review already in flight", Now: now, AvailableAt: now.Add(timeout + time.Minute)})
			continue
		case SubmitCoalesced:
			stopHeartbeat()
			now := h.now().UTC()
			_ = h.store.ReleaseHostJob(ctx, store.HostJobReleaseInput{JobID: jobID, AttemptID: attemptID, Error: "review already in flight for PR", Now: now, AvailableAt: now.Add(snap.interval)})
			continue
		case SubmitFull:
			stopHeartbeat()
			now := h.now().UTC()
			_ = h.store.ReleaseHostJob(ctx, store.HostJobReleaseInput{JobID: jobID, AttemptID: attemptID, Error: "dispatcher rejected job", Now: now, AvailableAt: now.Add(snap.interval)})
			continue
		default:
			stopHeartbeat()
			now := h.now().UTC()
			_ = h.store.ReleaseHostJob(ctx, store.HostJobReleaseInput{JobID: jobID, AttemptID: attemptID, Error: "dispatcher rejected job", Now: now, AvailableAt: now})
			return nil
		}
	}
	return nil
}

func (h *HostRunner) startHostJobHeartbeat(ctx stdctx.Context, jobID, attemptID int64, attempt int, headSHA string, lease time.Duration, ref string) func() {
	if jobID == 0 || attemptID == 0 {
		return func() {}
	}
	if lease <= 0 {
		lease = defaultHostJobLeaseDuration
	}
	hctx, cancel := stdctx.WithCancel(stdctx.WithoutCancel(ctx))
	go func() {
		ticker := time.NewTicker(hostJobHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-hctx.Done():
				return
			case <-ticker.C:
				now := h.now().UTC()
				cctx, ccancel := stdctx.WithTimeout(hctx, 10*time.Second)
				err := h.store.HeartbeatHostJob(cctx, store.HostJobHeartbeatInput{JobID: jobID, AttemptID: attemptID, Now: now, LeaseDuration: lease})
				ccancel()
				if errors.Is(err, stdctx.Canceled) {
					return
				}
				if errors.Is(err, store.ErrHostStaleAttempt) {
					h.log.Debug("host: job heartbeat stopped for stale attempt", "ref", ref, "job_id", jobID, "attempt_id", attemptID, "attempt", attempt, "head_sha", headSHA)
					return
				}
				if err != nil {
					h.log.Warn("host: job heartbeat failed", "ref", ref, "job_id", jobID, "attempt_id", attemptID, "attempt", attempt, "head_sha", headSHA, "error", config.RedactString(err.Error()))
					continue
				}
				h.log.Debug("host: job heartbeat", "ref", ref, "job_id", jobID, "attempt_id", attemptID, "attempt", attempt, "head_sha", headSHA, "lease_until", now.Add(lease).Format(time.RFC3339))
			}
		}
	}()
	return cancel
}

func hostJobLeaseDuration(timeout time.Duration) time.Duration {
	if timeout > 0 && timeout+time.Minute < defaultHostJobLeaseDuration {
		return timeout + time.Minute
	}
	return defaultHostJobLeaseDuration
}

func hostFailedRetryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	shift := attempts - 1
	if shift > 4 {
		shift = 4
	}
	delay := hostFailedRetryBase << shift
	if delay > hostFailedRetryCap {
		return hostFailedRetryCap
	}
	return delay
}

func RunHost(ctx stdctx.Context, pool *Pool, runner *HostRunner) error {
	drain := func() error {
		syncDone := runner.waitThreadResolutionSync(runHostDrainGrace)
		if pool != nil {
			pool.Drain()
		}
		if !syncDone {
			return ErrHostRunnerStopTimeout
		}
		return nil
	}
	done := make(chan struct{})
	go func() {
		runner.Run(ctx)
		close(done)
	}()
	<-ctx.Done()
	select {
	case <-done:
		return drain()
	case <-time.After(runHostDrainGrace):
		if err := drain(); err != nil {
			return err
		}
		select {
		case <-done:
			return nil
		default:
			return ErrHostRunnerStopTimeout
		}
	}
}

func (h *HostRunner) waitThreadResolutionSync(timeout time.Duration) bool {
	h.mu.Lock()
	h.threadSyncStop = true
	active := h.threadSyncActive
	done := h.threadSyncDone
	h.mu.Unlock()
	if active == 0 {
		return true
	}
	if timeout <= 0 || done == nil {
		return false
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	select {
	case <-done:
		return true
	case <-deadline.C:
		return false
	}
}

func HashJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
