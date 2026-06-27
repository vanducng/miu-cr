// Package serve hosts the miucr webhook daemon: an HMAC-verified GitHub webhook
// receiver that dispatches PR reviews to a bounded async worker, reusing the M2
// in-process PR review path (cli.ReviewPRForServe). No engine code lives here and
// no shelling out happens; serve is a thin, security-critical front for M2.
package serve

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/vanducng/miu-cr/internal/config"
)

// maxBodyBytes caps the webhook request body before HMAC validation to bound
// memory on a network-facing endpoint.
const maxBodyBytes = 5 << 20 // 5MB

// prKey identifies a PR for coalesce. A single daemon serves many repos, so the
// PR number alone is not unique, the owner/repo pair is part of the key.
type prKey struct {
	Owner  string
	Repo   string
	Number int
}

func (k prKey) String() string { return fmt.Sprintf("%s/%s#%d", k.Owner, k.Repo, k.Number) }

// Job is a unit of work handed to the Dispatcher: the PR to review, the ref in
// owner/repo#N form, and the resolved (in-memory-only) GitHub token. The token
// is never logged, never put in any envelope, never persisted.
type Job struct {
	Key     prKey
	Ref     string
	Token   string
	Timeout time.Duration
	Review  *JobReviewOptions
	HeadSHA string
	// ReviewID is the server-generated id of a REST-initiated review (empty on the
	// webhook/poll paths). reviewFn persists the FINAL record under this id; the
	// CLI/webhook/poll paths leave it empty and skip that upsert.
	ReviewID string
	// OnDone, when non-nil, runs after the review returns: nil on success, the
	// reviewFn's error (or a recovered panic) on failure. Additive, the webhook Job leaves it nil so the
	// webhook path is byte-for-byte unchanged; the poller sets it to record its
	// dedup cursor only on review success.
	OnDone func(error)
}

type JobReviewOptions struct {
	Post           bool
	Suggest        bool
	PatchRepair    bool
	ApproveClean   bool
	Force          bool
	Conversation   bool
	Gate           string
	FilterMode     string
	MinSeverity    string
	Mode           string
	Provider       string
	APIKey         string
	BaseURL        string
	AuthToken      string
	Model          string
	OperatorPrompt string
	ExpandWindow   int
	TokenBudget    int
	DeepContext    bool
	ContextHops    int
	Subagents      config.ReviewSubagents
}

// SubmitResult tells callers why a job was not enqueued.
type SubmitResult int

const (
	SubmitQueued SubmitResult = iota
	SubmitClosed
	SubmitFull
	SubmitCoalesced
	SubmitDuplicate
)

func (r SubmitResult) String() string {
	switch r {
	case SubmitQueued:
		return "queued"
	case SubmitClosed:
		return "closed"
	case SubmitFull:
		return "full"
	case SubmitCoalesced:
		return "coalesced"
	case SubmitDuplicate:
		return "duplicate"
	default:
		return "unknown"
	}
}

// Dispatcher accepts review jobs. The real bounded pool is P2; tests inject a fake.
type Dispatcher interface {
	Submit(Job) SubmitResult
}

// repoRef is the structural allowlist key. Comparing {owner,repo} fields (rather
// than a joined "owner/repo" string) removes path-confusion: a malformed entry
// like "a/b/c" can never alias allows("a","b/c") or allows("a/b","c").
type repoRef struct {
	Owner string
	Repo  string
}

// repoAllowlist is the set of owner/repo serve is permitted to review. A forged
// or odd webhook for any other repo is 200-ignored, so the PAT can never be used
// to clone an arbitrary repo (SSRF / cost-abuse guard). An empty allowlist
// denies everything.
type repoAllowlist map[repoRef]struct{}

// newRepoAllowlist parses each entry as exactly "owner/repo". Entries that aren't
// well-formed (missing or extra "/", empty halves) are skipped, they can never
// match a real owner/repo lookup, so the safe default is deny.
func newRepoAllowlist(repos []string) repoAllowlist {
	a := make(repoAllowlist, len(repos))
	for _, r := range repos {
		owner, repo, ok := strings.Cut(r, "/")
		if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
			continue
		}
		a[repoRef{Owner: owner, Repo: repo}] = struct{}{}
	}
	return a
}

func (a repoAllowlist) allows(owner, repo string) bool {
	_, ok := a[repoRef{Owner: owner, Repo: repo}]
	return ok
}

