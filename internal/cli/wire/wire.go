// Package wire binds the engine-backed Reviewer into cli. It imports both cli
// and engine/agent (which transitively import cli for CLIError), so it must sit
// above cli in the import graph; cmd/miucr blank-imports it to register.
package wire

import (
	stdctx "context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
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
	"github.com/vanducng/miu-cr/internal/rules"
	"github.com/vanducng/miu-cr/internal/store"
	"github.com/vanducng/miu-cr/internal/store/sqlite"
)

// defaultRulesTokenBudget caps the rendered rules section. Always-on baseline so
// even with no user/repo rules the embedded defaults flow within a bounded slice
// of the prompt.
const defaultRulesTokenBudget = 4096

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

func init() {
	engine.SetAnchorer(anchor.ResolveLineNumbers)
	cli.SetReviewer(engineReviewer{})
	cli.SetPRReviewer(prReviewer{})
	cli.SetMCPServer(mcpServerImpl{})
}

type engineReviewer struct{}

func (engineReviewer) Review(ctx stdctx.Context, req cli.ReviewRequest) (cli.ReviewOutcome, error) {
	creds, err := agent.Resolve(agent.ResolveInput{
		Provider:  req.Provider,
		APIKey:    req.APIKey,
		BaseURL:   req.BaseURL,
		AuthToken: req.AuthToken,
		Model:     req.Model,
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

	res, err := eng.Review(ctx, engine.Request{
		Mode:         modeFor(req),
		Staged:       req.Staged,
		From:         req.From,
		To:           req.To,
		Commit:       req.Commit,
		Gate:         req.Gate,
		RepoDir:      req.RepoDir,
		IncludeGlobs: req.IncludeGlobs,
		ExcludeGlobs: req.ExcludeGlobs,
		Extensions:   req.Extensions,
		ExpandWindow: req.ExpandWindow,
		TokenBudget:  req.TokenBudget,

		Rules:            loadRules(req.RepoDir, true),
		RulesFork:        false,
		RulesTokenBudget: defaultRulesTokenBudget,
	})
	if err != nil {
		return cli.ReviewOutcome{}, err
	}
	return cli.ReviewOutcome{Findings: toCLIFindings(res.Findings), Stats: res.Stats}, nil
}

func (engineReviewer) GateFailed(findings []cli.ReviewFinding, gate string) bool {
	return engine.GateFailed(toEngineFindings(findings), gate)
}

// newGitHubClient is the GitHub client constructor seam; tests override it to
// inject a fake client without live network.
var newGitHubClient = mgithub.NewClient

// newPRThreadStore is the opt-in PR-thread store seam. It returns a non-nil store
// (and a closer) ONLY when MIUCR_PR_STORE is set — never on the action/CI path,
// which must stay stateless and byte-for-byte M2/M9. A nil store disables all
// resolution tracking; the caller MUST nil-check. Tests override it to inject a
// temp store. An open failure degrades to nil (resolution off, review proceeds).
var newPRThreadStore = func() (store.PRThreadStore, func()) {
	if os.Getenv("MIUCR_PR_STORE") == "" {
		return nil, nil
	}
	path, err := sqlite.DefaultPath()
	if err != nil {
		slog.Warn("pr-thread store disabled: " + err.Error())
		return nil, nil
	}
	s, err := sqlite.Open(path)
	if err != nil {
		slog.Warn("pr-thread store disabled: " + err.Error())
		return nil, nil
	}
	return s.PRThread(), func() { _ = s.Close() }
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

func (prReviewer) ReviewPR(ctx stdctx.Context, req cli.PRReviewRequest) (cli.ReviewOutcome, error) {
	ref, err := mgithub.ParseRef(req.Ref)
	if err != nil {
		return cli.ReviewOutcome{}, err
	}

	creds, err := agent.Resolve(agent.ResolveInput{
		Provider:  req.Provider,
		APIKey:    req.APIKey,
		BaseURL:   req.BaseURL,
		AuthToken: req.AuthToken,
		Model:     req.Model,
	})
	if err != nil {
		return cli.ReviewOutcome{}, err
	}
	llm, err := agent.New(creds, req.Timeout)
	if err != nil {
		return cli.ReviewOutcome{}, err
	}

	client := newGitHubClient(req.Token)
	info, err := mgithub.FetchPR(ctx, client, ref)
	if err != nil {
		return cli.ReviewOutcome{}, err
	}

	runner := gitcmd.New()
	dir, cleanup, err := mgithub.FetchIntoTempClone(ctx, runner, info, req.Token)
	if err != nil {
		return cli.ReviewOutcome{}, err
	}
	defer cleanup()

	eng := engine.New(agentAdapter{inner: llm}, runner)
	res, err := eng.Review(ctx, engine.Request{
		Mode:         diff.ModeRange,
		From:         info.BaseSHA,
		To:           info.HeadSHA,
		Gate:         req.Gate,
		RepoDir:      dir,
		IncludeGlobs: req.IncludeGlobs,
		ExcludeGlobs: req.ExcludeGlobs,
		Extensions:   req.Extensions,
		ExpandWindow: req.ExpandWindow,
		TokenBudget:  req.TokenBudget,

		Rules:            loadRules(dir, !info.IsFork),
		RulesFork:        info.IsFork,
		RulesTokenBudget: defaultRulesTokenBudget,
	})
	if err != nil {
		return cli.ReviewOutcome{}, err
	}

	prResult := &cli.PRResult{
		Owner:         info.Owner,
		Repo:          info.Repo,
		Number:        info.Number,
		HeadSHA:       info.HeadSHA,
		IsFork:        info.IsFork,
		SummaryAction: "none",
	}

	if req.Post {
		prStore, closeStore := newPRThreadStore()
		if closeStore != nil {
			defer closeStore()
		}
		if err := publishReview(ctx, client, runner, dir, info, res, prResult, req, prStore); err != nil {
			return cli.ReviewOutcome{}, err
		}
	}

	return cli.ReviewOutcome{
		Findings: toCLIFindings(res.Findings),
		Stats:    res.Stats,
		PR:       prResult,
	}, nil
}

// publishReview posts the review THIS run: inline comments first (skipping any
// already-posted via the per-comment fingerprint), then the sentinel summary
// last so a partial failure leaves the summary reflecting reality. It computes
// gateClean via engine.GateFailed + reviewedFiles from stats, threads both opt-in
// write-actions (default OFF) into PostReviewOptions, and fills the outcome fields.
func publishReview(ctx stdctx.Context, client mgithub.Client, runner *gitcmd.Runner, dir string, info *mgithub.PRInfo, res engine.ReviewResult, prResult *cli.PRResult, req cli.PRReviewRequest, prStore store.PRThreadStore) error {
	diffs, err := mgithub.DiffsForPR(ctx, runner, dir, info.BaseSHA, info.HeadSHA)
	if err != nil {
		return err
	}
	existing, err := mgithub.ExistingFingerprints(ctx, client, info)
	if err != nil {
		return err
	}

	// skip is the dedupe set passed to PostReview. With no store it is exactly the
	// M2/M9 ExistingFingerprints; with a store we layer prior 'posted' fps on top
	// and then SUBTRACT recurring resolved fps so a fixed-then-reappearing finding
	// reopens (the lingering marker keeps it in `existing`, so only a set-difference
	// can re-raise it — a union never could).
	skip := existing
	prKey := store.PRKey{Owner: info.Owner, Repo: info.Repo, Number: info.Number}
	var prior []store.PRFinding
	if prStore != nil {
		prior, err = prStore.ListFindings(ctx, prKey)
		if err != nil {
			return err
		}
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

	opts := mgithub.PostReviewOptions{
		Suggest:       req.Suggest,
		ApproveClean:  req.ApproveClean,
		Gate:          req.Gate,
		GateClean:     !engine.GateFailed(res.Findings, req.Gate),
		ReviewedFiles: reviewedFilesFromStats(res.Stats),
	}

	pr, err := mgithub.PostReview(ctx, client, info, res.Findings, diffs, "", skip, opts)
	if err != nil {
		return err
	}
	summary := mgithub.RenderSummary(info, res.Findings, res.Stats, pr.Omitted)
	action, err := mgithub.UpsertSummaryComment(ctx, client, info, summary)
	if err != nil {
		return err
	}

	if prStore != nil {
		if err := trackResolution(ctx, prStore, prKey, prior, res.Findings, diffs, pr.PostedFindings); err != nil {
			return err
		}
	}

	prResult.Posted = true
	prResult.PostedInline = pr.Posted
	prResult.SummaryAction = action
	prResult.SuggestionsPosted = pr.Suggestions
	prResult.ApproveAction = approveActionFor(pr.Event)
	prResult.ApproveReason = pr.Reason
	return nil
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
	pathsInDiff := make(map[string]bool, len(diffs))
	for i := range diffs {
		if diffs[i].NewPath != "" && diffs[i].NewPath != "/dev/null" {
			pathsInDiff[diffs[i].NewPath] = true
		}
	}

	var resolved []string
	for _, pf := range prior {
		if pf.Status == "posted" && !currentFPs[pf.Fingerprint] && pathsInDiff[pf.Path] {
			resolved = append(resolved, pf.Fingerprint)
		}
	}
	return prStore.MarkResolved(ctx, key, resolved)
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

func (a agentAdapter) Review(ctx stdctx.Context, rc engine.AgentContext) ([]engine.Finding, error) {
	return a.inner.Review(ctx, agent.Context{
		Text:    rc.Text,
		Rules:   rc.Rules, // lockstep: forgetting this silently drops all rules
		RepoDir: rc.RepoDir,
		Rev:     rc.Rev,
		Runner:  rc.Runner,
	})
}

// mcpServerImpl builds the engine + SQLite store and serves them over MCP. The
// agent resolves credentials lazily (on the first review_run), so the MCP
// handshake and tools/list need no Anthropic key.
type mcpServerImpl struct{}

func (mcpServerImpl) Serve(ctx stdctx.Context, req cli.MCPRequest) error {
	runner := gitcmd.New()
	eng := engine.New(lazyAgent{timeout: req.Timeout}, runner)

	var st store.Store
	if path, err := sqlite.DefaultPath(); err == nil {
		if s, oerr := sqlite.Open(path); oerr == nil {
			eng.Store = sqlite.EngineStore{S: s}
			st = s
			defer func() { _ = s.Close() }()
		}
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
// the MCP server can start (and answer initialize/tools-list) without a key.
type lazyAgent struct{ timeout time.Duration }

func (l lazyAgent) Review(ctx stdctx.Context, rc engine.AgentContext) ([]engine.Finding, error) {
	creds, err := agent.Resolve(agent.ResolveInput{})
	if err != nil {
		return nil, err
	}
	llm, err := agent.New(creds, l.timeout)
	if err != nil {
		return nil, err
	}
	return agentAdapter{inner: llm}.Review(ctx, rc)
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
