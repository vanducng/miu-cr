// Package wire binds the engine-backed Reviewer into cli. It imports both cli
// and engine/agent (which transitively import cli for CLIError), so it must sit
// above cli in the import graph; cmd/miucr blank-imports it to register.
package wire

import (
	stdctx "context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vanducng/miu-cr/internal/cli"
	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/agent"
	"github.com/vanducng/miu-cr/internal/engine/anchor"
	"github.com/vanducng/miu-cr/internal/engine/diff"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
	mgithub "github.com/vanducng/miu-cr/internal/github"
	"github.com/vanducng/miu-cr/internal/mcpserver"
	"github.com/vanducng/miu-cr/internal/oauth"
	"github.com/vanducng/miu-cr/internal/rules"
	"github.com/vanducng/miu-cr/internal/serve"
	"github.com/vanducng/miu-cr/internal/store"
	"github.com/vanducng/miu-cr/internal/store/postgres"
)

// defaultRulesTokenBudget caps the rendered rules section. Always-on baseline so
// even with no user/repo rules the embedded defaults flow within a bounded slice
// of the prompt.
const defaultRulesTokenBudget = 4096
const reviewErrorSummaryTimeout = 30 * time.Second

// loadRules discovers + trust-tags rules for a review. repoDir is the working
// tree (local) or the PR temp clone (--pr). allowRepo gates the Untrusted repo
// layer: false on fork PRs (attacker-authored). Warnings are non-fatal but are
// logged to stderr (never the JSON envelope or the prompt) so a user can see why
// a rule didn't load (bad YAML, skipped symlink, oversized file, invalid glob).
func loadRules(repoDir string, allowRepo bool) []rules.Rule {
	repoRulesDir := ""
	if repoDir != "" {
		repoRulesDir = filepath.Join(repoDir, ".miu", "cr", "rules")
	}
	loaded, warnings := rules.LoadRules(config.RulesDir(), repoRulesDir, allowRepo)
	for _, w := range warnings {
		slog.Warn(w)
	}
	return loaded
}

// ruleCitations builds the wire-validated stem→citation map from the LOADED
// (fork-dropped) rule set. Every loaded stem is CITED as text; only a repo
// (RepoUntrusted) rule is LINKABLE, with its absolute Path converted to a
// repo-ROOT-relative path via filepath.Rel(repoDir, Path) for the blob URL
// (blobURL anchors at the repo root, so a rule at .miu/cr/rules/go.md must link
// as .miu/cr/rules/go.md, not go.md). A user rule (absolute home path) and a
// built-in (defaults/* virtual path) are NEVER given a path; linking either
// would leak the home dir or point at a non-repo file. A repo rule whose Rel
// fails or escapes the repo (rel starts with "..") is downgraded to cite-only.
func ruleCitations(loaded []rules.Rule, repoDir string) map[string]mgithub.RuleCitation {
	if len(loaded) == 0 {
		return nil
	}
	cites := make(map[string]mgithub.RuleCitation, len(loaded))
	for _, r := range loaded {
		c := mgithub.RuleCitation{}
		if r.Provenance == rules.RepoUntrusted && repoDir != "" {
			if rel, err := filepath.Rel(repoDir, r.Path); err == nil && rel != "" && !strings.HasPrefix(rel, "..") {
				c.RepoRelPath = filepath.ToSlash(rel)
				c.Linkable = true
			}
		}
		cites[r.Stem] = c
	}
	return cites
}

func init() {
	engine.SetAnchorer(anchor.ResolveLineNumbers)
	engine.SetCleanReplacement(mgithub.ClassifyReplacement)
	cli.SetReviewer(engineReviewer{})
	cli.SetPRReviewer(prReviewer{})
	cli.SetMCPServer(mcpServerImpl{})
	cli.SetReviewStoreFactory(openReviewStore)
	cli.SetHostStoreFactory(openHostStore)
	cli.SetHistoryStoreFactory(openHistoryStoreForCmd)
}

// openHistoryStoreForCmd opens the configured backend store for the `history`
// command group, reusing the same backend selection as every other store path.
func openHistoryStoreForCmd(ctx stdctx.Context) (store.Store, func(), error) {
	cfg, lerr := config.Load()
	if lerr != nil {
		return nil, nil, lerr
	}
	return openStore(ctx, cfg)
}

