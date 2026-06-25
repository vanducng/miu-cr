package github

import (
	"strings"

	"github.com/vanducng/miu-cr/internal/engine"
)

// suggestionSeverityFloor is the lowest severity at which --suggest emits a
// native one-click suggestion; below it the plain fenced hint is used instead.
const suggestionSeverityFloor = "medium"

// severityRankOf returns the engine's (low→critical) numeric rank of a severity,
// the same scale GateFailed uses, NOT the inverted github.severityRank.
func severityRankOf(sev string) int {
	return engine.MaxSeverityRank([]engine.Finding{{Severity: sev}})
}

// meetsSuggestionFloor reports whether a finding's severity is at or above the
// native-suggestion floor.
func meetsSuggestionFloor(sev string) bool {
	return severityRankOf(sev) >= severityRankOf(suggestionSeverityFloor)
}

// repairReason classifies why classifyReplacement accepted or rejected a patch.
// Only the "repairable" subset is worth a second LLM pass; the rest are anchoring
// bugs a patch-only re-prompt cannot fix.
type repairReason int

const (
	reasonOK repairReason = iota
	reasonNoAnchor
	reasonOutOfRange
	reasonEmpty
	reasonNoOp
	reasonAnchorMismatch
	reasonGarbledSpan
	reasonLengthMismatch
)

func (r repairReason) String() string {
	switch r {
	case reasonOK:
		return "ok"
	case reasonNoAnchor:
		return "no_anchor"
	case reasonOutOfRange:
		return "out_of_range"
	case reasonEmpty:
		return "empty"
	case reasonNoOp:
		return "no_op"
	case reasonAnchorMismatch:
		return "anchor_mismatch"
	case reasonGarbledSpan:
		return "garbled_span"
	case reasonLengthMismatch:
		return "length_mismatch"
	default:
		return "unknown"
	}
}

// repairable reports whether a rejection is worth a second LLM pass: only an
// empty/no-op/garbled-span/length-mismatch patch can be fixed by re-prompting for
// the replacement code. A true anchor mismatch (or no/out-of-range anchor) is an
// anchoring bug, not a patch problem, so it is NOT repairable.
func (r repairReason) repairable() bool {
	switch r {
	// A garbled span (EndLine set but != Line) is structural: it lives in the
	// finding's EndLine, which repair never changes, so re-prompting can't fix it.
	case reasonEmpty, reasonNoOp, reasonLengthMismatch:
		return true
	default:
		return false
	}
}

// ClassifyReplacement is the exported re-validation seam the engine injects from
// wire (mirroring Anchorer), keeping internal/engine free of an internal/github
// import. It returns the suggestion text, a stable lowercase reason string, and
// whether that reason is worth a repair pass.
func ClassifyReplacement(f engine.Finding, newFileContent string) (patch string, reason string, repairable bool) {
	p, r := classifyReplacement(f, newFileContent)
	return p, r.String(), r.repairable()
}

// normalizeLine mirrors the anchor resolver's normalization: trim surrounding
// whitespace and strip a single leading +/- diff marker, then trim again. Kept
// local so isCleanReplacement re-matches against the SAME normalization the
// anchorer used, without exporting it from internal/engine/anchor.
func normalizeLine(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "+")
	s = strings.TrimPrefix(s, "-")
	return strings.TrimSpace(s)
}

// isCleanReplacement decides whether f.SuggestedPatch is a safe replacement of the
// raw new-file line at f.Line, returning the suggestion text to emit and ok=true
// only then. The patch MAY span multiple lines (a wrap/guard/insert fix): GitHub
// replaces exactly the single anchored line f.Line with the whole block, so once the
// anchor is QuotedCode-proven a multi-line patch is a safe in-place expansion.
//
// Best-effort: the engine offers NO guarantee that SuggestedPatch corresponds to
// the anchored range, it is a free-form model field. The safe fallback for every
// rejected case is the plain fenced hint. ok is true ONLY when ALL hold:
//   - single-line anchor: EndLine==0 || EndLine==Line
//   - SuggestedPatch is non-empty (it may be a single line OR a multi-line block)
//   - the raw NewFileContent line at f.Line exists (1-based)
//   - normalizeLine(rawLine) == normalizeLine(f.QuotedCode): proves the raw line
//     at f.Line IS the anchored line. f.Line can be an OLD-file number when the
//     anchor resolver falls back to the old side; without this re-match a
//     suggestion could replace an unrelated new-file line.
//   - the patch is not a no-op (differs from the whitespace-trimmed raw line;
//     +/- are NOT stripped from the patch, which may be operator-prefixed code)
func isCleanReplacement(f engine.Finding, newFileContent string) (string, bool) {
	s, r := classifyReplacement(f, newFileContent)
	return s, r == reasonOK
}

