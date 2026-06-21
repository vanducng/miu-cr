package github

import (
	"strings"

	"github.com/vanducng/miu-cr/internal/engine"
)

// suggestionSeverityFloor is the lowest severity at which --suggest emits a
// native one-click suggestion; below it the plain fenced hint is used instead.
const suggestionSeverityFloor = "medium"

// severityRankOf returns the engine's (low→critical) numeric rank of a severity,
// the same scale GateFailed uses — NOT the inverted github.severityRank.
func severityRankOf(sev string) int {
	return engine.MaxSeverityRank([]engine.Finding{{Severity: sev}})
}

// meetsSuggestionFloor reports whether a finding's severity is at or above the
// native-suggestion floor.
func meetsSuggestionFloor(sev string) bool {
	return severityRankOf(sev) >= severityRankOf(suggestionSeverityFloor)
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

// isCleanReplacement decides whether f.SuggestedPatch is a safe verbatim
// single-line replacement of the raw new-file line at f.Line, returning the
// suggestion text to emit and ok=true only then.
//
// Best-effort: the engine offers NO guarantee that SuggestedPatch corresponds to
// the anchored range — it is a free-form model field. The safe fallback for every
// rejected case is the plain fenced hint. ok is true ONLY when ALL hold:
//   - single-line: EndLine==0 || EndLine==Line
//   - SuggestedPatch is a single (non-empty) line
//   - the raw NewFileContent line at f.Line exists (1-based)
//   - normalizeLine(rawLine) == normalizeLine(f.QuotedCode): proves the raw line
//     at f.Line IS the anchored line. f.Line can be an OLD-file number when the
//     anchor resolver falls back to the old side; without this re-match a
//     suggestion could replace an unrelated new-file line.
//   - the patch is not a no-op (differs from the whitespace-trimmed raw line;
//     +/- are NOT stripped from the patch, which may be operator-prefixed code)
func isCleanReplacement(f engine.Finding, newFileContent string) (string, bool) {
	if f.EndLine != 0 && f.EndLine != f.Line {
		return "", false
	}
	if f.Line <= 0 {
		return "", false
	}

	patch := strings.TrimRight(strings.TrimSpace(f.SuggestedPatch), "\r")
	if patch == "" || strings.Contains(patch, "\n") {
		return "", false
	}

	lines := strings.Split(newFileContent, "\n")
	if f.Line > len(lines) {
		return "", false
	}
	rawLine := strings.TrimRight(lines[f.Line-1], "\r")

	if normalizeLine(rawLine) != normalizeLine(f.QuotedCode) {
		return "", false
	}
	// No-op check compares with whitespace-trim ONLY — never strip +/- from the
	// patch: SuggestedPatch is replacement CODE that can legitimately begin with
	// +/- (e.g. an arithmetic `+offset`), so normalizing it would wrongly flag a
	// real fix as a no-op. QuotedCode anchoring above keeps normalizeLine.
	if strings.TrimSpace(rawLine) == patch {
		return "", false
	}
	return patch, true
}