// openReviewStore opens the configured backend store for the serve REST API. The
// concrete *Store satisfies serve.ReviewStore (UpsertReview + GetReview). It
// reuses the same backend selection as the engine/PR-thread paths.
func openReviewStore(ctx stdctx.Context) (serve.ReviewStore, func(), error) {
	cfg, lerr := config.Load()
	if lerr != nil {
		return nil, nil, lerr
	}
	s, closeStore, err := openStore(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	return s, closeStore, nil
}

func openHostStore(ctx stdctx.Context, cfg config.HostConfig) (store.HostStore, func(), error) {
	if cfg.Store.Backend != "" && cfg.Store.Backend != "postgres" {
		return nil, nil, &cli.CLIError{
			Code:    "config.invalid",
			Message: "host store backend must be postgres",
			Hint:    "set store.backend: postgres",
			Exit:    2,
		}
	}
	dsn := firstNonEmpty(os.Getenv("MIUCR_PG_DSN"), cfg.Store.DSN)
	if dsn == "" {
		return nil, nil, &cli.CLIError{
			Code:    "config.invalid",
			Message: "host store backend is postgres but no DSN is configured",
			Hint:    "set MIUCR_PG_DSN or host store.dsn",
			Exit:    2,
		}
	}
	s, err := postgres.Open(ctx, dsn)
	if err != nil {
		return nil, nil, err
	}
	return s, func() { _ = s.Close() }, nil
}

// oauthResolver bridges the cli OAuth provider registry + the FS-backed oauth
// package into the agent's resolver hook, keeping the engine/agent resolution
// free of any direct filesystem read. The default OAuth provider is openai (the
// only registered one). Returns nil when no provider is registered.
func oauthResolver() func(stdctx.Context) (agent.OAuthCredential, bool, error) {
	meta, ok := cli.OAuthBackend("openai")
	if !ok {
		return nil
	}
	return func(ctx stdctx.Context) (agent.OAuthCredential, bool, error) {
		res, ok, err := oauth.Credential(ctx, oauth.Meta{
			Provider:       meta.Provider,
			TokenURL:       meta.TokenURL,
			ClientID:       meta.ClientID,
			BackendBaseURL: meta.BackendBaseURL,
		}, nil, nil)
		if err != nil || !ok {
			return agent.OAuthCredential{}, ok, err
		}
		return agent.OAuthCredential{
			AccessToken:    res.AccessToken,
			AccountID:      res.AccountID,
			BackendBaseURL: res.BackendBaseURL,
			Refresh:        res.Refresh,
		}, true, nil
	}
}

// quotaWarn routes the quota gate's one-shot >=80% warning to the review's
// milestone sink when present, else to slog (stderr). The message carries only
// counts/period, never secrets.
func quotaWarn(progress func(string)) func(string) {
	return func(msg string) {
		if progress != nil {
			progress(msg)
		} else {
			slog.Warn(msg)
		}
	}
}

type engineReviewer struct{}

func (engineReviewer) Review(ctx stdctx.Context, req cli.ReviewRequest) (cli.ReviewOutcome, error) {
	creds, err := agent.Resolve(agent.ResolveInput{
		Ctx:           ctx,
		Provider:      req.Provider,
		APIKey:        req.APIKey,
		BaseURL:       req.BaseURL,
		AuthToken:     req.AuthToken,
		Model:         req.Model,
		OAuthResolver: oauthResolver(),
	})
	if err != nil {
		return cli.ReviewOutcome{}, err
	}
	llm, err := agent.New(creds, req.Timeout)
	if err != nil {
		return cli.ReviewOutcome{}, err
	}
	runner := gitcmd.New()
	eng := engine.New(agentAdapter{inner: llm}, runner)

	cfg, lerr := config.Load()
	if lerr != nil {
		slog.Warn("config load failed, using built-in defaults: " + config.RedactString(lerr.Error()))
	}
	hist, closeHist := openHistoryStore(ctx, cfg, req.NoSave)
	if closeHist != nil {
		defer closeHist()
	}
	if hist != nil {
		eng.Store = engineStoreFor(hist)
	}

	gate, closeGate, qerr := buildQuotaGate(ctx, cfg, req.Provider, nil, quotaWarn(req.Progress))
	if qerr != nil {
		return cli.ReviewOutcome{}, qerr
	}
	if closeGate != nil {
		defer closeGate()
	}

	res, err := eng.Review(ctx, engine.Request{
		Mode:            modeFor(req),
		Staged:          req.Staged,
		From:            req.From,
		To:              req.To,
		Commit:          req.Commit,
		Gate:            req.Gate,
		RepoDir:         req.RepoDir,
		IncludeGlobs:    req.IncludeGlobs,
		ExcludeGlobs:    req.ExcludeGlobs,
		Extensions:      req.Extensions,
		ExpandWindow:    req.ExpandWindow,
		TokenBudget:     req.TokenBudget,
		ProjectContext:  req.DeepContext,
		ContextHops:     req.ContextHops,
		ContextHopsAuto: req.ContextHopsAuto,
		Subagents:       engineSubagents(req.Subagents),
		ProviderRetry:   req.ProviderRetry,
		Tools:           req.Tools,
		SymbolContext:   req.SymbolContext,
		Provider:        string(creds.Kind),
		Model:           creds.Model,
		Quota:           gate,

		Rules:            loadRules(req.RepoDir, true),
		RulesFork:        false,
		RulesTokenBudget: defaultRulesTokenBudget,
		WantDiagram:      req.WantDiagram,
		Instruction:      req.Instruction,
		PromptFormat:     req.PromptFormat,
		OperatorPrompt:   req.OperatorPrompt,
		Progress:         req.Progress,
		TraceSink:        req.TraceSink,
		CaptureReasoning: req.CaptureReasoning,
	})
	if err != nil {
		return cli.ReviewOutcome{}, err
	}
	pruneHistory(ctx, hist, cfg)
	return cli.ReviewOutcome{Findings: toCLIFindings(res.Findings), Stats: res.Stats, ReviewID: res.ID}, nil
}

func (engineReviewer) GateFailed(findings []cli.ReviewFinding, gate string) bool {
	return engine.GateFailed(toEngineFindings(findings), gate)
}

// newGitHubClient is the GitHub client constructor seam; tests override it to
// inject a fake client without live network.
var newGitHubClient = mgithub.NewClient

// newPRThreadStore is the opt-in PR-thread store seam. It returns a non-nil store
// (and a closer) ONLY when MIUCR_PR_STORE is set, never on the action/CI path,
// which must stay stateless and byte-for-byte M2/M9. A nil store disables all
// resolution tracking; the caller MUST nil-check. Tests override it to inject a
// temp store.
//
// Backend selection routes through the wire factory. The SQLite path keeps the
// silent nil-degrade (resolution off, review proceeds); it is implicitly
// opt-in. Any non-sqlite backend PROPAGATES its failure: an explicit
// backend=postgres open failure surfaces the typed, redacted store.unavailable,
// and an unknown backend surfaces config.invalid: a user who chose a non-default
// backend must know it failed rather than silently lose resolution tracking.
var newPRThreadStore = func(ctx stdctx.Context, cfg config.Config) (store.PRThreadStore, func(), error) {
	if os.Getenv("MIUCR_PR_STORE") == "" {
		return nil, nil, nil
	}
	prStore, closeStore, err := openPRThreadStore(ctx, cfg)
	if err != nil {
		if resolveBackend(cfg) != "sqlite" {
			return nil, nil, err
		}
		slog.Warn("pr-thread store disabled: " + config.RedactString(err.Error()))
		return nil, nil, nil
	}
	return prStore, closeStore, nil
}

// prReviewer fetches a GitHub PR into a non-shallow temp clone and runs the M1
// engine via ModeRange (zero internal/engine changes). The LLM is still required
// for findings; the GitHub token is optional (anonymous client for public repos).
type prReviewer struct{}

// GateFailed evaluates the gate from the PR review's own findings, so --pr gating
// stays correct regardless of how the local-mode reviewer is wired.
func (prReviewer) GateFailed(findings []cli.ReviewFinding, gate string) bool {
	return engine.GateFailed(toEngineFindings(findings), gate)
}

// wantConversation gates the opt-in PR-conversation fetch: requested AND not a
// fork. Untrusted participant text gains no injection channel on fork PRs,
// mirroring fork-dropped repo rules.
func wantConversation(requested, isFork bool) bool { return requested && !isFork }

// wantProjectContext gates root project files on forks, mirroring conversation.
func wantProjectContext(requested, isFork bool) bool { return requested && !isFork }

func contextHopsForPR(hops int, isFork bool) int {
	if isFork {
		return 0
	}
	return hops
}

func contextHopsAutoForPR(auto, isFork bool) bool { return auto && !isFork }

func (prReviewer) ReviewPR(ctx stdctx.Context, req cli.PRReviewRequest) (cli.ReviewOutcome, error) {
	ref, err := mgithub.ParseRef(req.Ref)
	if err != nil {
		return cli.ReviewOutcome{}, err
	}
	if req.Progress != nil {
		req.Progress(fmt.Sprintf("fetching PR %s/%s#%d…", ref.Owner, ref.Repo, ref.Number))
	}

	client := newGitHubClient(req.Token)
	var info *mgithub.PRInfo
	err = retryTransient(ctx, maxGitHubAttempts, func() error {
		var e error
		info, e = mgithub.FetchPR(ctx, client, ref)
		return e
	})
	if err != nil {
		return cli.ReviewOutcome{}, err
	}

	cfg, lerr := config.Load()
	if lerr != nil {
		slog.Warn("config load failed, using built-in defaults: " + config.RedactString(lerr.Error()))
	}
	// A request-level thinking override (the host sets it per-repo from HostReview;
	// cfg.Review.Thinking is only populated on the standalone CLI path) must flow to
	// BOTH the resolved creds and the cache-reuse fingerprint — set it on cfg so the
	// single source (cfg.Review.Thinking) stays consistent everywhere downstream.
	if strings.TrimSpace(req.Thinking) != "" {
		cfg.Review.Thinking = req.Thinking
	}
	hist, closeHist := openHistoryStore(ctx, cfg, req.NoSave)
	if closeHist != nil {
		defer closeHist()
	}
	reuseKey := reviewReuseKey(req, cfg)

	// Incremental re-review: short-circuit before the clone + LLM pass when the
	// desired end-state already holds and --force was not passed. A store read
	// failure degrades to always-review (skipUnchanged returns ok=false), never
	// blocks. See skipUnchanged.
	if prior, ok := skipUnchanged(ctx, hist, info, req.Force, req.Post, req.Mode, reuseKey); ok {
		rec, haveRec := loadPriorReview(ctx, hist, prior.ID)
		if req.Post && !approvalReuseOK(ctx, client, info, rec, haveRec, req.Approval, req.Gate) {
			if req.Progress != nil {
				req.Progress("same-head review found, but approval is not confirmed; reviewing")
			}
		} else {
			if req.Progress != nil {
				req.Progress("skipped: head SHA " + info.HeadSHA + " already reviewed (use --force to re-review)")
			}
			out := cli.ReviewOutcome{
				ReviewID:         prior.ID,
				SkippedUnchanged: true,
				PriorReviewID:    prior.ID,
				PR: &cli.PRResult{
					Owner: info.Owner, Repo: info.Repo, Number: info.Number,
					HeadSHA: info.HeadSHA, IsFork: info.IsFork, SummaryAction: "none",
				},
			}
			if haveRec {
				out.Findings = toCLIFindings(rec.Findings)
				out.Stats = rec.Stats
			}
			return out, nil
		}
	}

	creds, err := agent.Resolve(agent.ResolveInput{
		Ctx:           ctx,
		Provider:      req.Provider,
		APIKey:        req.APIKey,
		BaseURL:       req.BaseURL,
		AuthToken:     req.AuthToken,
		Model:         req.Model,
		OAuthResolver: oauthResolver(),
	})
	if err != nil {
		return cli.ReviewOutcome{}, err
	}
	llm, err := agent.New(creds, req.Timeout)
	if err != nil {
		return cli.ReviewOutcome{}, err
	}

	runner := gitcmd.New()
	dir, cleanup, err := mgithub.FetchIntoTempClone(ctx, runner, info, req.Token)
	if err != nil {
		return cli.ReviewOutcome{}, err
	}
	defer cleanup()

	// M7 semantic layer (opt-in: [embedding].enabled AND backend=postgres). Built
	// here so the Retriever can be injected into the --pr engine.Request and the
	// embedder reused on the post-publish write path. Off => nils => byte-for-byte M6.
	repo := repoKey(info.Owner, info.Repo)
	emb, embStore, closeEmb := buildSemantic(ctx, cfg)
	if closeEmb != nil {
		defer closeEmb()
	}
	var retr engine.Retriever
	if emb != nil && embStore != nil {
		retr = &retriever{emb: emb, store: embStore, repo: repo}
	}

	eng := engine.New(agentAdapter{inner: llm}, runner)
	if hist != nil {
		eng.Store = engineStoreFor(hist)
	}
	gate, closeGate, qerr := buildQuotaGate(ctx, cfg, firstNonEmpty(req.QuotaProvider, req.Provider), req.Quota, quotaWarn(req.Progress))
	if qerr != nil {
		return cli.ReviewOutcome{}, qerr
	}
	if closeGate != nil {
		defer closeGate()
	}
	// Hoisted so the same loaded (fork-dropped) rule set feeds BOTH the engine
	// (injection) and the publish-layer citation map (validation/linking).
	loaded := loadRules(dir, !info.IsFork)
	// Opt-in conversation context: fetched only with --conversation. Dropped on fork
	// PRs (Untrusted participant text gains no injection channel), mirroring the
	// fork-dropped repo rules above. Best-effort: FetchConversation degrades to "".
	conversation := ""
	if wantConversation(req.Conversation, info.IsFork) {
		conversation = mgithub.FetchConversation(ctx, client, info)
	}
	if req.Post && req.Mode == "review" && !info.IsFork {
		ackPRReviewStarted(ctx, client, info)
	}
	res, err := eng.Review(ctx, engine.Request{
		Mode:            diff.ModeRange,
		From:            info.BaseSHA,
		To:              info.HeadSHA,
		Gate:            req.Gate,
		RepoDir:         dir,
		IncludeGlobs:    req.IncludeGlobs,
		ExcludeGlobs:    req.ExcludeGlobs,
		Extensions:      req.Extensions,
		ExpandWindow:    req.ExpandWindow,
		TokenBudget:     req.TokenBudget,
		ProjectContext:  wantProjectContext(req.DeepContext, info.IsFork),
		ContextHops:     contextHopsForPR(req.ContextHops, info.IsFork),
		ContextHopsAuto: contextHopsAutoForPR(req.ContextHopsAuto, info.IsFork),
		Subagents:       engineSubagents(req.Subagents),
		ProviderRetry:   req.ProviderRetry,
		Tools:           req.Tools,
		SymbolContext:   req.SymbolContext,
		Provider:        string(creds.Kind),
		Model:           creds.Model,
		Quota:           gate,
		Owner:           info.Owner,
		Repo:            info.Repo,
		Number:          info.Number,
		Post:            req.Post,
		PatchRepair:     req.PatchRepair,

		Rules:            loaded,
		RulesFork:        info.IsFork,
		RulesTokenBudget: defaultRulesTokenBudget,
		Retriever:        retr,
		WantDiagram:      req.WantDiagram,
		Instruction:      req.Instruction,
		PromptFormat:     req.PromptFormat,
		OperatorPrompt:   req.OperatorPrompt,
		Conversation:     conversation,
		Progress:         req.Progress,
		TraceSink:        req.TraceSink,
		CaptureReasoning: req.CaptureReasoning,
	})
	if err != nil {
		// Review failed AFTER miucr's internal retries (provider/network/auth). On a
		// --post run, leave a visible error on the PR (upserting miucr's summary
		// comment so a later good run replaces it) instead of failing silently. Fork
		// PRs skip it: the token can't write an issue comment (403). Best-effort.
		//
		// A context.Canceled is NOT a failure: the host cancels an in-flight review
		// when a newer head supersedes it (or on shutdown). See
		// shouldPostReviewErrorSummary.
		if shouldPostReviewErrorSummary(req.Post, info.IsFork, err) {
			if uerr := upsertReviewErrorSummary(ctx, client, info, err); uerr != nil {
				slog.Warn("review-error summary upsert failed: " + config.RedactString(uerr.Error()))
			}
		}
		return cli.ReviewOutcome{}, err
	}
	pruneHistory(ctx, hist, cfg)

	prResult := &cli.PRResult{
		Owner:         info.Owner,
		Repo:          info.Repo,
		Number:        info.Number,
		HeadSHA:       info.HeadSHA,
		IsFork:        info.IsFork,
		SummaryAction: "none",
	}

	if req.Post {
		prStore, closeStore, serr := newPRThreadStore(ctx, cfg)
		if serr != nil {
			return cli.ReviewOutcome{}, serr
		}
		if closeStore != nil {
			defer closeStore()
		}
		ew := embedWriter{emb: emb, store: embStore, repo: repo}
		if err := publishReview(ctx, client, runner, dir, info, res, prResult, req, prStore, ew, cfg.Review.CategoryURLMap(), ruleCitations(loaded, dir), reuseKey); err != nil {
			return cli.ReviewOutcome{}, err
		}
		// Source of truth is the engine stat (repair ran in the engine, not github);
		// omitempty drops it when --patch-repair is OFF.
		prResult.PatchesRepaired = patchRepairedCount(res.Stats)
	}

	return cli.ReviewOutcome{
		Findings: toCLIFindings(res.Findings),
		Stats:    res.Stats,
		PR:       prResult,
		ReviewID: res.ID,
	}, nil
}

// skipUnchanged reports whether the desired PR review state already exists.
func skipUnchanged(ctx stdctx.Context, hist store.Store, info *mgithub.PRInfo, force, post bool, mode, reuseKey string) (store.LatestReview, bool) {
	if force {
		return store.LatestReview{}, false
	}
	if post && mode == "checks" {
		return store.LatestReview{}, false
	}
	if hist == nil {
		return skipFromPublishedMarker(info, post, reuseKey)
	}
	key := store.PRKey{Owner: info.Owner, Repo: info.Repo, Number: info.Number}
	prior, ok, err := hist.LatestReviewForPR(ctx, key)
	if err != nil {
		slog.Warn("incremental re-review check failed, reviewing: " + config.RedactString(err.Error()))
		return store.LatestReview{}, false
	}
	if !ok || prior.HeadSHA == "" || prior.HeadSHA != info.HeadSHA {
		if marker, mok := skipFromPublishedMarker(info, post, reuseKey); mok {
			return marker, true
		}
		return store.LatestReview{}, false
	}
	if post && !priorPublishedAtHead(info, reuseKey) {
		return store.LatestReview{}, false
	}
	return prior, true
}

func skipFromPublishedMarker(info *mgithub.PRInfo, post bool, reuseKey string) (store.LatestReview, bool) {
	if !post || !priorPublishedAtHead(info, reuseKey) {
		return store.LatestReview{}, false
	}
	return store.LatestReview{HeadSHA: info.HeadSHA}, true
}

func priorPublishedAtHead(info *mgithub.PRInfo, reuseKey string) bool {
	if info == nil || info.HeadSHA == "" || info.PriorPublishedHeadSHA == "" || reuseKey == "" || info.PriorPublishedKey != reuseKey {
		return false
	}
	return strings.EqualFold(info.HeadSHA, info.PriorPublishedHeadSHA)
}

type reviewReuseShape struct {
	Version         string
	PatchRepair     bool
	Gate            string
	Provider        string
	BaseURL         string
	Model           string
	IncludeGlobs    []string
	ExcludeGlobs    []string
	Extensions      []string
	ExpandWindow    int
	TokenBudget     int
	DeepContext     bool
	ContextHops     int
	ContextHopsAuto bool
	Subagents       config.ReviewSubagents
	ProviderRetry   config.ProviderRetry
	Tools           config.ReviewTools
	SymbolContext   config.SymbolContext
	FilterMode      string
	MinSeverity     string
	WantDiagram     bool
	Instruction     string
	OperatorPrompt  string
	Conversation    bool
	Mode            string
	Config          reviewReuseConfigShape
}

type reviewReuseConfigShape struct {
	DefaultProvider                 string
	ProviderName                    string
	ProviderKind                    config.Kind
	ProviderBaseURL                 string
	ProviderModel                   string
	ProviderAuth                    string
	ProviderAuthEnv                 string
	ProviderAuthCommand             []string
	ProviderAuthTokenSet            bool
	ProviderAuthTokenFingerprint    string
	ProviderAuthEnvValueFingerprint string
	ReviewTemperature               *float64
	ReviewThinking                  string
	CategoryURLs                    map[string]string
	Env                             reviewReuseEnvShape
	RequestSecretSet                reviewReuseRequestSecretShape
}

type reviewReuseEnvShape struct {
	AnthropicBaseURL              string
	AnthropicModel                string
	OpenAIBaseURL                 string
	OpenAIModel                   string
	MiucrCodexModel               string
	AnthropicAPIKeySet            bool
	AnthropicAPIKeyFingerprint    string
	AnthropicAuthTokenSet         bool
	AnthropicAuthTokenFingerprint string
	OpenAIAPIKeySet               bool
	OpenAIAPIKeyFingerprint       string
}

type reviewReuseRequestSecretShape struct {
	APIKey               bool
	APIKeyFingerprint    string
	AuthToken            bool
	AuthTokenFingerprint string
}

func reviewReuseKey(req cli.PRReviewRequest, cfg config.Config) string {
	providerName := reuseProviderName(req, cfg)
	provider := cfg.Providers[providerName]
	shape := newReviewReuseShape(req, cfg, providerName, provider)
	data, _ := json.Marshal(shape)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:16]
}

