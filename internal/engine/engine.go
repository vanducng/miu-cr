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

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	"github.com/vanducng/miu-cr/internal/config"
	enginectx "github.com/vanducng/miu-cr/internal/engine/context"
	"github.com/vanducng/miu-cr/internal/engine/diff"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
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

// TurnRecord is one tool dispatch in the review session (turn index, tool name,
// raw args). Args are paths/patterns — no auth tokens.
type TurnRecord struct {
	Turn int    `json:"turn"`
	Tool string `json:"tool"`
	Args string `json:"args"`
}

// DiffMeta identifies which revisions the review compared and how that diff was
// computed (staged index / commit / range).
type DiffMeta struct {
	BaseSHA string `json:"base_sha"`
	HeadSHA string `json:"head_sha"`
	Source  string `json:"source"`
}

// RuleRef is one injected rule's identity for the trace: its file stem and trust
// provenance (e.g. built-in / user / repo). No rule body — selection identity only.
type RuleRef struct {
	Stem       string `json:"stem"`
	Provenance string `json:"provenance"`
}

// ReviewTrace accumulates the full session content during a review for
// persistence: the system + user prompts, diff identification, selected files,
// injected rules, model/provider, the per-turn tool calls, and the raw final
// response. The agent/engine record into it via nil-safe setters (mirroring the
// Progress seam); the engine creates it, threads it onto AgentContext, and reads
// it back after Review. A nil *ReviewTrace makes every recorder a no-op (capture
// disabled). The content (system/user prompt + diff) is captured in full by
// design — stored LOCAL-only in the gitignored state.db, never in the review
// envelope or a posted comment; the invariant is no auth tokens (redacted at
// persist), not "no code".
//
// Sink, when non-nil, is invoked by each setter with the step name + the recorded
// payload — phase 2 wires it to live --trace NDJSON on stderr; nil = persist-only.
type ReviewTrace struct {
	SystemPrompt  string                         `json:"system_prompt"`
	UserPrompt    string                         `json:"user_prompt"`
	DiffMeta      DiffMeta                       `json:"diff_meta"`
	SelectedFiles []string                       `json:"selected_files"`
	InjectedRules []RuleRef                      `json:"injected_rules"`
	Model         string                         `json:"model"`
	Provider      string                         `json:"provider"`
	FinalResponse string                         `json:"final_response"`
	Turns         []TurnRecord                   `json:"turns"`
	Sink          func(step string, payload any) `json:"-"`
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
	if t.Provider == "" {
		t.Provider = provider
	}
	if t.Model == "" {
		t.Model = model
	}
	t.emit("model", map[string]string{"provider": provider, "model": model})
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
	// WantDiagram opts into the mermaid change diagram. LOCKSTEP: mirror this field
	// everywhere Rules/SemanticContext are threaded or it is silently dropped.
	WantDiagram bool
	RepoDir     string
	Rev         string
	Runner      *gitcmd.Runner
	Progress    func(string) // nil = silent; milestone strings only, never secrets
	// Trace, when non-nil, captures the raw prompt, per-turn tool calls, and raw
	// final response for persistence. nil = no capture (mirrors Progress).
	Trace *ReviewTrace
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
// walkthrough/per-file digest the same pass may emit.
type Agent interface {
	Review(ctx stdctx.Context, rc AgentContext) (ReviewOutput, error)
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

	// Progress is the optional milestone sink (stderr); nil = silent. The wire/cli
	// layer builds it from --verbose/--quiet + a TTY check. Only milestone strings
	// and file paths/tool names ever reach it — never tokens.
	Progress func(string)

	// TraceSink, when non-nil, is wired onto the ReviewTrace.Sink so each capture
	// seam emits a live step (cli's --trace renders NDJSON to stderr). Distinct from
	// Progress; nil = persist-only (no live stream). Capture still runs regardless,
	// so the live stream and the persisted trace stay consistent by construction.
	TraceSink func(step string, payload any)

	// Persist context copied onto the saved PersistRecord (no secrets): the resolved
	// provider/model and, on the --pr path, the PR owner/repo/number. Local reviews
	// leave Owner/Repo/Number zero.
	Provider string
	Model    string
	Owner    string
	Repo     string
	Number   int
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

	var trace *ReviewTrace
	if e.Store != nil || req.TraceSink != nil {
		trace = &ReviewTrace{Sink: req.TraceSink}
		headSHA, _ := e.Runner.HeadSHA(ctx, req.RepoDir)
		trace.SetDiffMeta(DiffMeta{BaseSHA: baseRef(req), HeadSHA: headSHA, Source: modeName(req.Mode)})
		trace.SetSelectedFiles(changedPathsOf(selected))
	}

	rulesText, rulesApplied, rulesTruncated := e.buildRules(req, selected, trace)

	assembled := enginectx.AssembleContext(selected, enginectx.AssembleOptions{
		TokenBudget:  diffBudget(req.TokenBudget, req.RulesTokenBudget),
		ExpandWindow: req.ExpandWindow,
	})

	req.progress(fmt.Sprintf("reviewing %d files (%d changed)…", len(selected), len(diffs)))
	if lvl, _ := assembled.Stats["truncation_level"].(string); lvl != "" && lvl != "full" {
		req.progress("diff compressed: " + lvl)
	}

	semanticContext, semanticStat := retrieveSemantic(ctx, req.Retriever, selected)

	trace.SetModel(req.Provider, req.Model)

	rev := selected[0].Ref
	out, err := e.Agent.Review(ctx, AgentContext{
		Text:            assembled.Text,
		Rules:           rulesText,
		SemanticContext: semanticContext,
		WantDiagram:     req.WantDiagram,
		RepoDir:         req.RepoDir,
		Rev:             rev,
		Runner:          e.Runner,
		Progress:        req.Progress,
		Trace:           trace,
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
	}
	if semanticStat != "" {
		stats["semantic_recall"] = semanticStat
	}

	result := ReviewResult{Findings: kept, Walkthrough: out.Walkthrough, FileSummaries: out.FileSummaries, Diagram: out.Diagram, Stats: stats}

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
			if len(trace.Turns) > 0 {
				if blob, merr := json.Marshal(trace.Turns); merr != nil {
					stats["transcript_error"] = merr.Error()
				} else {
					rec.Transcript = blob
				}
			}
			if blob, merr := json.Marshal(redactTrace(*trace)); merr != nil {
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
	return rules.BuildRulesSection(picked, !req.RulesFork, req.RulesTokenBudget)
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
// fields that could carry a credential are blanked (defensive — model/provider
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
		turns[i] = TurnRecord{Turn: tr.Turn, Tool: tr.Tool, Args: config.RedactString(tr.Args)}
	}
	t.Turns = turns
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
