package engine

import (
	stdctx "context"
	"sort"
	"strings"

	"github.com/vanducng/miu-cr/internal/engine/diff"
)

// maxAnchorRecovery caps the per-review anchor-recovery LLM calls; candidates
// past the cap stay dropped (highest severity gets the budget first).
const maxAnchorRecovery = 3

// anchorRecoveryOutcome is what the recovery pass reports back for stats/metering.
type anchorRecoveryOutcome struct {
	attempted  int
	recovered  int
	skippedCap int
	usage      Usage
}

// recoverAnchors is the gated, bounded second pass for drift-rejected findings:
// for each finding whose QuotedCode failed anchoring (Line==0) at severity >=
// medium, it makes ONE focused RelocateQuote call giving the finding (file,
// rationale, failed quote) plus that file's diff excerpt, then re-runs the SAME
// deterministic anchorer on the reply. The relocated quote is committed onto the
// finding ONLY if it now anchors — the exact-anchor guarantee never weakens —
// otherwise the finding drops exactly as before. OFF (req.AnchorRecovery false)
// is byte-identical: no calls, no stats. On any error it keeps the original
// (dropped) finding and NEVER fails the review.
func (e *Engine) recoverAnchors(ctx stdctx.Context, anchored []Finding, selected []diff.Diff, req Request, trace *ReviewTrace) ([]Finding, anchorRecoveryOutcome) {
	var out anchorRecoveryOutcome
	if !req.AnchorRecovery || e.Agent == nil || anchorLineNumbers == nil {
		return anchored, out
	}

	byPath := make(map[string]*diff.Diff, len(selected))
	for i := range selected {
		d := &selected[i]
		if d.NewPath != "" && d.NewPath != "/dev/null" {
			byPath[d.NewPath] = d
		}
		if d.OldPath != "" && d.OldPath != "/dev/null" {
			byPath[d.OldPath] = d
		}
	}

	type candidate struct {
		idx  int
		rank int
	}
	var cands []candidate
	floor := rankOf("medium")
	for i := range anchored {
		f := anchored[i]
		if f.Line != 0 { // anchored fine, nothing to recover
			continue
		}
		rank := rankOf(f.Severity)
		if rank < floor {
			continue
		}
		if strings.TrimSpace(f.QuotedCode) == "" { // no failed quote to relocate from
			continue
		}
		if _, ok := byPath[f.File]; !ok { // no reviewed file to excerpt
			continue
		}
		cands = append(cands, candidate{idx: i, rank: rank})
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].rank > cands[j].rank })
	if len(cands) > maxAnchorRecovery {
		out.skippedCap = len(cands) - maxAnchorRecovery
		cands = cands[:maxAnchorRecovery]
	}

	for _, c := range cands {
		f := anchored[c.idx]
		out.attempted++
		reply, u, err := e.Agent.RelocateQuote(ctx, RelocateRequest{
			File:          f.File,
			Rationale:     f.Rationale,
			QuotedCode:    f.QuotedCode,
			Excerpt:       relocateExcerpt(byPath[f.File]),
			Category:      f.Category,
			Severity:      f.Severity,
			ProviderRetry: req.ProviderRetry,
		})
		out.usage.Add(u) // count tokens even for a failed relocation — they were spent
		if err == nil && strings.TrimSpace(reply) != "" {
			probe := f
			probe.QuotedCode = reply
			if res := anchorLineNumbers([]Finding{probe}, selected); len(res) == 1 && res[0].Line != 0 {
				anchored[c.idx].QuotedCode = reply
				anchored[c.idx].Line = res[0].Line
				anchored[c.idx].EndLine = res[0].EndLine
				out.recovered++
				trace.RecordAnchorRecovery(AnchorRecoveryRecord{File: f.File, Severity: f.Severity, Recovered: true, Line: res[0].Line})
				continue
			}
		}
		trace.RecordAnchorRecovery(AnchorRecoveryRecord{File: f.File, Severity: f.Severity})
	}
	return anchored, out
}

// relocateExcerpt is the file-scoped context for one relocation call: the
// file's unified diff when present, else the raw new-file content. Rune-capped
// downstream by the prompt builder.
func relocateExcerpt(d *diff.Diff) string {
	if strings.TrimSpace(d.Diff) != "" {
		return d.Diff
	}
	return d.NewFileContent
}