func newReviewReuseShape(req cli.PRReviewRequest, cfg config.Config, providerName string, provider config.Provider) reviewReuseShape {
	shape := reviewReuseShape{
		Version:         cli.Version(),
		PatchRepair:     req.PatchRepair,
		Gate:            req.Gate,
		Provider:        req.Provider,
		BaseURL:         req.BaseURL,
		Model:           req.Model,
		IncludeGlobs:    append([]string(nil), req.IncludeGlobs...),
		ExcludeGlobs:    append([]string(nil), req.ExcludeGlobs...),
		Extensions:      append([]string(nil), req.Extensions...),
		ExpandWindow:    req.ExpandWindow,
		TokenBudget:     req.TokenBudget,
		DeepContext:     req.DeepContext,
		ContextHops:     req.ContextHops,
		ContextHopsAuto: req.ContextHopsAuto,
		Subagents:       req.Subagents,
		ProviderRetry:   req.ProviderRetry,
		Tools:           req.Tools,
		SymbolContext:   req.SymbolContext,
		FilterMode:      req.FilterMode,
		MinSeverity:     req.MinSeverity,
		WantDiagram:     req.WantDiagram,
		Instruction:     req.Instruction,
		OperatorPrompt:  req.OperatorPrompt,
		Conversation:    req.Conversation,
		Mode:            req.Mode,
		Config: reviewReuseConfigShape{
			DefaultProvider:                 cfg.DefaultProvider,
			ProviderName:                    providerName,
			ProviderKind:                    provider.Kind,
			ProviderBaseURL:                 provider.BaseURL,
			ProviderModel:                   provider.Model,
			ProviderAuth:                    provider.Auth,
			ProviderAuthEnv:                 provider.AuthEnv,
			ProviderAuthCommand:             append([]string(nil), provider.AuthCommand...),
			ProviderAuthTokenSet:            strings.TrimSpace(provider.AuthToken) != "",
			ProviderAuthTokenFingerprint:    secretReuseFingerprint(provider.AuthToken),
			ProviderAuthEnvValueFingerprint: secretReuseFingerprint(os.Getenv(provider.AuthEnv)),
			ReviewTemperature:               cfg.Review.Temperature,
			ReviewThinking:                  cfg.Review.Thinking,
			CategoryURLs:                    cfg.Review.CategoryURLs,
			Env:                             reviewReuseEnvShapeFromProcess(),
			RequestSecretSet: reviewReuseRequestSecretShape{
				APIKey:               strings.TrimSpace(req.APIKey) != "",
				APIKeyFingerprint:    secretReuseFingerprint(req.APIKey),
				AuthToken:            strings.TrimSpace(req.AuthToken) != "",
				AuthTokenFingerprint: secretReuseFingerprint(req.AuthToken),
			},
		},
	}
	return shape
}

