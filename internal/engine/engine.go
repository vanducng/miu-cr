// Package engine composes the review pillars (diff acquisition, file selection,
// context assembly, the LLM pass, and drift-reject anchoring) into a single
// Review call producing a gated ReviewResult.
package engine

import (
	stdctx "context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	enginectx "github.com/vanducng/miu-cr/internal/engine/context"
	"github.com/vanducng/miu-cr/internal/engine/diff"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
	"github.com/vanducng/miu-cr/internal/rules"
)

// PersistRecord is the engine's view of a persisted review. The store package
// adapts it to its concrete ReviewRecord; defined here so engine sits below
// store in the import graph (store imports engine.Finding).
type PersistRecord struct {
	ID        string
	RepoDir   string
	Mode      string
	HeadSHA   string
	CreatedAt time.Time
	Findings  []Finding
	Stats     map[string]any
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
	RepoDir         string
	Rev             string
	Runner          *gitcmd.Runner
}

// Retriever is the engine-local seam for M7 semantic recall: wire injects an
// implementation that scrubs+embeds the current changed code and returns prior
// cosine-near findings. The engine does NO embedding/DB/network itself (it
// imports neither openai-go nor pgx), exactly like the Rules injection. nil =>
// the default M6 path (no SemanticContext).
type Retriever interface {
	Related(ctx stdctx.Context, changedCode []string) (string, error)
}

// Agent runs one review pass over the assembled context and returns findings
// WITHOUT line numbers (the engine re-anchors from QuotedCode).
type Agent interface {
	Review(ctx stdctx.Context, rc AgentContext) ([]Finding, error)
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

	rulesText, rulesApplied, rulesTruncated := e.buildRules(req, selected)

	assembled := enginectx.AssembleContext(selected, enginectx.AssembleOptions{
		TokenBudget:  diffBudget(req.TokenBudget, req.RulesTokenBudget),
		ExpandWindow: req.ExpandWindow,
	})

	semanticContext, semanticStat := retrieveSemantic(ctx, req.Retriever, selected)

	rev := selected[0].Ref
	raw, err := e.Agent.Review(ctx, AgentContext{
		Text:            assembled.Text,
		Rules:           rulesText,
		SemanticContext: semanticContext,
		RepoDir:         req.RepoDir,
		Rev:             rev,
		Runner:          e.Runner,
	})
	if err != nil {
		return ReviewResult{}, err
	}

	if anchorLineNumbers == nil {
		return ReviewResult{}, &clierr.CLIError{Code: "engine.no_anchorer", Message: "anchoring not wired", Exit: 1}
	}
	anchored := anchorLineNumbers(raw, selected)
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

	result := ReviewResult{Findings: kept, Stats: stats}

	if e.Store != nil {
		headSHA, _ := e.Runner.HeadSHA(ctx, req.RepoDir)
		id, serr := e.Store.SaveReview(ctx, PersistRecord{
			RepoDir:   req.RepoDir,
			Mode:      modeName(req.Mode),
			HeadSHA:   headSHA,
			CreatedAt: time.Now().UTC(),
			Findings:  kept,
			Stats:     stats,
		})
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
func (e *Engine) buildRules(req Request, selected []diff.Diff) (text string, applied int, truncated bool) {
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
	code := changedCodeOf(selected)
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

// changedCodeOf collects the added+deleted code lines across the selected diffs:
// the code-anchor representation embedded on the read path, matching the write
// path which embeds each finding's QuotedCode (also changed code).
func changedCodeOf(selected []diff.Diff) []string {
	var out []string
	for _, d := range selected {
		for _, h := range diff.ParseHunks(d.Diff) {
			for _, l := range h.Lines {
				if l.Type == diff.HunkContext {
					continue
				}
				if strings.TrimSpace(l.Content) == "" {
					continue
				}
				out = append(out, l.Content)
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
