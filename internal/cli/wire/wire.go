// Package wire binds the engine-backed Reviewer into cli. It imports both cli
// and engine/agent (which transitively import cli for CLIError), so it must sit
// above cli in the import graph; cmd/miucr blank-imports it to register.
package wire

import (
	stdctx "context"
	"errors"
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

// userRulesDir returns ~/.config/miu/cr/rules, or "" when the home dir is
// unresolvable (LoadRules treats "" as no user layer).
func userRulesDir() string {
	dir, err := config.Dir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "rules")
}

// loadRules discovers + trust-tags rules for a review. repoDir is the working
// tree (local) or the PR temp clone (--pr). allowRepo gates the Untrusted repo
// layer: false on fork PRs (attacker-authored). Warnings are non-fatal.
func loadRules(repoDir string, allowRepo bool) []rules.Rule {
	repoRulesDir := ""
	if repoDir != "" {
		repoRulesDir = filepath.Join(repoDir, ".miucr", "rules")
	}
	loaded, _ := rules.LoadRules(userRulesDir(), repoRulesDir, allowRepo)
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
		if err := publishReview(ctx, client, runner, dir, info, res, prResult); err != nil {
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
// last so a partial failure leaves the summary reflecting reality. It fills
// prResult.PostedInline + SummaryAction.
func publishReview(ctx stdctx.Context, client mgithub.Client, runner *gitcmd.Runner, dir string, info *mgithub.PRInfo, res engine.ReviewResult, prResult *cli.PRResult) error {
	diffs, err := mgithub.DiffsForPR(ctx, runner, dir, info.BaseSHA, info.HeadSHA)
	if err != nil {
		return err
	}
	existing, err := mgithub.ExistingFingerprints(ctx, client, info)
	if err != nil {
		return err
	}

	posted, omitted, err := mgithub.PostReview(ctx, client, info, res.Findings, diffs, "", existing)
	if err != nil {
		return err
	}
	summary := mgithub.RenderSummary(info, res.Findings, res.Stats, omitted)
	action, err := mgithub.UpsertSummaryComment(ctx, client, info, summary)
	if err != nil {
		return err
	}

	prResult.Posted = true
	prResult.PostedInline = posted
	prResult.SummaryAction = action
	return nil
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