func reviewReuseEnvShapeFromProcess() reviewReuseEnvShape {
	return reviewReuseEnvShape{
		AnthropicBaseURL:              os.Getenv("ANTHROPIC_BASE_URL"),
		AnthropicModel:                os.Getenv("ANTHROPIC_MODEL"),
		OpenAIBaseURL:                 os.Getenv("OPENAI_BASE_URL"),
		OpenAIModel:                   os.Getenv("OPENAI_MODEL"),
		MiucrCodexModel:               os.Getenv("MIUCR_CODEX_MODEL"),
		AnthropicAPIKeySet:            envPresentForReuse("ANTHROPIC_API_KEY"),
		AnthropicAPIKeyFingerprint:    secretReuseFingerprint(os.Getenv("ANTHROPIC_API_KEY")),
		AnthropicAuthTokenSet:         envPresentForReuse("ANTHROPIC_AUTH_TOKEN"),
		AnthropicAuthTokenFingerprint: secretReuseFingerprint(os.Getenv("ANTHROPIC_AUTH_TOKEN")),
		OpenAIAPIKeySet:               envPresentForReuse("OPENAI_API_KEY"),
		OpenAIAPIKeyFingerprint:       secretReuseFingerprint(os.Getenv("OPENAI_API_KEY")),
	}
}

