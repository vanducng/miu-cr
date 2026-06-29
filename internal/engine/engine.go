// Package engine composes the review pillars (diff acquisition, file selection,
// context assembly, the LLM pass, and drift-reject anchoring) into a single
// Review call producing a gated ReviewResult.
package engine

import (
	stdctx "context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	"github.com/vanducng/miu-cr/internal/config"
	enginectx "github.com/vanducng/miu-cr/internal/engine/context"
	"github.com/vanducng/miu-cr/internal/engine/diff"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
	"github.com/vanducng/miu-cr/internal/engine/tools/symbolcontext"
	"github.com/vanducng/miu-cr/internal/rules"
)

// PersistRecord is the engine's view of a persisted review. The store package
// adapts it to its concrete ReviewRecord; defined here so engine sits below
// store in the import graph (store imports engine.Finding).
type PersistRecord struct {
	ID          string
	RepoDir     string
	Mode        string
	HeadSHA     string
	Owner       string
	Repo        string
	Number      int
	Provider    string
	Model       string
	CreatedAt   time.Time
	Findings    []Finding
	Stats       map[string]any
	Transcript  []byte
	RawPrompt   string
	RawResponse string
	TraceJSON   string
}

const maxTraceToolResultBytes = 16 * 1024

// TurnRecord is one tool dispatch in the review session.
type TurnRecord struct {
	Turn            int    `json:"turn"`
	Tool            string `json:"tool"`
	Args            string `json:"args"`
	Result          string `json:"result,omitempty"`
	Error           bool   `json:"error,omitempty"`
	ResultTruncated bool   `json:"result_truncated,omitempty"`
}

// DiffMeta identifies which revisions the review compared and how that diff was
// computed (staged index / commit / range).
type DiffMeta struct {
	BaseSHA string `json:"base_sha"`
	HeadSHA string `json:"head_sha"`
	Source  string `json:"source"`
}

// RuleRef is one injected rule's identity for the trace: its file stem and trust
// provenance (e.g. built-in / user / repo). No rule body, selection identity only.
type RuleRef struct {
	Stem       string `json:"stem"`
	Provenance string `json:"provenance"`
}

// ReviewTrace accumulates the full session content during a review for
// persistence: the system + user prompts, diff identification, selected files,
// injected rules, model/provider, the per-turn tool calls/results, and the raw final
// response. The agent/engine record into it via nil-safe setters (mirroring the
// Progress seam); the engine creates it, threads it onto AgentContext, and reads
// it back after Review. A nil *ReviewTrace makes every recorder a no-op (capture
// disabled). The content (system/user prompt + diff) is captured in full by
// design: stored LOCAL-only in the gitignored state.db, never in the review
// envelope or a posted comment; the invariant is no auth tokens (redacted at
// persist), not "no code".
//
// Sink, when non-nil, is invoked by each setter with the step name + the recorded
// payload; phase 2 wires it to live --trace NDJSON on stderr; nil = persist-only.
type ReviewTrace struct {
	SystemPrompt  string       `json:"system_prompt"`
	UserPrompt    string       `json:"user_prompt"`
	DiffMeta      DiffMeta     `json:"diff_meta"`
	SelectedFiles []string     `json:"selected_files"`
	InjectedRules []RuleRef    `json:"injected_rules"`
	Model         string       `json:"model"`
	Provider      string       `json:"provider"`
	FinalResponse string       `json:"final_response"`
	Turns         []TurnRecord `json:"turns"`
	// Reasoning holds the model's captured internal reasoning. Populated only when
	// CaptureReasoning is on AND [review].thinking is enabled (off = nothing to capture).
	// Quotes diff content: always redacted at persist; opt-in, off by default.
	Reasoning    *TraceReasoning                `json:"reasoning,omitempty"`
	Sink         func(step string, payload any) `json:"-"`
	modelEmitted bool                           // the live "model" step fires once, when model first becomes non-empty
}

// TraceReasoning holds one captured reasoning output. Text is the raw thinking for
// Anthropic; "[hidden by provider]" for OpenAI (raw reasoning is not returned).
// Tokens is the reasoning token count from the usage object (0 if not reported).
type TraceReasoning struct {
	Provider string `json:"provider"`
	Text     string `json:"text"`
	Tokens   int64  `json:"tokens,omitempty"`
}

// emit forwards a recorded step to the live Sink when set; nil-safe.
func (t *ReviewTrace) emit(step string, payload any) {
	if t == nil || t.Sink == nil {
		return
	}
	t.Sink(step, payload)
}

// SetSystemPrompt records the shared system prompt once (first non-empty wins);
// nil-safe. Called in every backend so openai/codex don't persist an empty one.
func (t *ReviewTrace) SetSystemPrompt(p string) {
	if t == nil || t.SystemPrompt != "" {
		return
	}
	t.SystemPrompt = p
	t.emit("system_prompt", p)
}

// SetPrompt records the raw user prompt once (first non-empty wins); nil-safe.
func (t *ReviewTrace) SetPrompt(p string) {
	if t == nil || t.UserPrompt != "" {
		return
	}
	t.UserPrompt = p
	t.emit("user_prompt", p)
}

// SetDiffMeta records the diff identification (base/head + source); nil-safe.
func (t *ReviewTrace) SetDiffMeta(m DiffMeta) {
	if t == nil {
		return
	}
	t.DiffMeta = m
	t.emit("diff_meta", m)
}

// SetSelectedFiles records the post-filter selected file set; nil-safe.
func (t *ReviewTrace) SetSelectedFiles(files []string) {
	if t == nil {
		return
	}
	t.SelectedFiles = files
	t.emit("selected_files", files)
}

// SetInjectedRules records which rules survived selection (stem + provenance);
// nil-safe.
func (t *ReviewTrace) SetInjectedRules(refs []RuleRef) {
	if t == nil {
		return
	}
	t.InjectedRules = refs
	t.emit("injected_rules", refs)
}