// classifyReplacement implements the single- and multi-line accept/reject logic of
// isCleanReplacement, returning the precise rejection reason for the repair loop.
// The accept/reject decision is byte-for-byte identical to the original
// isCleanReplacement/cleanMultiLineReplacement; only the reason is new.
func classifyReplacement(f engine.Finding, newFileContent string) (string, repairReason) {
	if f.Line <= 0 {
		return "", reasonNoAnchor
	}
	if f.EndLine > f.Line {
		return classifyMultiLineReplacement(f, newFileContent)
	}
	if f.EndLine != 0 && f.EndLine != f.Line {
		return "", reasonGarbledSpan
	}

	patch := strings.TrimRight(strings.TrimSpace(f.SuggestedPatch), "\r")
	if patch == "" {
		return "", reasonEmpty
	}

	lines := strings.Split(newFileContent, "\n")
	if f.Line > len(lines) {
		return "", reasonOutOfRange
	}
	rawLine := strings.TrimRight(lines[f.Line-1], "\r")

	if normalizeLine(rawLine) != normalizeLine(f.QuotedCode) {
		return "", reasonAnchorMismatch
	}
	// No-op check whitespace-trims BOTH sides (consistent with the multi-line path)
	// but never strips +/-: SuggestedPatch is replacement CODE that can legitimately
	// begin with +/- (e.g. an arithmetic `+offset`), so normalizing it would wrongly
	// flag a real fix as a no-op. QuotedCode anchoring above keeps normalizeLine.
	if strings.TrimSpace(rawLine) == strings.TrimSpace(patch) {
		return "", reasonNoOp
	}
	return patch, reasonOK
}

// cleanMultiLineReplacement proves a multi-line one-click suggestion is safe: the
// span Line..EndLine must exist in the new file AND its QuotedCode must match those
// raw lines verbatim (per-line normalized), so the patch replaces EXACTLY the
// anchored on-diff block. Any mismatch (length, content, no-op) rejects → the
// caller falls back to a plain fenced hint, never a one-click multi-line apply.
func cleanMultiLineReplacement(f engine.Finding, newFileContent string) (string, bool) {
	s, r := classifyMultiLineReplacement(f, newFileContent)
	return s, r == reasonOK
}

func classifyMultiLineReplacement(f engine.Finding, newFileContent string) (string, repairReason) {
	patch := strings.TrimRight(strings.TrimSpace(f.SuggestedPatch), "\r")
	if patch == "" {
		return "", reasonEmpty
	}

	raw := strings.Split(newFileContent, "\n")
	if f.EndLine > len(raw) {
		return "", reasonOutOfRange
	}
	span := make([]string, 0, f.EndLine-f.Line+1)
	for i := f.Line - 1; i < f.EndLine; i++ {
		span = append(span, strings.TrimRight(raw[i], "\r"))
	}

	quoted := strings.Split(strings.ReplaceAll(f.QuotedCode, "\r\n", "\n"), "\n")
	if len(quoted) != len(span) {
		return "", reasonLengthMismatch
	}
	for i := range span {
		if normalizeLine(span[i]) != normalizeLine(quoted[i]) {
			return "", reasonAnchorMismatch
		}
	}
	// No-op: the patch reproduces the span verbatim (whitespace-trimmed per line).
	if strings.Join(trimAll(span), "\n") == strings.Join(trimAll(strings.Split(patch, "\n")), "\n") {
		return "", reasonNoOp
	}
	return patch, reasonOK
}

func trimAll(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.TrimSpace(l)
	}
	return out
}
