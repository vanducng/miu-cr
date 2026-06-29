package engine

import (
	stdctx "context"
	"sort"
	"strings"

	"github.com/vanducng/miu-cr/internal/engine/diff"
)

// defaultMaxRepair caps the per-review repair LLM calls when MaxRepair is unset.
const defaultMaxRepair = 5

// repairSpanRuneCap bounds the span sent to RepairPatch; a single-line span past
// this is skipped rather than burning a call on an oversized prompt.
const repairSpanRuneCap = 4000

// repairPatches is the gated, bounded second pass: for each single-line finding that
// meets the suggestion floor but whose SuggestedPatch was rejected for a REPAIRABLE
// reason, it makes ONE focused RepairPatch call giving the verbatim anchored span +
// the issue, then re-validates the reply with the SAME injected classifier; the new
// patch is committed onto the finding ONLY if it now passes, else the original is
// kept. OFF (req.PatchRepair && req.Post false) is byte-identical: no calls, no stat.
// On any error it falls back to the original finding and NEVER fails the review.
func (e *Engine) repairPatches(ctx stdctx.Context, kept []Finding, selected []diff.Diff, req Request, stats map[string]any) ([]Finding, Usage) {
	if !req.PatchRepair || !req.Post || e.Agent == nil || classifyReplacement == nil {
		return kept, Usage{}
	}

	newFileContent := make(map[string]string, len(selected))
	for i := range selected {
		if selected[i].NewPath != "" {
			newFileContent[selected[i].NewPath] = selected[i].NewFileContent
		}
	}

	type candidate struct {
		idx  int
		span string
		rank int
	}
	var cands []candidate
	floor := rankOf("medium")
	for i := range kept {
		f := kept[i]
		rank := rankOf(f.Severity)
		if rank < floor {
			continue
		}
		if f.Line <= 0 {
			continue
		}
		if f.EndLine != 0 && f.EndLine != f.Line { // single-line only for V1
			continue
		}
		_, _, repairable := classifyReplacement(f, newFileContent[f.File])
		if !repairable {
			continue
		}
		span, ok := spanLine(newFileContent[f.File], f.Line)
		if !ok {
			continue
		}
		if strings.Contains(span, "```") { // fenceSafe would defeat re-validation
			continue
		}
		if len([]rune(span)) > repairSpanRuneCap {
			continue
		}
		cands = append(cands, candidate{idx: i, span: span, rank: rank})
	}

	limit := req.MaxRepair
	if limit <= 0 {
		limit = defaultMaxRepair
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].rank > cands[j].rank })
	skippedCap := 0
	if len(cands) > limit {
		skippedCap = len(cands) - limit
		cands = cands[:limit]
	}

	attempted, repaired := 0, 0
	var usage Usage
	for _, c := range cands {
		attempted++
		f := kept[c.idx]
		reply, u, err := e.Agent.RepairPatch(ctx, RepairRequest{
			Span:          c.span,
			Rationale:     f.Rationale,
			Category:      f.Category,
			Severity:      f.Severity,
			ProviderRetry: req.ProviderRetry,
		})
		usage.Add(u) // count tokens even for a rejected/failed repair — they were spent
		if err != nil || strings.TrimSpace(reply) == "" {
			continue
		}
		probe := f
		probe.SuggestedPatch = reply
		if !cleanNow(probe, newFileContent[f.File]) {
			continue
		}
		kept[c.idx].SuggestedPatch = reply
		repaired++
	}

	stats["patch_repair"] = map[string]any{
		"attempted":             float64(attempted),
		"repaired":              float64(repaired),
		"skipped_cap":           float64(skippedCap),
		"input_tokens":          float64(usage.InputTokens),
		"output_tokens":         float64(usage.OutputTokens),
		"cache_read_tokens":     float64(usage.CacheReadTokens),
		"cache_creation_tokens": float64(usage.CacheCreationTokens),
	}
	return kept, usage
}

// cleanNow reports whether the (possibly repaired) finding now passes the injected
// re-validation gate (reason == "ok").
func cleanNow(f Finding, newFileContent string) bool {
	_, reason, _ := classifyReplacement(f, newFileContent)
	return reason == "ok"
}

// spanLine returns the raw (CR-trimmed) new-file line at the 1-based line number,
// the SAME line the validator anchors against. ok=false when out of range.
func spanLine(newFileContent string, line int) (string, bool) {
	lines := strings.Split(newFileContent, "\n")
	if line <= 0 || line > len(lines) {
		return "", false
	}
	return strings.TrimRight(lines[line-1], "\r"), true
}