// SetModel records the resolved model + provider; nil-safe. Called per backend.
func (t *ReviewTrace) SetModel(provider, model string) {
	if t == nil {
		return
	}
	// First-NON-EMPTY-wins per field: the engine may call this with an empty
	// req.Provider/Model before the backend supplies the resolved values.
	if t.Provider == "" {
		t.Provider = provider
	}
	if t.Model == "" {
		t.Model = model
	}
	// Emit the live step exactly once, when the model first becomes known.
	if !t.modelEmitted && t.Model != "" {
		t.modelEmitted = true
		t.emit("model", map[string]string{"provider": t.Provider, "model": t.Model})
	}
}

// SetFinalResponse records the raw final response text; nil-safe.
func (t *ReviewTrace) SetFinalResponse(r string) {
	if t == nil {
		return
	}
	t.FinalResponse = r
	t.emit("final_response", r)
}

// RecordTool appends one tool dispatch; nil-safe.
func (t *ReviewTrace) RecordTool(turn int, tool, args string) {
	if t == nil {
		return
	}
	tr := TurnRecord{Turn: turn, Tool: tool, Args: args}
	t.Turns = append(t.Turns, tr)
	t.emit("tool", tr)
}

// RecordToolResult attaches a bounded result to the latest matching dispatch.
func (t *ReviewTrace) RecordToolResult(turn int, tool, args, result string, isErr bool) {
	if t == nil {
		return
	}
	result, truncated := truncateTraceToolResult(result)
	for i := len(t.Turns) - 1; i >= 0; i-- {
		tr := &t.Turns[i]
		if tr.Turn == turn && tr.Tool == tool && tr.Args == args && tr.Result == "" && !tr.Error && !tr.ResultTruncated {
			tr.Result = result
			tr.Error = isErr
			tr.ResultTruncated = truncated
			t.emit("tool_result", *tr)
			return
		}
	}
	tr := TurnRecord{Turn: turn, Tool: tool, Args: args, Result: result, Error: isErr, ResultTruncated: truncated}
	t.Turns = append(t.Turns, tr)
	t.emit("tool_result", tr)
}

func truncateTraceToolResult(result string) (string, bool) {
	if len(result) <= maxTraceToolResultBytes {
		return result, false
	}
	const marker = "\n...[truncated tool result]..."
	keep := maxTraceToolResultBytes - len(marker)
	if keep <= 0 {
		return string(truncateUTF8Bytes([]byte(result), maxTraceToolResultBytes)), true
	}
	return string(truncateUTF8Bytes([]byte(result), keep)) + marker, true
}

// SetReasoning captures the model's reasoning once (first non-empty wins); nil-safe.
// Only call when text is non-empty: reasoning quotes diff content and is redacted at persist.
func (t *ReviewTrace) SetReasoning(provider, text string, tokens int64) {
	if t == nil || t.Reasoning != nil || text == "" {
		return
	}
	t.Reasoning = &TraceReasoning{Provider: provider, Text: text, Tokens: tokens}
	t.emit("reasoning", t.Reasoning)
}

// Store is the optional persistence seam: when set, Review saves each result and
// GetReview re-fetches by id. Implemented by internal/store/sqlite.
type Store interface {
	SaveReview(ctx stdctx.Context, rec PersistRecord) (string, error)
	GetReview(ctx stdctx.Context, id string) (PersistRecord, error)
}

// Anchorer re-anchors model findings to line numbers from their QuotedCode
// against the reviewed diffs (drift = Line==0). The anchor package implements it
// and the wire layer injects it via SetAnchorer, keeping engine below anchor in
// the import graph (anchor imports engine.Finding).
type Anchorer func(findings []Finding, diffs []diff.Diff) []Finding

var anchorLineNumbers Anchorer

// SetAnchorer wires the drift-reject anchoring implementation.
func SetAnchorer(a Anchorer) { anchorLineNumbers = a }

// CleanReplacementFn is the injected re-validation seam (mirrors Anchorer): given
// a finding and its raw new-file content it classifies whether SuggestedPatch is a
// clean replacement, returning the suggestion text, a stable lowercase reason, and
// whether that reason is worth a repair pass. internal/github implements it
// (ClassifyReplacement) and wire injects it via SetCleanReplacement, keeping engine
// free of an internal/github import (github imports engine.Finding → cycle).
type CleanReplacementFn func(f Finding, newFileContent string) (patch string, reason string, repairable bool)

var classifyReplacement CleanReplacementFn

// SetCleanReplacement wires the deterministic re-validation gate the repair loop
// re-runs after each repair attempt.
func SetCleanReplacement(fn CleanReplacementFn) { classifyReplacement = fn }