func secretReuseFingerprint(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("miu-cr/reuse-secret\x00" + value))
	return hex.EncodeToString(sum[:])[:16]
}

func reuseProviderName(req cli.PRReviewRequest, cfg config.Config) string {
	if p := strings.ToLower(strings.TrimSpace(req.Provider)); p != "" && p != "auto" {
		return p
	}
	hasAnthropic := strings.TrimSpace(req.APIKey) != "" ||
		strings.TrimSpace(req.AuthToken) != "" ||
		envPresentForReuse("ANTHROPIC_API_KEY") ||
		envPresentForReuse("ANTHROPIC_AUTH_TOKEN")
	if envPresentForReuse("OPENAI_API_KEY") && !hasAnthropic {
		return string(config.KindOpenAI)
	}
	if d := strings.TrimSpace(cfg.DefaultProvider); d != "" && d != "auto" {
		return d
	}
	return string(config.KindAnthropic)
}

func envPresentForReuse(name string) bool {
	return strings.TrimSpace(os.Getenv(name)) != ""
}

func loadPriorReview(ctx stdctx.Context, hist store.Store, id string) (store.ReviewRecord, bool) {
	if hist == nil || id == "" {
		return store.ReviewRecord{}, false
	}
	rec, err := hist.GetReview(ctx, id)
	if err != nil {
		slog.Warn("incremental prior review load failed: " + config.RedactString(err.Error()))
		return store.ReviewRecord{}, false
	}
	return rec, true
}

func approvalReuseOK(ctx stdctx.Context, client mgithub.Client, info *mgithub.PRInfo, rec store.ReviewRecord, haveRec bool, policy config.ApprovalPolicy, gate string) bool {
	findings, reviewedFiles, gateClean := priorReviewShape(info, rec, haveRec, gate)
	if !mgithub.ApprovalWouldApprove(*info, policy, gateClean, findings, reviewedFiles) {
		return true
	}
	approved, err := mgithub.HasApprovedReview(ctx, client, info)
	if err != nil {
		slog.Warn("approval reuse check failed, reviewing: " + config.RedactString(err.Error()))
		return false
	}
	return approved
}

func priorReviewShape(info *mgithub.PRInfo, rec store.ReviewRecord, haveRec bool, gate string) (findings []engine.Finding, reviewedFiles int, gateClean bool) {
	if haveRec {
		return rec.Findings, reviewedFilesFromStats(rec.Stats), !engine.GateFailed(rec.Findings, gate) && !engine.SubagentsDegraded(rec.Stats)
	}
	for _, e := range info.PriorLedger {
		if e.Status != "open" && e.Status != "reopened" {
			continue
		}
		severity := e.Sev
		if severity == "" {
			severity = "critical"
		}
		findings = append(findings, engine.Finding{Severity: severity})
	}
	return findings, len(info.Files), !engine.GateFailed(findings, gate)
}

func ackPRReviewStarted(ctx stdctx.Context, client mgithub.Client, info *mgithub.PRInfo) {
	if err := mgithub.ReactEyes(ctx, client, info); err != nil {
		slog.Warn("review: PR acknowledgement reaction failed",
			"repo", info.Owner+"/"+info.Repo, "pr", info.Number, "head_sha", shortSHA(info.HeadSHA),
			"reaction", "eyes", "error", config.RedactString(err.Error()))
	} else {
		slog.Info("review: PR acknowledgement reaction posted",
			"repo", info.Owner+"/"+info.Repo, "pr", info.Number, "head_sha", shortSHA(info.HeadSHA),
			"reaction", "eyes")
	}
	action, url, err := mgithub.UpsertSummaryComment(ctx, client, info, mgithub.RenderRunningSummary(info, cli.Version()))
	if err != nil {
		slog.Warn("review: running summary upsert failed",
			"repo", info.Owner+"/"+info.Repo, "pr", info.Number, "head_sha", shortSHA(info.HeadSHA),
			"error", config.RedactString(err.Error()))
		return
	}
	slog.Info("review: running summary upserted",
		"repo", info.Owner+"/"+info.Repo, "pr", info.Number, "head_sha", shortSHA(info.HeadSHA),
		"summary_action", string(action), "summary_url", url)
}

func shortSHA(s string) string {
	if len(s) <= 7 {
		return s
	}
	return s[:7]
}

