package serve

import (
	stdctx "context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/google/go-github/v84/github"

	"github.com/vanducng/miu-cr/internal/config"
)

// configDir is a seam (defaults to config.Dir) so cursor path resolution is
// testable without touching the real home dir.
var configDir = config.Dir

// PollSource is the internal trigger source selected by serve wiring.
type PollSource string

type pollSource = PollSource

const (
	sourceNotifications pollSource = "notifications"
	sourcePulls         pollSource = "pulls"
	// SourcePulls is host mode's supported cold-start-complete source.
	SourcePulls = sourcePulls
)

// ParsePollSource maps a CLI string to a pollSource; anything but "pulls"
// (including "") defaults to notifications.
func ParsePollSource(s string) pollSource {
	if s == "pulls" {
		return sourcePulls
	}
	return sourceNotifications
}

// backoffCap bounds the exponential transient-error backoff.
const backoffCap = 15 * time.Minute

// subjectURLRe extracts owner/repo/number from a notification subject URL of the
// form .../repos/{owner}/{repo}/pulls/{number}.
var subjectURLRe = regexp.MustCompile(`/repos/([^/]+)/([^/]+)/pulls/(\d+)`)

// notifGetter is the NARROW serve-local GitHub surface the poller needs. It is
// deliberately NOT the shared github.Client (which has 3 fakes that would break
// if widened), the ghNotifGetter adapter wraps *github.Client directly and unit
// tests fake this interface.
type notifGetter interface {
	ListNotifications(ctx stdctx.Context, opts *github.NotificationListOptions) ([]*github.Notification, *github.Response, error)
	ListOpenPRs(ctx stdctx.Context, owner, repo string, opts *github.PullRequestListOptions) ([]*github.PullRequest, *github.Response, error)
	GetPR(ctx stdctx.Context, owner, repo string, number int) (*github.PullRequest, *github.Response, error)
}

// ghNotifGetter adapts *github.Client to notifGetter, calling go-github's
// Activity/PullRequests services directly so the shared Client interface stays
// untouched.
type ghNotifGetter struct{ c *github.Client }

// NewNotifGetter builds the real notifGetter from a GitHub PAT (anonymous when
// empty). It is the production adapter wired in P2.
func NewNotifGetter(token string) notifGetter {
	c := github.NewClient(&http.Client{Timeout: 30 * time.Second})
	if token != "" {
		c = c.WithAuthToken(token)
	}
	return ghNotifGetter{c: c}
}

func (g ghNotifGetter) ListNotifications(ctx stdctx.Context, opts *github.NotificationListOptions) ([]*github.Notification, *github.Response, error) {
	return g.c.Activity.ListNotifications(ctx, opts)
}

func (g ghNotifGetter) ListOpenPRs(ctx stdctx.Context, owner, repo string, opts *github.PullRequestListOptions) ([]*github.PullRequest, *github.Response, error) {
	return g.c.PullRequests.List(ctx, owner, repo, opts)
}

func (g ghNotifGetter) GetPR(ctx stdctx.Context, owner, repo string, number int) (*github.PullRequest, *github.Response, error) {
	return g.c.PullRequests.Get(ctx, owner, repo, number)
}

// PollConfig carries the resolved poll-mode options. resolveToken yields the PAT
// per tick (in-memory only; never persisted/logged).
type PollConfig struct {
	Source       pollSource
	Repos        []string
	Interval     time.Duration
	ResolveToken func() (string, error)
	Dispatcher   Dispatcher
	Logger       *slog.Logger
	ReviewTO     time.Duration
}

// Poller is the poll-mode trigger: a ticker loop that enumerates candidates,
// dedups per head SHA via a restart-safe cursor, and dispatches NEW/UPDATED PRs
// to the same Dispatcher (the serve Pool in P2). It is trigger-only, it never
// touches the review/publish engine and never Drains the pool.
type Poller struct {
	src          pollSource
	gh           notifGetter
	disp         Dispatcher
	allow        repoAllowlist
	interval     time.Duration
	resolveToken func() (string, error)
	log          *slog.Logger
	reviewTO     time.Duration

	mu     sync.Mutex // guards cursor; the Pool may run OnDone on a worker goroutine
	cursor *Cursor
}

// NewPoller builds a Poller. The cursor is loaded from disk (tolerant of a
// missing/corrupt file). gh defaults to the real adapter via the resolved token;
// tests inject a fake gh by setting it directly with newPoller.
func NewPoller(cfg PollConfig, gh notifGetter) *Poller {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	src := cfg.Source
	if src == "" {
		src = sourceNotifications
	}
	var cur *Cursor
	if path, err := cursorPath(); err == nil {
		cur = loadCursor(path, log)
	} else {
		log.Warn("poll cursor: path unresolvable, in-memory only", "error", err.Error())
		cur = newCursor()
	}
	return &Poller{
		src:          src,
		gh:           gh,
		disp:         cfg.Dispatcher,
		allow:        newRepoAllowlist(cfg.Repos),
		interval:     cfg.Interval,
		resolveToken: cfg.ResolveToken,
		log:          log,
		reviewTO:     cfg.ReviewTO,
		cursor:       cur,
	}
}