// AgentContext is everything the review pass needs: the assembled prompt text
// plus the reviewed revision so the agent reads the SAME content the diff came
// from (Rev=="" is the staged index). Defined here (not imported from the agent
// package) so engine sits below agent in the import graph; the wire layer adapts
// the concrete Anthropic agent to this interface.
type AgentContext struct {
	Text  string
	Rules string // fenced rules section injected into the USER turn before the diff
	// SemanticContext is the optional M7 advisory block (prior findings whose code
	// is cosine-near the current change). Empty => byte-for-byte M6 prompt. LOCKSTEP:
	// mirror this field everywhere Rules is threaded or it is silently dropped.
	SemanticContext string
	ProjectContext  string
	RelatedContext  string
	// WantDiagram opts into the mermaid change diagram. LOCKSTEP: mirror this field
	// everywhere Rules/SemanticContext are threaded or it is silently dropped.
	WantDiagram bool
	// Instruction is the optional per-review developer steer. LOCKSTEP: mirror this
	// field everywhere Rules/WantDiagram are threaded or it is silently dropped.
	Instruction string
	// Conversation is the optional fetched PR conversation (Untrusted, fenced,
	// byte-capped, context-only). LOCKSTEP: mirror Instruction at every hop.
	Conversation string
	// PromptFormat selects the prompt serialization: "" or "xml" → XML-tagged format
	// (the default, resolved in Engine.Review); "markdown" → fenced format. LOCKSTEP:
	// mirror Conversation at every hop or the format is silently dropped.
	PromptFormat   string
	OperatorPrompt string
	ProviderRetry  config.ProviderRetry
	Tools          config.ReviewTools
	SymbolContext  config.SymbolContext
	RepoDir        string
	Rev            string
	Runner         *gitcmd.Runner
	Progress       func(string) // nil = silent; milestone strings only, never secrets
	// Trace, when non-nil, captures the raw prompt, per-turn tool calls/results, and raw
	// final response for persistence. nil = no capture (mirrors Progress).
	Trace *ReviewTrace
	// CaptureReasoning, when true, captures thinking blocks (Anthropic) or reasoning
	// token count (OpenAI) into Trace.Reasoning. Requires [review].thinking to be on.
	CaptureReasoning bool
}

type SubagentConfig struct {
	Mode            string
	MaxParallel     int
	MinFiles        int
	MinContextBytes int
	RequireAll      bool
	Agents          []SubagentSpec
}

type SubagentSpec struct {
	Name           string
	IncludeGlobs   []string
	ExcludeGlobs   []string
	OperatorPrompt string
}

// Retriever is the engine-local seam for M7 semantic recall: wire injects an
// implementation that scrubs+embeds the current changed code and returns prior
// cosine-near findings. The engine does NO embedding/DB/network itself (it
// imports neither openai-go nor pgx), exactly like the Rules injection. nil =>
// the default M6 path (no SemanticContext). changedHunks is one inner slice per
// diff hunk (its changed lines) so the read path can embed at hunk granularity,
// matching the write path's per-finding code-anchor representation.
type Retriever interface {
	Related(ctx stdctx.Context, changedHunks [][]string) (string, error)
}

// Agent runs one review pass over the assembled context and returns findings
// WITHOUT line numbers (the engine re-anchors from QuotedCode) plus the optional
// walkthrough/per-file digest the same pass may emit. Review must be safe for
// concurrent calls; subagent fanout shares one Agent instance across workers.
type Agent interface {
	Review(ctx stdctx.Context, rc AgentContext) (ReviewOutput, error)
	// RepairPatch runs the conditional second pass: ONE span + ONE problem in,
	// the minimal replacement out (already fence-stripped/trimmed) plus that call's
	// token Usage. "" => no usable replacement; the engine falls back to the
	// original finding. The wire layer adapts this to agent.RepairPatch.
	RepairPatch(ctx stdctx.Context, rr RepairRequest) (string, Usage, error)
}

// RepairRequest is the engine-side shadow of agent.RepairRequest (defined here so
// engine sits below agent in the import graph; the wire layer adapts it).
type RepairRequest struct {
	Span          string
	Rationale     string
	Category      string
	Severity      string
	ProviderRetry config.ProviderRetry
}

// Request is one review invocation: the diff mode and its operands, the severity
// gate, and the file-selection globs.
type Request struct {
	Mode         diff.Mode
	Staged       bool
	From         string
	To           string
	Commit       string
	Gate         string
	IncludeGlobs []string
	ExcludeGlobs []string
	RepoDir      string
	Extensions   []string
	TokenBudget  int
	ExpandWindow int

	// Rules are the loaded, trust-tagged rule files (wire loads + tags them; the
	// engine selects them against the changed files in-memory). RulesFork drops
	// Untrusted (repo) rules + their context_files before selection (attacker-
	// authored on fork PRs). RulesTokenBudget caps the rendered rules section.
	Rules            []rules.Rule
	RulesFork        bool
	RulesTokenBudget int

	// Retriever is the optional M7 semantic-recall seam. When non-nil the engine
	// calls it with the current change's code anchors BEFORE the agent and threads
	// the returned advisory prose into AgentContext.SemanticContext. nil => M6.
	Retriever Retriever

	// WantDiagram opts into the mermaid change diagram (default OFF). Threaded onto
	// AgentContext so the diagram instruction rides the USER turn; OFF is byte-identical.
	WantDiagram bool

	ProjectContext  bool
	ContextHops     int
	ContextHopsAuto bool
	Subagents       SubagentConfig

	// Instruction is the optional per-review developer steer (--instruction). Threaded
	// onto AgentContext so it rides the USER turn; empty is byte-identical. LOCKSTEP:
	// mirror WantDiagram at every hop or it is silently dropped.
	Instruction string

	// Conversation is the optional fetched PR conversation (--conversation). Untrusted,
	// fenced, byte-capped, context-only; the wire layer fetches/renders/caps it and
	// drops it on fork PRs. Threaded onto AgentContext so it rides the USER turn; empty
	// is byte-identical. LOCKSTEP: mirror Instruction at every hop.
	Conversation string
	// PromptFormat selects the prompt serialization: "" or "xml" → XML-tagged (default);
	// "markdown" → fenced. LOCKSTEP: mirror Conversation at every hop.
	PromptFormat string
	// OperatorPrompt is trusted host policy. LOCKSTEP: mirror Conversation at every hop.
	OperatorPrompt string
	ProviderRetry  config.ProviderRetry
	Tools          config.ReviewTools
	SymbolContext  config.SymbolContext

	// Progress is the optional milestone sink (stderr); nil = silent. The wire/cli
	// layer builds it from --verbose/--quiet + a TTY check. Only milestone strings
	// and file paths/tool names ever reach it, never tokens.
	Progress func(string)

	// TraceSink, when non-nil, is wired onto the ReviewTrace.Sink so each capture
	// seam emits a live step (cli's --trace renders NDJSON to stderr). Distinct from
	// Progress; nil = persist-only (no live stream). Capture still runs regardless,
	// so the live stream and the persisted trace stay consistent by construction.
	TraceSink func(step string, payload any)

	// CaptureReasoning, when true, captures the model's reasoning into ReviewTrace.Reasoning.
	// Requires [review].thinking to be on (with thinking off there is nothing to capture).
	// OFF by default; gated by MIUCR_TRACE_REASONING env var.
	CaptureReasoning bool

	// Post reports whether this run will publish (the --pr post path). Repair only
	// matters when one-click suggestions are actually rendered, so the loop gates on
	// PatchRepair && Post: a dry-run (--no-post) makes ZERO repair LLM calls.
	Post bool

	// PatchRepair opts into the conditional second LLM pass that recovers one-click
	// suggestions the first pass nearly produced (default OFF; PR-path + --suggest +
	// --post only). OFF is byte-identical. MaxRepair caps the per-review repair calls
	// (0 => defaultMaxRepair); candidates are tried highest-severity-first.
	PatchRepair bool
	MaxRepair   int

	// Persist context copied onto the saved PersistRecord (no secrets): the resolved
	// provider/model and, on the --pr path, the PR owner/repo/number. Local reviews
	// leave Owner/Repo/Number zero.
	Provider string
	Model    string
	Owner    string
	Repo     string
	Number   int

	// Quota optionally enforces the resolved provider's usage quota. nil = no
	// quota. The wire layer builds it for the selected provider+window; the engine
	// stays storage-agnostic. Check runs before the LLM call (fail-closed), Record
	// after with the pass usage.
	Quota QuotaGate
}