// publishReview upserts the summary comment, posts inline review comments, then
// finalizes the same summary. Normal COMMENT reviews keep the body empty;
// approvals may carry a short note.
func publishReview(ctx stdctx.Context, client mgithub.Client, runner *gitcmd.Runner, dir string, info *mgithub.PRInfo, res engine.ReviewResult, prResult *cli.PRResult, req cli.PRReviewRequest, prStore store.PRThreadStore, ew embedWriter, categoryURLs map[string]string, ruleCites map[string]mgithub.RuleCitation, publishKey string) error {
	diffs, err := mgithub.DiffsForPR(ctx, runner, dir, info.BaseSHA, info.HeadSHA)
	if err != nil {
		return err
	}

	if req.Mode == "checks" {
		return publishChecks(ctx, client, info, res, diffs, prResult, req, ew)
	}
	existing, err := mgithub.ExistingFingerprints(ctx, client, info)
	if err != nil {
		return err
	}
	publishFindings := res.Findings

	// skip is the dedupe set passed to PostReview, built from the keys of `existing`
	// (fp -> inline comment URL). With no store it is exactly the M2/M9
	// ExistingFingerprints; with a store we layer prior 'posted' fps on top and then
	// SUBTRACT recurring resolved fps so a fixed-then-reappearing finding reopens
	// (the lingering marker keeps it in `existing`, so only a set-difference can
	// re-raise it; a union never could).
	skip := make(map[string]bool, len(existing))
	for fp := range existing {
		skip[fp] = true
	}
	prKey := store.PRKey{Owner: info.Owner, Repo: info.Repo, Number: info.Number}
	var prior []store.PRFinding
	if prStore != nil {
		// Best-effort store: a read failure degrades to an EMPTY prior set (no
		// skip/resolution this run) and is logged; it must never abort the review.
		if p, lerr := prStore.ListFindings(ctx, prKey); lerr != nil {
			slog.Warn("pr-thread store read failed, proceeding without prior findings: " + config.RedactString(lerr.Error()))
		} else {
			prior = p
			skip = make(map[string]bool, len(existing)+len(prior))
			for fp := range existing {
				skip[fp] = true
			}
			priorStatus := make(map[string]string, len(prior))
			for _, pf := range prior {
				priorStatus[pf.Fingerprint] = pf.Status
				if pf.Status == "posted" {
					skip[pf.Fingerprint] = true
				}
			}
			for _, f := range res.Findings {
				if priorStatus[mgithub.Fingerprint(f)] == "resolved" {
					delete(skip, mgithub.Fingerprint(f))
				}
			}
		}
	}

	opts := mgithub.PostReviewOptions{
		Suggest:       req.Suggest,
		Approval:      req.Approval,
		Gate:          req.Gate,
		GateClean:     !engine.GateFailed(publishFindings, req.Gate) && !engine.SubagentsDegraded(res.Stats),
		ReviewedFiles: reviewedFilesFromStats(res.Stats),
		FilterMode:    filterModeOf(req.FilterMode),
		MinSeverity:   req.MinSeverity,
		Format:        req.Format,
		CategoryURLs:  categoryURLs,
		RuleCitations: ruleCites,
		// Fork-fallback ::error:: commands must share the envelope's stdout stream
		// (GitHub parses workflow commands only from stdout); the command's writer is
		// threaded in via req. nil → PostReview falls back to os.Stdout.
		ActionsOut: req.ActionsOut,
	}

	// Merge this run's findings into the comment-embedded finding ledger ONCE (the
	// summary may be upserted twice for overflow; merging twice would double-count
	// reopens). MergeLedger returns non-nil even when empty, so the summary always
	// renders in lifecycle mode on the PR path.
	now := time.Now()
	ledger := mgithub.MergeLedger(info.PriorLedger, publishFindings, info.HeadSHA, diffPathSet(diffs), now)

	// renderSummary builds the summary body for a given omitted set. info.ReviewCount
	// is already this run's number (FetchPR did the +1); the body's runs token seeds
	// the next read.
	renderSummary := func(omitted int, omittedFindings []engine.Finding, published bool) string {
		return mgithub.RenderSummaryFull(info, publishFindings, res.Stats, omitted, omittedFindings, categoryURLs, mgithub.SummaryOptions{
			Diffs:               diffs,
			FilterMode:          filterModeOf(req.FilterMode),
			ReviewID:            res.ID,
			Walkthrough:         res.Walkthrough,
			FileSummaries:       res.FileSummaries,
			Diagram:             res.Diagram,
			Version:             cli.Version(),
			RuleCitations:       ruleCites,
			Ledger:              ledger,
			InlineURLs:          existing,
			Published:           published,
			PublishKey:          publishKey,
			Format:              req.Format,
			SuppressWalkthrough: req.SuppressWalkthrough,
			FileChangeSummary:   req.FileChangeSummary,
		})
	}

	// Post the summary FIRST on a non-fork PR (with omitted=0) so it anchors ABOVE the
	// inline review in the timeline (overview, then details); finalized after PostReview.
	// A fork PR defers to after PostReview (an issue comment would 403 like the review).
	summaryFirst := !info.IsFork
	if summaryFirst {
		if action, url, uerr := mgithub.UpsertSummaryComment(ctx, client, info, renderSummary(0, nil, false)); uerr != nil {
			slog.Warn("summary upsert failed, continuing to inline review: " + config.RedactString(uerr.Error()))
			prResult.SummaryAction = "failed"
		} else {
			prResult.SummaryAction = string(action)
			opts.SummaryURL = url
		}
	}

	// nil summaryFn: summary lives in the issue comment. COMMENT reviews keep the
	// review body empty; APPROVE may add a short body.
	pr, err := mgithub.PostReview(ctx, client, info, publishFindings, diffs, nil, skip, opts)
	if err != nil {
		return err
	}
	prResult.Mode = "review"
	prResult.FallbackAnnotations = pr.Fallback
	// Fork-PR 403 fallback fired on inline: findings went to ::error:: annotations. The
	// summary issue comment would 403 the same way (and we skipped it on the fork path).
	if pr.Fallback > 0 {
		prResult.Posted = false
		prResult.SummaryAction = "fork_fallback"
		return nil
	}

	if action, _, uerr := mgithub.UpsertSummaryComment(ctx, client, info, renderSummary(pr.Omitted, pr.OmittedFindings, true)); uerr != nil {
		// Only a successful PostReview earns the reusable publish marker.
		slog.Warn("summary upsert failed (inline review still posted): " + config.RedactString(uerr.Error()))
		prResult.SummaryAction = "failed"
	} else {
		prResult.SummaryAction = string(action)
	}

	if prStore != nil {
		// PostReview already succeeded: the review is live. A store write failure must
		// not discard that outcome: log (redacted), continue.
		if terr := trackResolution(ctx, prStore, prKey, prior, publishFindings, diffs, pr.PostedFindings); terr != nil {
			slog.Warn("pr-thread store tracking failed: " + config.RedactString(terr.Error()))
		}
	}

	// M7 write path (gated by [embedding].enabled+postgres, independent of the
	// PR-thread store): embed the actually-posted findings' scrubbed code anchors.
	// Best-effort, never affects the published review.
	ew.write(ctx, pr.PostedFindings, publishFindings, res.Stats)

	prResult.Posted = true
	prResult.PostedInline = pr.Posted
	prResult.SuggestionsPosted = pr.Suggestions
	prResult.ApproveAction = approveActionFor(pr.Event)
	prResult.ApproveReason = pr.Reason
	return nil
}