// candidate is one enumerated PR to consider this tick. headSHA is set when the
// pulls source already carries it (no GetPR needed); notifUpdated is the
// notification updated_at for the pre-GetPR dedup ("" for the pulls source).
type candidate struct {
	owner        string
	repo         string
	number       int
	headSHA      string
	notifUpdated string
}

func (c candidate) ref() string { return fmt.Sprintf("%s/%s#%d", c.owner, c.repo, c.number) }

// Run drives the ticker loop until ctx is cancelled. It NEVER Drains the pool
// (the webhook Server or RunPoll owns the single Drain). The effective interval
// is max(configured, X-Poll-Interval); transient errors back off without
// advancing the cursor or re-reviewing.
func (p *Poller) Run(ctx stdctx.Context) {
	backoff := time.Duration(0)
	for {
		tickStart := time.Now()
		wait, err := p.tick(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			backoff = p.handleErr(ctx, err, backoff)
			if backoff == 0 { // slept already (rate limit), loop immediately
				if ctx.Err() != nil { // unless the rate-limit sleep was cancelled (shutdown)
					return
				}
				continue
			}
			if sleepCtx(ctx, backoff) {
				return
			}
			continue
		}
		backoff = 0
		eff := p.interval
		if wait > eff {
			eff = wait
		}
		next := time.Until(tickStart.Add(eff))
		if next < 0 {
			next = 0
		}
		if sleepCtx(ctx, next) {
			return
		}
	}
}

// tick runs one poll cycle: enumerate, filter, dedup, dispatch, advance Since.
// It returns the X-Poll-Interval the server asked for (0 if none) and any error.
func (p *Poller) tick(ctx stdctx.Context) (time.Duration, error) {
	token, err := p.resolveToken()
	if err != nil {
		p.log.Error("poll: token resolution failed, skipping tick",
			"error", config.RedactString(err.Error()))
		return 0, nil // skip the tick (no submit/advance), not a backoff error
	}

	tickStart := time.Now()
	cands, wait, err := p.enumerate(ctx)
	if err != nil {
		return wait, err
	}

	for _, c := range cands {
		p.dispatchCandidate(ctx, c, token)
	}

	p.mu.Lock()
	p.cursor.Since = tickStart
	p.cursor.prune(time.Now())
	if path, perr := cursorPath(); perr == nil {
		if serr := p.cursor.save(path); serr != nil {
			p.log.Warn("poll cursor: save failed", "error", serr.Error())
		}
	}
	p.mu.Unlock()
	return wait, nil
}

// dispatchCandidate applies the per-candidate dedup + dispatch. For the
// notifications source it does the pre-GetPR updated_at guard and one GetPR to
// resolve the head SHA; the pulls source already carries the head SHA.
func (p *Poller) dispatchCandidate(ctx stdctx.Context, c candidate, token string) {
	ref := c.ref()
	if c.notifUpdated != "" {
		p.mu.Lock()
		unchanged := p.cursor.NotifSeen[ref] == c.notifUpdated
		p.mu.Unlock()
		if unchanged {
			return // pre-GetPR dedup: nothing new since last seen
		}
	}

	sha := c.headSHA
	if sha == "" {
		pr, _, err := p.gh.GetPR(ctx, c.owner, c.repo, c.number)
		if err != nil {
			p.log.Warn("poll: GetPR failed, skipping candidate",
				"repo", c.owner+"/"+c.repo, "number", c.number,
				"error", config.RedactString(err.Error()))
			return
		}
		sha = pr.GetHead().GetSHA()
	}
	if sha == "" {
		return
	}
	p.mu.Lock()
	already := p.cursor.Seen[ref] == sha
	p.mu.Unlock()
	if already {
		return
	}

	pk := prKey{Owner: c.owner, Repo: c.repo, Number: c.number}
	job := Job{
		Key:     pk,
		Ref:     pk.String(),
		Token:   token,
		Timeout: p.reviewTO,
		OnDone: func(err error) {
			if err != nil {
				return // failed review stays retryable next tick (NotifSeen left unrecorded too)
			}
			p.mu.Lock()
			p.cursor.recordSeen(ref, sha)
			if c.notifUpdated != "" {
				// success-gate the pre-GetPR dedup cursor too, so a failed review with an
				// unchanged updated_at is not skipped by the NotifSeen guard next tick.
				p.cursor.recordNotif(ref, c.notifUpdated)
			}
			p.mu.Unlock()
		},
	}
	// Submit==false leaves Seen/NotifSeen unrecorded on purpose: the head is
	// re-enumerated and retried next tick (no cursor advance for this candidate).
	if !p.disp.Submit(job) {
		p.log.Warn("poll: job not enqueued (coalesced/full); retry next tick",
			"repo", c.owner+"/"+c.repo, "number", c.number)
		return
	}
	p.log.Info("poll: review dispatched", "repo", c.owner+"/"+c.repo, "number", c.number)
}