// QuotaGate meters a provider instance's usage against its configured quota.
// Check returns a non-nil (typed quota.exceeded) error to hard-block the review
// before any LLM call; Record adds the completed pass's usage to the counter.
// Implemented by internal/quota, injected via Request.Quota.
type QuotaGate interface {
	Check(ctx stdctx.Context) error
	Record(ctx stdctx.Context, usage Usage) error
}

// progress invokes the sink when set; a nil sink is a silent no-op.
func (req Request) progress(msg string) {
	if req.Progress != nil {
		req.Progress(msg)
	}
}

// Engine orchestrates a review with an injectable Agent so the pipeline is
// testable without network or an API key. Store is optional persistence.
type Engine struct {
	Agent  Agent
	Runner *gitcmd.Runner
	Store  Store
}

// New returns an Engine bound to the given Agent and git Runner.
func New(a Agent, runner *gitcmd.Runner) *Engine {
	if runner == nil {
		runner = gitcmd.New()
	}
	return &Engine{Agent: a, Runner: runner}
}

// GetReview re-fetches a persisted review by id; errors if no Store is wired.
func (e *Engine) GetReview(ctx stdctx.Context, id string) (PersistRecord, error) {
	if e.Store == nil {
		return PersistRecord{}, &clierr.CLIError{Code: "engine.no_store", Message: "persistence not configured", Exit: 1}
	}
	return e.Store.GetReview(ctx, id)
}

// severityRank orders severities low→critical; unknown/empty sorts below low so
// an ungraded finding never trips a gate above "none".
var severityRank = map[string]int{
	"info":     1,
	"low":      2,
	"medium":   3,
	"high":     4,
	"critical": 5,
}

func rankOf(sev string) int { return severityRank[normSeverity(sev)] }

func normSeverity(s string) string {
	switch s {
	case "info", "low", "medium", "high", "critical":
		return s
	default:
		return ""
	}
}

// gateRank maps a gate name to its threshold. "none"/"" disables gating (0,true);
// a recognized severity returns its rank; an unknown gate returns (0,false) so
// callers fail loudly rather than silently disabling the gate.
func gateRank(gate string) (int, bool) {
	if gate == "" || gate == "none" {
		return 0, true
	}
	r, ok := severityRank[gate]
	return r, ok
}