// publishChecks is the --mode checks reporter: it posts a GitHub CheckRun with
// annotations from the diff-eligible findings (conclusion from the gate) instead
// of inline comments + a summary. No fingerprint dedupe / summary upsert: a CheckRun
// is replaced wholesale each run by the same name, so re-runs are naturally idempotent.
func publishChecks(ctx stdctx.Context, client mgithub.Client, info *mgithub.PRInfo, res engine.ReviewResult, diffs []diff.Diff, prResult *cli.PRResult, req cli.PRReviewRequest, ew embedWriter) error {
	gateClean := !engine.GateFailed(res.Findings, req.Gate) && !engine.SubagentsDegraded(res.Stats)
	cr, err := mgithub.PostChecks(ctx, client, info, res.Findings, diffs, res.Stats, gateClean, filterModeOf(req.FilterMode))
	if err != nil {
		return err
	}
	// M7 write path: embed the annotated findings' scrubbed code anchors so semantic
	// recall is fed regardless of reporter (review vs checks). Nil-safe + best-effort.
	ew.write(ctx, cr.Posted, res.Findings, res.Stats)

	prResult.Posted = true
	prResult.Mode = "checks"
	prResult.PostedInline = cr.Annotations
	prResult.CheckRunID = cr.CheckRunID
	prResult.CheckConclusion = cr.Conclusion
	prResult.SummaryAction = "none"
	return nil
}

// patchRepairedCount reads the engine's repair stat (set only when --patch-repair
// ran). The repair loop records counts as float64; a missing/mistyped stat → 0.
func patchRepairedCount(stats map[string]any) int {
	pr, ok := stats["patch_repair"].(map[string]any)
	if !ok {
		return 0
	}
	if n, ok := pr["repaired"].(float64); ok {
		return int(n)
	}
	return 0
}

// trackResolution records the actually-submitted findings as posted, then marks as
// resolved any prior 'posted' fp absent from THIS run whose stored path is still in
// the PR diff (a finding off the diff can't be re-posted, so absence isn't a fix).
func trackResolution(ctx stdctx.Context, prStore store.PRThreadStore, key store.PRKey, prior []store.PRFinding, current []engine.Finding, diffs []diff.Diff, posted []mgithub.PostedFinding) error {
	upserts := make([]store.PRFinding, 0, len(posted))
	for _, pf := range posted {
		upserts = append(upserts, store.PRFinding{Fingerprint: pf.Fingerprint, Path: pf.Path, Status: "posted"})
	}
	if err := prStore.UpsertPosted(ctx, key, upserts); err != nil {
		return err
	}

	currentFPs := make(map[string]bool, len(current))
	for _, f := range current {
		currentFPs[mgithub.Fingerprint(f)] = true
	}
	pathsInDiff := diffPathSet(diffs)

	var resolved []string
	for _, pf := range prior {
		if pf.Status == "posted" && !currentFPs[pf.Fingerprint] && pathsInDiff[pf.Path] {
			resolved = append(resolved, pf.Fingerprint)
		}
	}
	return prStore.MarkResolved(ctx, key, resolved)
}

// diffPathSet is the set of new-side paths present in the diff (skipping
// deletions). A finding off this set can't be re-posted, so its absence from a
// run is not treated as a fix by either the ledger merge or trackResolution.
func diffPathSet(diffs []diff.Diff) map[string]bool {
	m := make(map[string]bool, len(diffs))
	for i := range diffs {
		if diffs[i].NewPath != "" && diffs[i].NewPath != "/dev/null" {
			m[diffs[i].NewPath] = true
		}
	}
	return m
}

// filterModeOf maps the request's filter-mode string to the github enum; an empty
// or unrecognized value defaults to diff_context (the validated CLI default).
func filterModeOf(s string) mgithub.FilterMode {
	if mgithub.ValidFilterMode(s) {
		return mgithub.FilterMode(s)
	}
	return mgithub.FilterDiffContext
}

func engineSubagents(in config.ReviewSubagents) engine.SubagentConfig {
	out := engine.SubagentConfig{
		Mode:            in.Mode,
		MaxParallel:     in.MaxParallel,
		MinFiles:        in.MinFiles,
		MinContextBytes: in.MinContextBytes,
		RequireAll:      true,
	}
	if in.RequireAll != nil {
		out.RequireAll = *in.RequireAll
	}
	out.Agents = make([]engine.SubagentSpec, 0, len(in.Agents))
	for _, a := range in.Agents {
		out.Agents = append(out.Agents, engine.SubagentSpec{
			Name:           a.Name,
			IncludeGlobs:   append([]string(nil), a.Include...),
			ExcludeGlobs:   append([]string(nil), a.Exclude...),
			OperatorPrompt: a.SystemPrompt,
		})
	}
	return out
}

// approveActionFor maps the resolved CreateReview Event to the PRResult action
// label (approved|commented).
func approveActionFor(event string) string {
	if event == "APPROVE" {
		return "approved"
	}
	return "commented"
}

// reviewedFilesFromStats reads the engine's files_reviewed stat (a float64) so the
// approve resolver can require ≥1 file actually reviewed.
func reviewedFilesFromStats(stats map[string]any) int {
	if v, ok := stats["files_reviewed"].(float64); ok {
		return int(v)
	}
	return 0
}

// agentAdapter bridges the concrete Anthropic agent (agent.Context) to the
// engine's local Agent interface (engine.AgentContext), keeping engine below
// agent in the import graph.
type agentAdapter struct{ inner agent.Agent }

func (a agentAdapter) Review(ctx stdctx.Context, rc engine.AgentContext) (engine.ReviewOutput, error) {
	return a.inner.Review(ctx, agent.Context{
		Text:             rc.Text,
		Rules:            rc.Rules,           // lockstep: forgetting this silently drops all rules
		SemanticContext:  rc.SemanticContext, // lockstep: forgetting this silently drops M7 advisory
		ProjectContext:   rc.ProjectContext,  // lockstep: forgetting this silently drops deep project context
		RelatedContext:   rc.RelatedContext,  // lockstep: forgetting this silently drops hop-expanded context
		WantDiagram:      rc.WantDiagram,     // lockstep: forgetting this silently drops the diagram opt-in
		Instruction:      rc.Instruction,     // lockstep: forgetting this silently drops the developer steer
		Conversation:     rc.Conversation,    // lockstep: forgetting this silently drops the PR conversation
		PromptFormat:     rc.PromptFormat,    // lockstep: forgetting this silently renders legacy under xml request
		OperatorPrompt:   rc.OperatorPrompt,
		ProviderRetry:    rc.ProviderRetry,
		Tools:            rc.Tools,
		SymbolContext:    rc.SymbolContext,
		RepoDir:          rc.RepoDir,
		Rev:              rc.Rev,
		Runner:           rc.Runner,
		Progress:         rc.Progress,
		Trace:            rc.Trace,
		CaptureReasoning: rc.CaptureReasoning, // lockstep: forgetting this silently drops reasoning capture
	})
}