// enumerate returns this tick's candidates plus the server's requested
// X-Poll-Interval (0 if none), per the configured source.
func (p *Poller) enumerate(ctx stdctx.Context) ([]candidate, time.Duration, error) {
	if p.src == sourcePulls {
		return p.enumeratePulls(ctx)
	}
	return p.enumerateNotifications(ctx)
}

func (p *Poller) enumerateNotifications(ctx stdctx.Context) ([]candidate, time.Duration, error) {
	p.mu.Lock()
	since := p.cursor.Since
	p.mu.Unlock()
	opts := &github.NotificationListOptions{Since: since}
	notifs, resp, err := p.gh.ListNotifications(ctx, opts)
	if err != nil {
		return nil, pollIntervalOf(resp), err
	}
	var cands []candidate
	for _, n := range notifs {
		if n.GetSubject().GetType() != "PullRequest" {
			continue
		}
		owner, repo, number, ok := parsePRSubject(n)
		if !ok {
			continue
		}
		if !p.allow.allows(owner, repo) {
			continue
		}
		cands = append(cands, candidate{
			owner:        owner,
			repo:         repo,
			number:       number,
			notifUpdated: n.GetUpdatedAt().Time.UTC().Format(time.RFC3339),
		})
	}
	return cands, pollIntervalOf(resp), nil
}

func (p *Poller) enumeratePulls(ctx stdctx.Context) ([]candidate, time.Duration, error) {
	var cands []candidate
	var lastWait time.Duration
	for _, r := range p.allow.sorted() { // deterministic order; one repo's failure must not abort the tick
		opts := &github.PullRequestListOptions{State: "open"}
		prs, resp, err := p.gh.ListOpenPRs(ctx, r.Owner, r.Repo, opts)
		if w := pollIntervalOf(resp); w > lastWait {
			lastWait = w
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, lastWait, err // propagate cancellation; stop the tick
			}
			p.log.Warn("poll: ListOpenPRs failed, skipping repo",
				"repo", r.Owner+"/"+r.Repo, "error", config.RedactString(err.Error()))
			continue
		}
		for _, pr := range prs {
			cands = append(cands, candidate{
				owner:   r.Owner,
				repo:    r.Repo,
				number:  pr.GetNumber(),
				headSHA: pr.GetHead().GetSHA(),
			})
		}
	}
	return cands, lastWait, nil
}

// parsePRSubject derives owner/repo/number from a Notification: owner/repo from
// GetRepository(), number by regex on the subject URL.
func parsePRSubject(n *github.Notification) (owner, repo string, number int, ok bool) {
	m := subjectURLRe.FindStringSubmatch(n.GetSubject().GetURL())
	if m == nil {
		return "", "", 0, false
	}
	num, err := strconv.Atoi(m[3])
	if err != nil || num <= 0 {
		return "", "", 0, false
	}
	owner = m[1]
	repo = m[2]
	if r := n.GetRepository(); r != nil && r.GetOwner().GetLogin() != "" && r.GetName() != "" {
		owner = r.GetOwner().GetLogin()
		repo = r.GetName()
	}
	return owner, repo, num, true
}

// pollIntervalOf reads the X-Poll-Interval header (seconds) off a response.
func pollIntervalOf(resp *github.Response) time.Duration {
	if resp == nil || resp.Response == nil {
		return 0
	}
	v := resp.Header.Get("X-Poll-Interval")
	if v == "" {
		return 0
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// handleErr applies the backoff policy. A *RateLimitError sleeps until Reset and
// an *AbuseRateLimitError honors RetryAfter (both return 0 = slept already);
// other transients grow exp backoff + jitter (cap backoffCap). The cursor is
// NEVER advanced here.
func (p *Poller) handleErr(ctx stdctx.Context, err error, prev time.Duration) time.Duration {
	var rl *github.RateLimitError
	if errors.As(err, &rl) {
		d := time.Until(rl.Rate.Reset.Time)
		if d < 0 {
			d = 0
		}
		p.log.Warn("poll: rate limited, sleeping until reset", "seconds", int(d.Seconds()))
		sleepCtx(ctx, d)
		return 0
	}
	var abuse *github.AbuseRateLimitError
	if errors.As(err, &abuse) {
		d := abuse.GetRetryAfter()
		if d < 0 {
			d = 0
		}
		p.log.Warn("poll: abuse rate limit, honoring retry-after", "seconds", int(d.Seconds()))
		sleepCtx(ctx, d)
		return 0
	}
	next := prev * 2
	if next == 0 {
		next = time.Second
	}
	if next > backoffCap {
		next = backoffCap
	}
	jitter := time.Duration(rand.Int64N(int64(next)/2 + 1))
	p.log.Warn("poll: transient error, backing off",
		"backoff", (next + jitter).String(), "error", config.RedactString(err.Error()))
	return next + jitter
}

// sleepCtx sleeps for d or until ctx is done; it returns true if ctx was
// cancelled (the caller should stop the loop).
func sleepCtx(ctx stdctx.Context, d time.Duration) bool {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return true
		default:
			return false
		}
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-t.C:
		return false
	}
}
