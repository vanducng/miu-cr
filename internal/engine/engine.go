// Package engine composes the review pillars (diff acquisition, file selection,
// context assembly, the LLM pass, and drift-reject anchoring) into a single
// Review call producing a gated ReviewResult.
package engine

import (
	stdctx "context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	enginectx "github.com/vanducng/miu-cr/internal/engine/context"
	"github.com/vanducng/miu-cr/internal/engine/diff"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
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
	Text    string
	RepoDir string
	Rev     string
	Runner  *gitcmd.Runner
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

	assembled := enginectx.AssembleContext(selected, enginectx.AssembleOptions{
		TokenBudget:  req.TokenBudget,
		ExpandWindow: req.ExpandWindow,
	})

	rev := selected[0].Ref
	raw, err := e.Agent.Review(ctx, AgentContext{
		Text:    assembled.Text,
		RepoDir: req.RepoDir,
		Rev:     rev,
		Runner:  e.Runner,
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