// Review runs the full pipeline: GetDiff → SelectFiles → AssembleContext →
// Agent.Review → ResolveLineNumbers → drop drift (Line==0) → dedupe → stats with
// the max severity rank for the gate decision. An empty diff set yields an empty
// findings list (not an error).
func (e *Engine) Review(ctx stdctx.Context, req Request) (ReviewResult, error) {
	// xml is the default prompt format; an unset value resolves here so every
	// downstream dispatch (and inherited subagent requests) sees it. markdown is
	// opt-out via an explicit "markdown".
	if req.PromptFormat == "" {
		req.PromptFormat = "xml"
	}
	diffs, err := diff.GetDiff(ctx, req.Mode, req.RepoDir, req.From, req.To, req.Commit, e.Runner)
	if err != nil {
		return ReviewResult{}, err
	}

	selected := SelectFiles(diffs, FilterOptions{
		Extensions: req.Extensions,
		Include:    req.IncludeGlobs,
		Exclude:    req.ExcludeGlobs,
	})

	if len(selected) == 0 {
		return ReviewResult{
			Findings: []Finding{},
			Stats: map[string]any{
				"files_changed":  float64(len(diffs)),
				"files_reviewed": float64(0),
				"findings_total": float64(0),
				"max_severity":   maxSeverity(nil),
				"gate":           req.Gate,
			},
		}, nil
	}

	// Fail-closed quota gate, before any context assembly or LLM call: an
	// over-quota (or unverifiable) provider blocks here doing almost no work.
	if req.Quota != nil {
		if err := req.Quota.Check(ctx); err != nil {
			return ReviewResult{}, err
		}
	}

	var trace *ReviewTrace
	if e.Store != nil || req.TraceSink != nil {
		trace = &ReviewTrace{Sink: req.TraceSink}
		headSHA, _ := e.Runner.HeadSHA(ctx, req.RepoDir)
		trace.SetDiffMeta(DiffMeta{BaseSHA: baseRef(req), HeadSHA: headSHA, Source: modeName(req.Mode)})
		trace.SetSelectedFiles(changedPathsOf(selected))
	}

	rulesText, rulesApplied, rulesTruncated := e.buildRules(req, selected, trace)

	assembleStart := time.Now()
	assembled := enginectx.AssembleContext(selected, enginectx.AssembleOptions{
		TokenBudget:  diffBudget(req.TokenBudget, req.RulesTokenBudget),
		ExpandWindow: req.ExpandWindow,
		UseXML:       req.PromptFormat == "xml",
	})
	assembleMS := time.Since(assembleStart).Milliseconds()

	req.progress(fmt.Sprintf("reviewing %d files (%d changed)…", len(selected), len(diffs)))
	if lvl, _ := assembled.Stats["truncation_level"].(string); lvl != "" && lvl != "full" {
		req.progress("diff compressed: " + lvl)
	}

	rev := selected[0].Ref
	semanticStart := time.Now()
	semanticContext, semanticStat := retrieveSemantic(ctx, req.Retriever, selected)
	semanticMS := time.Since(semanticStart).Milliseconds()
	projectContext, projectContextFileCount, projectContextTruncated := "", 0, false
	projectContextMS := int64(0)
	if req.ProjectContext {
		projectStart := time.Now()
		projectContext, projectContextFileCount, projectContextTruncated = e.loadProjectContext(ctx, req.RepoDir, rev)
		projectContextMS = time.Since(projectStart).Milliseconds()
	}
	relatedHops := req.ContextHops
	if req.ContextHopsAuto {
		relatedHops = autoContextHops(selected)
	}
	related := enginectx.RelatedResult{}
	relatedMS := int64(0)
	if relatedHops > 0 {
		relatedStart := time.Now()
		related = enginectx.BuildRelatedContext(ctx, req.RepoDir, rev, selected, e.Runner, enginectx.RelatedOptions{HopDepth: relatedHops})
		relatedMS = time.Since(relatedStart).Milliseconds()
	}
	changedSymbolStart := time.Now()
	changedSymbolContext, changedSymbolFiles, changedSymbolTruncated := buildChangedSymbolContext(ctx, req, selected, rev, e.Runner)
	changedSymbolMS := time.Since(changedSymbolStart).Milliseconds()
	relatedText := joinPromptContext(changedSymbolContext, related.Text)

	trace.SetModel(req.Provider, req.Model)

	out, agentMS, passStats, err := e.reviewPasses(ctx, req, selected, assembled, reviewSharedContext{
		rulesText:       rulesText,
		semanticContext: semanticContext,
		projectContext:  projectContext,
		relatedContext:  relatedText,
		rev:             rev,
		trace:           trace,
	})
	if err != nil {
		return ReviewResult{}, err
	}

	if anchorLineNumbers == nil {
		return ReviewResult{}, &clierr.CLIError{Code: "engine.no_anchorer", Message: "anchoring not wired", Exit: 1}
	}
	anchored := anchorLineNumbers(out.Findings, selected)
	kept := dropDrift(anchored)
	kept = dedupe(kept)

	stats := map[string]any{
		"files_changed":    float64(len(diffs)),
		"files_reviewed":   float64(len(selected)),
		"findings_total":   float64(len(kept)),
		"findings_dropped": float64(len(anchored) - len(kept)),
		"max_severity":     maxSeverity(kept),
		"gate":             req.Gate,
		"truncation_level": assembled.Stats["truncation_level"],
		"rules_applied":    float64(rulesApplied),
		"rules_truncated":  rulesTruncated,
		"context_bytes":    float64(len(assembled.Text)),
		"rules_bytes":      float64(len(rulesText)),
		"context_ms":       float64(assembleMS),
		"provider_ms":      float64(agentMS),
	}
	if semanticStat != "" {
		stats["semantic_recall"] = semanticStat
		stats["semantic_recall_ms"] = float64(semanticMS)
	}
	if req.ProjectContext {
		stats["project_context_files"] = float64(projectContextFileCount)
		stats["project_context_truncated"] = projectContextTruncated
		stats["project_context_ms"] = float64(projectContextMS)
	}
	if relatedHops > 0 {
		stats["related_context_files"] = float64(len(related.Files))
		stats["related_context_hops"] = float64(related.Hops)
		stats["related_context_truncated"] = related.Truncated
		stats["related_context_ms"] = float64(relatedMS)
	}
	if changedSymbolFiles > 0 || changedSymbolTruncated {
		stats["changed_symbol_context_files"] = float64(changedSymbolFiles)
		stats["changed_symbol_context_truncated"] = changedSymbolTruncated
		stats["changed_symbol_context_ms"] = float64(changedSymbolMS)
	}
	for k, v := range passStats {
		stats[k] = v
	}
	repairStart := time.Now()
	kept, repairUsage := e.repairPatches(ctx, kept, selected, req, stats)
	out.Usage.Add(repairUsage) // fold the --patch-repair second-pass tokens into metering + stats
	if req.PatchRepair && req.Post {
		stats["patch_repair_ms"] = float64(time.Since(repairStart).Milliseconds())
	}

	if out.Usage.TotalTokens() > 0 {
		stats["input_tokens"] = float64(out.Usage.InputTokens)
		stats["output_tokens"] = float64(out.Usage.OutputTokens)
		stats["cache_read_tokens"] = float64(out.Usage.CacheReadTokens)
		stats["cache_creation_tokens"] = float64(out.Usage.CacheCreationTokens)
		stats["total_input_tokens"] = float64(out.Usage.TotalInputTokens())
		stats["cache_hit_ratio"] = out.Usage.CacheHitRatio()
	}
	if req.Quota != nil {
		// out.Usage now includes the --patch-repair second pass (folded in above), so
		// the quota meters the full review. Record is fail-open: a counter write error
		// surfaces in stats but never fails the review.
		if rerr := req.Quota.Record(ctx, out.Usage); rerr != nil {
			stats["quota_record_error"] = config.RedactString(rerr.Error())
		}
	}
	addTraceStats(stats, trace)

	result := ReviewResult{Findings: kept, Walkthrough: out.Walkthrough, FileSummaries: out.FileSummaries, Diagram: out.Diagram, Confidence: out.Confidence, ConfidenceReason: out.ConfidenceReason, Stats: stats}

	if e.Store != nil {
		headSHA, _ := e.Runner.HeadSHA(ctx, req.RepoDir)
		rec := PersistRecord{
			RepoDir:   req.RepoDir,
			Mode:      modeName(req.Mode),
			HeadSHA:   headSHA,
			Owner:     req.Owner,
			Repo:      req.Repo,
			Number:    req.Number,
			Provider:  req.Provider,
			Model:     req.Model,
			CreatedAt: time.Now().UTC(),
			Findings:  kept,
			Stats:     stats,
		}
		if trace != nil {
			rec.RawPrompt = trace.UserPrompt
			rec.RawResponse = trace.FinalResponse
			redactedTrace := redactTrace(*trace)
			if len(redactedTrace.Turns) > 0 {
				if blob, merr := json.Marshal(redactedTrace.Turns); merr != nil {
					stats["transcript_error"] = merr.Error()
				} else {
					rec.Transcript = blob
				}
			}
			if blob, merr := json.Marshal(redactedTrace); merr != nil {
				stats["trace_error"] = merr.Error()
			} else {
				rec.TraceJSON = string(blob)
			}
		}
		id, serr := e.Store.SaveReview(ctx, rec)
		if serr != nil {
			stats["persist_error"] = serr.Error()
		} else {
			result.ID = id
		}
	}

	return result, nil
}