// RepairPatch forwards the engine's repair request to the concrete agent —
// lockstep: a missed forward silently no-ops repair for the real agent.
func (a agentAdapter) RepairPatch(ctx stdctx.Context, rr engine.RepairRequest) (string, engine.Usage, error) {
	return a.inner.RepairPatch(ctx, agent.RepairRequest{
		Span:          rr.Span,
		Rationale:     rr.Rationale,
		Category:      rr.Category,
		Severity:      rr.Severity,
		ProviderRetry: rr.ProviderRetry,
	})
}

// mcpServerImpl builds the engine + SQLite store and serves them over MCP. The
// agent resolves credentials lazily (on the first review_run), so the MCP
// handshake and tools/list need no Anthropic key.
type mcpServerImpl struct{}

func (mcpServerImpl) Serve(ctx stdctx.Context, req cli.MCPRequest) error {
	runner := gitcmd.New()
	eng := engine.New(&lazyAgent{timeout: req.Timeout}, runner)

	cfg, lerr := config.Load()
	if lerr != nil {
		slog.Warn("config load failed, using built-in defaults: " + config.RedactString(lerr.Error()))
	}
	var st store.Store
	s, closeStore, oerr := openStore(ctx, cfg)
	if oerr != nil {
		// A non-sqlite backend failure is fatal (surface the typed, redacted
		// store.unavailable for postgres, or config.invalid for an unknown backend).
		// The SQLite default degrades silently so the MCP handshake/tools-list still
		// work without a writable state dir.
		if resolveBackend(cfg) != "sqlite" {
			return oerr
		}
		slog.Warn("mcp store disabled: " + config.RedactString(oerr.Error()))
	} else {
		eng.Store = engineStoreFor(s)
		st = s
		defer closeStore()
	}

	err := mcpserver.Serve(ctx, mcpserver.Deps{Engine: eng, Store: st}, mcpserver.Options{
		Transport:             req.Transport,
		ImplementationName:    "miucr",
		ImplementationVersion: req.Version,
		Timeout:               req.Timeout,
	}, req.In, req.Out, req.Err)

	var unsupported *mcpserver.UnsupportedTransportError
	if errors.As(err, &unsupported) {
		return &cli.CLIError{
			Code:    "mcp.unsupported_transport",
			Message: unsupported.Error(),
			Exit:    2,
			Details: map[string]any{"transport": unsupported.Transport},
		}
	}
	return err
}

// lazyAgent resolves Anthropic credentials only when a review actually runs, so
// the MCP server can start (and answer initialize/tools-list) without a key. The
// resolution is memoized (sync.Once), so a review plus its per-finding repair calls
// resolve credentials + build the client exactly once.
type lazyAgent struct {
	timeout time.Duration
	once    sync.Once
	inner   agentAdapter
	err     error
}

func (l *lazyAgent) resolve(ctx stdctx.Context) (agentAdapter, error) {
	l.once.Do(func() {
		creds, err := agent.Resolve(agent.ResolveInput{Ctx: ctx, OAuthResolver: oauthResolver()})
		if err != nil {
			l.err = err
			return
		}
		llm, err := agent.New(creds, l.timeout)
		if err != nil {
			l.err = err
			return
		}
		l.inner = agentAdapter{inner: llm}
	})
	return l.inner, l.err
}

func (l *lazyAgent) Review(ctx stdctx.Context, rc engine.AgentContext) (engine.ReviewOutput, error) {
	a, err := l.resolve(ctx)
	if err != nil {
		return engine.ReviewOutput{}, err
	}
	return a.Review(ctx, rc)
}

// RepairPatch reuses the memoized agent (mirroring Review) — lockstep with the
// engine.Agent interface.
func (l *lazyAgent) RepairPatch(ctx stdctx.Context, rr engine.RepairRequest) (string, engine.Usage, error) {
	a, err := l.resolve(ctx)
	if err != nil {
		return "", engine.Usage{}, err
	}
	return a.RepairPatch(ctx, rr)
}

func modeFor(req cli.ReviewRequest) diff.Mode {
	switch {
	case req.Commit != "":
		return diff.ModeCommit
	case req.From != "" || req.To != "":
		return diff.ModeRange
	default:
		return diff.ModeStaged
	}
}

func toCLIFindings(in []engine.Finding) []cli.ReviewFinding {
	out := make([]cli.ReviewFinding, 0, len(in))
	for _, f := range in {
		out = append(out, cli.ReviewFinding{
			File:           f.File,
			Line:           f.Line,
			EndLine:        f.EndLine,
			Title:          f.Title,
			Rule:           f.Rule,
			Severity:       f.Severity,
			Category:       f.Category,
			Rationale:      f.Rationale,
			SuggestedPatch: f.SuggestedPatch,
			QuotedCode:     f.QuotedCode,
		})
	}
	return out
}

func toEngineFindings(in []cli.ReviewFinding) []engine.Finding {
	out := make([]engine.Finding, 0, len(in))
	for _, f := range in {
		out = append(out, engine.Finding{File: f.File, Line: f.Line, Severity: f.Severity, Category: f.Category})
	}
	return out
}

// maxGitHubAttempts bounds the transient retry of a GitHub API call (first try plus
// retries). 5 ≈ 0.5+1+2+4s of jittered backoff worst-case, enough to ride out a TLS
// handshake / DNS blip without hanging on a genuine outage.
const maxGitHubAttempts = 5

// retryBackoffBase is the first-retry delay; a package var so tests can shrink it.
var retryBackoffBase = 500 * time.Millisecond

// retryTransient retries fn while it returns a RETRYABLE CLIError (a network blip or
// 5xx, classified by ghAPIError), with exponential backoff + equal jitter capped at
// 8s, up to maxAttempts. A non-retryable error or success returns at once; ctx
// cancellation aborts the wait so a caller timeout still wins.
func retryTransient(ctx stdctx.Context, maxAttempts int, fn func() error) error {
	const maxBackoff = 8 * time.Second
	var err error
	for attempt := 1; ; attempt++ {
		if err = fn(); err == nil || attempt >= maxAttempts || !isRetryableErr(err) {
			return err
		}
		// Cap the shift before it overflows int64 (a large maxAttempts), then cap the
		// duration; a negative d (overflow) also clamps to maxBackoff.
		shift := attempt - 1
		if shift > 6 {
			shift = 6
		}
		d := retryBackoffBase << shift
		if d <= 0 || d > maxBackoff {
			d = maxBackoff
		}
		d = d/2 + time.Duration(rand.Int63n(int64(d/2)+1)) // equal jitter [d/2, d]
		slog.Warn(fmt.Sprintf("miucr: %s; retry %d/%d in %s", config.RedactString(err.Error()), attempt, maxAttempts-1, d.Round(time.Millisecond)))
		select {
		case <-ctx.Done():
			return err
		case <-time.After(d):
		}
	}
}

func isRetryableErr(err error) bool {
	var ce *cli.CLIError
	return errors.As(err, &ce) && ce.Retry
}