// sorted returns the allowlist's repoRefs in deterministic (owner, repo) order so
// callers that iterate (e.g. the poller's pulls source) produce reproducible logs.
func (a repoAllowlist) sorted() []repoRef {
	out := make([]repoRef, 0, len(a))
	for r := range a {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Owner != out[j].Owner {
			return out[i].Owner < out[j].Owner
		}
		return out[i].Repo < out[j].Repo
	})
	return out
}

// Server is the webhook daemon seam M8 extends. secret is the []byte HMAC key
// (never logged); resolveToken yields the GitHub PAT per-request (M8 swaps in an
// App authenticator at this one call site). apiToken (REST bearer, env-only) and
// reviewStore are set only when the opt-in REST API is enabled; with both unset
// the /v1 routes are not registered. now is a clock seam for stuck-pending tests.
type Server struct {
	addr         string
	secret       []byte
	allow        repoAllowlist
	resolveToken func() (string, error)
	dispatcher   Dispatcher
	log          *slog.Logger
	reviewTO     time.Duration
	apiToken     []byte
	reviewStore  ReviewStore
	now          func() time.Time
}

// Config carries the resolved serve options. secret and the token resolver are
// in-memory only; neither is logged or persisted. Full P2 wiring (the bounded
// pool, graceful shutdown, the serve cobra command) builds on this.
type Config struct {
	Addr          string
	Secret        []byte
	Repos         []string
	ResolveToken  func() (string, error)
	Dispatcher    Dispatcher
	Logger        *slog.Logger
	ReviewTimeout time.Duration
	// APIToken (the REST bearer) and ReviewStore are set only to enable the opt-in
	// REST API; both empty/nil → the /v1 routes are not registered. Now is an
	// optional clock seam (defaults to time.Now) for stuck-pending recovery tests.
	APIToken    []byte
	ReviewStore ReviewStore
	Now         func() time.Time
}

func newServer(cfg Config) *Server {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Server{
		addr:         cfg.Addr,
		secret:       cfg.Secret,
		allow:        newRepoAllowlist(cfg.Repos),
		resolveToken: cfg.ResolveToken,
		dispatcher:   cfg.Dispatcher,
		log:          log,
		reviewTO:     cfg.ReviewTimeout,
		apiToken:     cfg.APIToken,
		reviewStore:  cfg.ReviewStore,
		now:          now,
	}
}

// New builds the production Server. It fails fast on security-critical
// misconfiguration, an empty Secret would make ValidatePayload accept any
// payload, and a nil ResolveToken would panic on the first request, rather than
// degrading silently at runtime. New always builds its own bounded worker Pool
// (the real reviewFn calls cli.ReviewPRForServe); any cfg.Dispatcher is ignored
// (tests inject a fake via the lower-level newServer). Returns the Server plus the
// Pool so Run can Drain it.
func New(cfg Config, reviewFn func(Job) error) (*Server, *Pool, error) {
	if len(cfg.Secret) == 0 {
		return nil, nil, errors.New("serve: webhook secret must be non-empty")
	}
	if cfg.ResolveToken == nil {
		return nil, nil, errors.New("serve: ResolveToken must be set")
	}
	if reviewFn == nil {
		return nil, nil, errors.New("serve: reviewFn must be set")
	}
	// The REST API needs BOTH the bearer and a store. An APIToken without a store
	// would register /v1 routes that fail every request (handler() gates on both),
	// so fail fast with a clear signal instead of serving broken routes.
	if len(cfg.APIToken) > 0 && cfg.ReviewStore == nil {
		return nil, nil, errors.New("serve: APIToken is set but ReviewStore is nil; the REST API requires a store")
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	cfg.Logger = log
	if cfg.Dispatcher != nil {
		log.Warn("serve: cfg.Dispatcher is ignored; New always builds its own pool")
	}
	pool := NewPool(reviewFn, log)
	cfg.Dispatcher = pool
	return newServer(cfg), pool, nil
}

// handler builds the serve mux: /webhook (POST, HMAC) and /healthz (GET). The
// authenticated REST API (POST/GET /v1/reviews) is registered ONLY when the
// opt-in API bearer + store are configured; without them no /v1 route exists, so
// an unconfigured deploy exposes no review-creation surface.
func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", s.handleWebhook)
	mux.HandleFunc("/healthz", s.handleHealthz)
	if len(s.apiToken) > 0 && s.reviewStore != nil {
		mux.Handle("POST /v1/reviews", s.requireAPIAuth(http.HandlerFunc(s.handleCreateReview)))
		mux.Handle("GET /v1/reviews/{id}", s.requireAPIAuth(http.HandlerFunc(s.handleGetReview)))
	}
	return mux
}