func addTraceStats(stats map[string]any, trace *ReviewTrace) {
	if trace == nil {
		return
	}
	stats["system_prompt_bytes"] = float64(len(trace.SystemPrompt))
	stats["user_prompt_bytes"] = float64(len(trace.UserPrompt))
	stats["final_response_bytes"] = float64(len(trace.FinalResponse))
	stats["tool_calls"] = float64(len(trace.Turns))
	if len(trace.Turns) == 0 {
		return
	}
	byTool := map[string]float64{}
	turns := map[int]struct{}{}
	for _, tr := range trace.Turns {
		byTool[tr.Tool]++
		turns[tr.Turn] = struct{}{}
	}
	stats["tool_turns"] = float64(len(turns))
	stats["tool_calls_by_tool"] = byTool
}

func autoContextHops(selected []diff.Diff) int {
	var churn int64
	for _, d := range selected {
		churn += d.Insertions + d.Deletions
	}
	switch {
	case len(selected) >= 10 || churn >= 300:
		return 3
	default:
		return 2
	}
}

// buildRules selects the loaded rules against the changed paths and renders the
// fenced rules section. On a fork PR, Untrusted (repo) rules are dropped before
// selection and their context_files are never inlined.
func (e *Engine) buildRules(req Request, selected []diff.Diff, trace *ReviewTrace) (text string, applied int, truncated bool) {
	loaded := req.Rules
	if req.RulesFork {
		loaded = trustedOnly(loaded)
	}
	if len(loaded) == 0 {
		return "", 0, false
	}
	picked := rules.SelectRules(loaded, changedPathsOf(selected))
	if len(picked) == 0 {
		return "", 0, false
	}
	refs := make([]RuleRef, 0, len(picked))
	for _, r := range picked {
		refs = append(refs, RuleRef{Stem: r.Stem, Provenance: r.Provenance.String()})
	}
	trace.SetInjectedRules(refs)
	return rules.BuildRulesSection(picked, !req.RulesFork, req.RulesTokenBudget, req.PromptFormat == "xml")
}

// trustedOnly drops Untrusted (repo) rules, used on fork PRs where repo rules
// are attacker-authored.
func trustedOnly(in []rules.Rule) []rules.Rule {
	out := make([]rules.Rule, 0, len(in))
	for _, r := range in {
		if r.Provenance.Trusted() {
			out = append(out, r)
		}
	}
	return out
}

// retrieveSemantic runs the optional M7 Retriever over the change's code anchors
// (the SAME code representation the write path embeds). It is best-effort: a nil
// Retriever, an empty result, or any error yields an empty advisory block (=> M6
// prompt) and a redacted stat; it never fails the review. The returned stat (when
// non-empty) is recorded under stats.semantic_recall so cost/outcome is visible.
func retrieveSemantic(ctx stdctx.Context, r Retriever, selected []diff.Diff) (advisory, stat string) {
	if r == nil {
		return "", ""
	}
	code := changedHunksOf(selected)
	if len(code) == 0 {
		return "", "empty_change"
	}
	advisory, err := r.Related(ctx, code)
	if err != nil {
		return "", "error"
	}
	if strings.TrimSpace(advisory) == "" {
		return "", "no_matches"
	}
	return advisory, "injected"
}

const (
	projectContextMaxFileBytes  = 8 * 1024
	projectContextMaxTotalBytes = 32 * 1024
	changedSymbolContextFiles   = 12
	changedSymbolContextBytes   = 16 * 1024
	changedSymbolContextLimit   = 8
	changedSymbolContextHeader  = "Changed symbol context from the reviewed revision:\n"
)

var projectContextFiles = []string{"AGENTS.md", "CLAUDE.md"}

