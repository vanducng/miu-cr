// Package serve hosts the miucr webhook daemon: an HMAC-verified GitHub webhook
// receiver that dispatches PR reviews to a bounded async worker, reusing the M2
// in-process PR review path (cli.ReviewPRForServe). No engine code lives here and
// no shelling out happens; serve is a thin, security-critical front for M2.
package serve

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// maxBodyBytes caps the webhook request body before HMAC validation to bound
// memory on a network-facing endpoint.
const maxBodyBytes = 5 << 20 // 5MB

// prKey identifies a PR for coalesce. A single daemon serves many repos, so the
// PR number alone is not unique — the owner/repo pair is part of the key.
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
}

// Dispatcher accepts review jobs. Submit returns false when the work could not
// be enqueued (full queue) so the caller can loud-log + count, never silently
// drop. The real bounded pool is P2; tests inject a fake.
type Dispatcher interface {
	Submit(Job) bool
}

// repoAllowlist is the set of owner/repo serve is permitted to review. A forged
// or odd webhook for any other repo is 200-ignored, so the PAT can never be used
// to clone an arbitrary repo (SSRF / cost-abuse guard). An empty allowlist
// denies everything.
type repoAllowlist map[string]struct{}

func newRepoAllowlist(repos []string) repoAllowlist {
	a := make(repoAllowlist, len(repos))
	for _, r := range repos {
		a[r] = struct{}{}
	}
	return a
}

func (a repoAllowlist) allows(owner, repo string) bool {
	_, ok := a[owner+"/"+repo]
	return ok
}

// Server is the webhook daemon seam M8 extends. secret is the []byte HMAC key
// (never logged); resolveToken yields the GitHub PAT per-request (M8 swaps in an
// App authenticator at this one call site).
type Server struct {
	addr         string
	secret       []byte
	allow        repoAllowlist
	resolveToken func() (string, error)
	dispatcher   Dispatcher
	log          *slog.Logger
	reviewTO     time.Duration
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
}

func newServer(cfg Config) *Server {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		addr:         cfg.Addr,
		secret:       cfg.Secret,
		allow:        newRepoAllowlist(cfg.Repos),
		resolveToken: cfg.ResolveToken,
		dispatcher:   cfg.Dispatcher,
		log:          log,
		reviewTO:     cfg.ReviewTimeout,
	}
}

// New builds the production Server: if cfg.Dispatcher is nil it constructs the
// bounded worker Pool with the given reviewFn (the real one calls
// cli.ReviewPRForServe). Returns the Server plus the Pool so Run can Drain it.
func New(cfg Config, reviewFn func(Job)) (*Server, *Pool) {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	cfg.Logger = log
	pool := NewPool(reviewFn, log)
	cfg.Dispatcher = pool
	return newServer(cfg), pool
}

// handler builds the serve mux: /webhook (POST, HMAC) and /healthz (GET).
func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", s.handleWebhook)
	mux.HandleFunc("/healthz", s.handleHealthz)
	return mux
}
