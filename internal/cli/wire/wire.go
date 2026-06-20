// Package wire binds the engine-backed Reviewer into cli. It imports both cli
// and engine/agent (which transitively import cli for CLIError), so it must sit
// above cli in the import graph; cmd/miucr blank-imports it to register.
package wire

import (
	stdctx "context"
	"errors"
	"time"

	"github.com/vanducng/miu-cr/internal/cli"
	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/agent"
	"github.com/vanducng/miu-cr/internal/engine/anchor"
	"github.com/vanducng/miu-cr/internal/engine/diff"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
	"github.com/vanducng/miu-cr/internal/mcpserver"
	"github.com/vanducng/miu-cr/internal/store"
	"github.com/vanducng/miu-cr/internal/store/sqlite"
)

func init() {
	engine.SetAnchorer(anchor.ResolveLineNumbers)
	cli.SetReviewer(engineReviewer{})
	cli.SetMCPServer(mcpServerImpl{})
}

type engineReviewer struct{}

func (engineReviewer) Review(ctx stdctx.Context, req cli.ReviewRequest) (cli.ReviewOutcome, error) {
	creds, err := agent.Resolve(req.APIKey)
	if err != nil {
		return cli.ReviewOutcome{}, err
	}
	runner := gitcmd.New()
	eng := engine.New(agentAdapter{inner: agent.New(creds, req.Timeout)}, runner)

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
	})
	if err != nil {
		return cli.ReviewOutcome{}, err
	}
	return cli.ReviewOutcome{Findings: toCLIFindings(res.Findings), Stats: res.Stats}, nil
}

func (engineReviewer) GateFailed(findings []cli.ReviewFinding, gate string) bool {
	return engine.GateFailed(toEngineFindings(findings), gate)
}

// agentAdapter bridges the concrete Anthropic agent (agent.Context) to the
// engine's local Agent interface (engine.AgentContext), keeping engine below
// agent in the import graph.
type agentAdapter struct{ inner agent.Agent }

func (a agentAdapter) Review(ctx stdctx.Context, rc engine.AgentContext) ([]engine.Finding, error) {
	return a.inner.Review(ctx, agent.Context{
		Text:    rc.Text,
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
	creds, err := agent.Resolve("")
	if err != nil {
		return nil, err
	}
	return agentAdapter{inner: agent.New(creds, l.timeout)}.Review(ctx, rc)
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