func (e *Engine) loadProjectContext(ctx stdctx.Context, repoDir, rev string) (string, int, bool) {
	runner := e.Runner
	if runner == nil {
		runner = gitcmd.New()
	}
	var sb strings.Builder
	total := 0
	files := 0
	truncated := false
	for _, name := range projectContextFiles {
		blob, err := runner.ShowBlob(ctx, repoDir, rev, name)
		if err != nil {
			continue
		}
		if len(blob) > projectContextMaxFileBytes {
			blob = truncateUTF8Bytes(blob, projectContextMaxFileBytes)
			truncated = true
		}
		if total+len(blob) > projectContextMaxTotalBytes {
			remaining := projectContextMaxTotalBytes - total
			if remaining <= 0 {
				truncated = true
				break
			}
			blob = truncateUTF8Bytes(blob, remaining)
			truncated = true
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString("--- project_file: ")
		sb.WriteString(name)
		sb.WriteString(" ---\n")
		sb.Write(blob)
		total += len(blob)
		files++
		if total >= projectContextMaxTotalBytes {
			break
		}
	}
	return sb.String(), files, truncated
}

func buildChangedSymbolContext(ctx stdctx.Context, req Request, selected []diff.Diff, rev string, runner *gitcmd.Runner) (string, int, bool) {
	if runner == nil {
		runner = gitcmd.New()
	}
	maxFiles := changedSymbolContextFiles
	if req.SymbolContext.MaxFiles > 0 && req.SymbolContext.MaxFiles < maxFiles {
		maxFiles = req.SymbolContext.MaxFiles
	}
	if maxFiles <= 0 {
		return "", 0, false
	}
	maxBytes := req.SymbolContext.MaxBytesOrDefault(changedSymbolContextBytes)
	if maxBytes > changedSymbolContextBytes {
		maxBytes = changedSymbolContextBytes
	}
	tc := symbolcontext.Context{RepoDir: req.RepoDir, Rev: rev, Runner: runner}
	var sb strings.Builder
	seen := map[string]bool{}
	files := 0
	truncated := false
	for _, d := range selected {
		if d.IsDeleted {
			continue
		}
		path := filepath.ToSlash(strings.TrimSpace(d.NewPath))
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		if files >= maxFiles {
			truncated = true
			break
		}
		block := changedFileSymbolBlock(ctx, req.SymbolContext, tc, path)
		if strings.TrimSpace(block) == "" {
			continue
		}
		prefix := changedSymbolContextHeader
		if sb.Len() > 0 {
			prefix = "\n"
		}
		block = prefix + block
		if sb.Len()+len(block) > maxBytes {
			remaining := maxBytes - sb.Len()
			if remaining > len(prefix) {
				sb.Write(truncateUTF8Bytes([]byte(block), remaining))
				files++
			}
			truncated = true
			break
		}
		sb.WriteString(block)
		files++
	}
	return strings.TrimRight(sb.String(), "\n"), files, truncated
}

func changedFileSymbolBlock(ctx stdctx.Context, cfg config.SymbolContext, tc symbolcontext.Context, path string) string {
	var blocks []string
	if out, failed := runSymbolContext(ctx, cfg, tc, symbolcontext.Args{Relation: "document_symbols", File: path, Limit: changedSymbolContextLimit}); usefulSymbolOutput(out, failed) {
		blocks = append(blocks, out)
	}
	if strings.EqualFold(filepath.Ext(path), ".sql") {
		if out, failed := runSymbolContext(ctx, cfg, tc, symbolcontext.Args{Relation: "dependencies", File: path, Limit: changedSymbolContextLimit}); usefulSymbolOutput(out, failed) {
			blocks = append(blocks, out)
		}
	}
	if len(blocks) == 0 {
		return ""
	}
	return strings.Join(blocks, "\n")
}

func runSymbolContext(ctx stdctx.Context, cfg config.SymbolContext, tc symbolcontext.Context, args symbolcontext.Args) (string, bool) {
	raw, err := json.Marshal(args)
	if err != nil {
		return "", true
	}
	return symbolcontext.Run(ctx, cfg, tc, -1, raw)
}

func usefulSymbolOutput(out string, failed bool) bool {
	if failed {
		return false
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return false
	}
	emptyMarkers := []string{
		symbolcontext.NoSymbolsDetectedMarker,
		symbolcontext.NoSymbolsFoundMarker,
		symbolcontext.NoDependenciesFoundMarker,
		symbolcontext.NoSymbolContextMarker,
	}
	for _, marker := range emptyMarkers {
		if strings.Contains(out, marker) {
			return false
		}
	}
	return true
}

func joinPromptContext(parts ...string) string {
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n\n")
}

func truncateUTF8Bytes(blob []byte, n int) []byte {
	if len(blob) <= n {
		return blob
	}
	cut := n
	for cut > 0 && cut < len(blob) && !utf8.RuneStart(blob[cut]) {
		cut--
	}
	return blob[:cut]
}

// changedHunksOf groups the added+deleted code lines per diff hunk: the read-path
// code-anchor representation. Hunk granularity (a few lines per chunk) keeps the
// read query chunks size-comparable to the write path's per-finding QuotedCode
// anchors, so cosine search compares like with like instead of one whole-diff
// blob against many tiny candidate vectors.
func changedHunksOf(selected []diff.Diff) [][]string {
	var out [][]string
	for _, d := range selected {
		for _, h := range diff.ParseHunks(d.Diff) {
			var lines []string
			for _, l := range h.Lines {
				if l.Type == diff.HunkContext {
					continue
				}
				if strings.TrimSpace(l.Content) == "" {
					continue
				}
				lines = append(lines, l.Content)
			}
			if len(lines) > 0 {
				out = append(out, lines)
			}
		}
	}
	return out
}

// changedPathsOf derives forward-slash relative paths from the selected diffs
// (NewPath, plus OldPath for renames) for glob matching.
func changedPathsOf(selected []diff.Diff) []string {
	out := make([]string, 0, len(selected)*2) // worst case: a rename adds OldPath + NewPath
	seen := map[string]bool{}
	add := func(p string) {
		p = filepath.ToSlash(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, d := range selected {
		add(d.NewPath)
		if d.IsRenamed {
			add(d.OldPath)
		}
	}
	return out
}

// diffBudget splits the caller's total token budget between the diff and the
// rules section. The rules cap is bounded to at most half the total so the diff
// always keeps a usable share even when rulesCap (default ~4096) dwarfs a small
// total; the result is clamped to [1, total]. A disabled total (<=0) stays
// disabled; a non-positive rulesCap means no rules section, so the diff gets the
// whole total.
func diffBudget(total, rulesCap int) int {
	if total <= 0 {
		return total
	}
	if rulesCap <= 0 {
		return total
	}
	b := total - min(rulesCap, total/2)
	if b < 1 {
		return 1
	}
	return b
}

// baseRef is the base operand the diff compared against, by mode: the range
// <from>, the commit's parent expression, or "" for the staged index (vs HEAD).
func baseRef(req Request) string {
	switch req.Mode {
	case diff.ModeRange:
		return req.From
	case diff.ModeCommit:
		if req.Commit == "" {
			return ""
		}
		return req.Commit + "^"
	default:
		return ""
	}
}

// redactTrace returns a copy of t with secrets removed two ways: structured
// fields that could carry a credential are blanked (defensive: model/provider
// are non-secret but Provider literals stay), and config.RedactString runs over
// every free-text field (system/user prompt, injected-rule stems, final
// response) so a token embedded in the diff or prompt prose is masked too. The
// trace is LOCAL-only; this guards the on-disk state.db.
func redactTrace(t ReviewTrace) ReviewTrace {
	t.Sink = nil
	t.SystemPrompt = config.RedactString(t.SystemPrompt)
	t.UserPrompt = config.RedactString(t.UserPrompt)
	t.FinalResponse = config.RedactString(t.FinalResponse)
	t.DiffMeta.BaseSHA = config.RedactString(t.DiffMeta.BaseSHA)
	t.DiffMeta.HeadSHA = config.RedactString(t.DiffMeta.HeadSHA)
	rules := make([]RuleRef, len(t.InjectedRules))
	for i, r := range t.InjectedRules {
		rules[i] = RuleRef{Stem: config.RedactString(r.Stem), Provenance: r.Provenance}
	}
	t.InjectedRules = rules
	files := make([]string, len(t.SelectedFiles))
	for i, f := range t.SelectedFiles {
		files[i] = config.RedactString(f)
	}
	t.SelectedFiles = files
	turns := make([]TurnRecord, len(t.Turns))
	for i, tr := range t.Turns {
		turns[i] = TurnRecord{
			Turn:            tr.Turn,
			Tool:            tr.Tool,
			Args:            config.RedactString(tr.Args),
			Result:          config.RedactString(tr.Result),
			Error:           tr.Error,
			ResultTruncated: tr.ResultTruncated,
		}
	}
	t.Turns = turns
	// Reasoning quotes diff content; redact text while preserving provider/tokens metadata.
	if t.Reasoning != nil {
		r := *t.Reasoning
		r.Text = config.RedactString(r.Text)
		t.Reasoning = &r
	}
	return t
}

func modeName(m diff.Mode) string {
	switch m {
	case diff.ModeStaged:
		return "staged"
	case diff.ModeRange:
		return "range"
	case diff.ModeCommit:
		return "commit"
	default:
		return "unknown"
	}
}

// dropDrift removes findings the anchor could not place (Line==0): drift-reject.
func dropDrift(findings []Finding) []Finding {
	out := make([]Finding, 0, len(findings))
	for _, f := range findings {
		if f.Line == 0 {
			continue
		}
		out = append(out, f)
	}
	return out
}

// dedupe collapses findings sharing file+line+category AND a short hash of
// rationale+patch, so two distinct findings on the same line/category survive.
func dedupe(findings []Finding) []Finding {
	seen := make(map[string]bool, len(findings))
	out := make([]Finding, 0, len(findings))
	for _, f := range findings {
		key := fmt.Sprintf("%s|%d|%s|%s", f.File, f.Line, f.Category, proseHash(f))
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}

func proseHash(f Finding) string {
	sum := sha256.Sum256([]byte(f.Rationale + "\x00" + f.SuggestedPatch))
	return hex.EncodeToString(sum[:6])
}

// maxSeverity returns the highest-ranked finding's normalized severity for the
// stats.max_severity field, or "none" when no finding carries a recognized
// severity (including the empty set). Mirrors the gate's ranking so the reported
// max and the gate decision never disagree.
// MaxSeverity returns the worst severity in findings ("none" when empty),
// exported so the store can project it into a ReviewSummary without redefining
// the severity ranking.
func MaxSeverity(findings []Finding) string { return maxSeverity(findings) }

func maxSeverity(findings []Finding) string {
	maxRank, maxSev := 0, ""
	for _, f := range findings {
		if r := rankOf(f.Severity); r > maxRank {
			maxRank, maxSev = r, normSeverity(f.Severity)
		}
	}
	if maxSev == "" {
		return "none"
	}
	return maxSev
}

// MaxSeverityRank returns the numeric rank of a finding set's worst severity.
func MaxSeverityRank(findings []Finding) int {
	max := 0
	for _, f := range findings {
		if r := rankOf(f.Severity); r > max {
			max = r
		}
	}
	return max
}

// GateFailed reports whether the findings' max severity reaches the gate. An
// unrecognized gate fails loudly (returns true) so a misconfigured invocation
// never silently passes a PR that should have failed; the CLI boundary rejects
// bad gates earlier, this is defense-in-depth.
func GateFailed(findings []Finding, gate string) bool {
	g, ok := gateRank(gate)
	if !ok {
		return true
	}
	if g == 0 {
		return false
	}
	return MaxSeverityRank(findings) >= g
}
