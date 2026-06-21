// Package anchor re-anchors model findings to line numbers by matching their
// QuotedCode against the reviewed revision's diff hunks and file content.
package anchor

import (
	"strings"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/diff"
)

// ResolveLineNumbers re-anchors every finding from its QuotedCode, ALWAYS
// ignoring any model-supplied line number (Line/EndLine are zeroed on entry and
// recomputed). A QuotedCode absent at the reviewed revision resolves to Line==0
// (drift-reject); the engine drops such findings. The slice is returned with
// Line/EndLine populated in place.
func ResolveLineNumbers(findings []engine.Finding, diffs []diff.Diff) []engine.Finding {
	if len(findings) == 0 || len(diffs) == 0 {
		return findings
	}

	byPath := make(map[string]*diff.Diff, len(diffs))
	for i := range diffs {
		d := &diffs[i]
		if d.NewPath != "/dev/null" && d.NewPath != "" {
			byPath[d.NewPath] = d
		}
		if d.OldPath != "/dev/null" && d.OldPath != "" {
			byPath[d.OldPath] = d
		}
	}

	for i := range findings {
		f := &findings[i]
		f.Line = 0
		f.EndLine = 0
		if f.QuotedCode == "" {
			continue
		}
		d, ok := byPath[f.File]
		if !ok {
			continue
		}
		ResolveFinding(f, d)
	}

	return findings
}

// ResolveFinding re-anchors a single finding against one diff. It unconditionally
// recomputes Line/EndLine from QuotedCode. Returns true when anchored to a
// non-zero line. Auto-rejects an empty-after-normalize quote.
func ResolveFinding(f *engine.Finding, d *diff.Diff) bool {
	f.Line = 0
	f.EndLine = 0
	if f.QuotedCode == "" {
		return false
	}
	target := splitAndNormalize(f.QuotedCode)
	if len(target) == 0 {
		return false
	}
	if resolveFromHunk(f, d, target) {
		return true
	}
	return resolveFromFileContent(f, d, target)
}

// indexedLine pairs a normalized line with its absolute file line number.
type indexedLine struct {
	lineNum int
	content string
}

// resolveFromHunk matches target against hunk lines, new-side first (context +
// added → new-file numbers), then old-side (context + deleted → old-file numbers).
func resolveFromHunk(f *engine.Finding, d *diff.Diff, target []string) bool {
	hunks := diff.ParseHunks(d.Diff)
	if len(hunks) == 0 {
		return false
	}
	for i := range hunks {
		if start, end, ok := matchConsecutive(extractSideLines(&hunks[i], true), target); ok {
			f.Line, f.EndLine = start, end
			return true
		}
	}
	for i := range hunks {
		if start, end, ok := matchConsecutive(extractSideLines(&hunks[i], false), target); ok {
			f.Line, f.EndLine = start, end
			return true
		}
	}
	return false
}

// extractSideLines returns one side of a hunk with absolute line numbers.
// newSide=true → context+added (new-file numbers); false → context+deleted (old-file numbers).
// Blank-after-normalize lines are dropped so the side matches splitAndNormalize's
// target (which also drops blanks), keeping the surviving lines' real numbers.
func extractSideLines(hunk *diff.Hunk, newSide bool) []indexedLine {
	var result []indexedLine
	oldLine := hunk.OldStart
	newLine := hunk.NewStart
	add := func(num int, content string) {
		if n := normalizeLine(content); n != "" {
			result = append(result, indexedLine{num, n})
		}
	}
	for _, l := range hunk.Lines {
		switch l.Type {
		case diff.HunkContext:
			if newSide {
				add(newLine, l.Content)
			} else {
				add(oldLine, l.Content)
			}
			oldLine++
			newLine++
		case diff.HunkAdded:
			if newSide {
				add(newLine, l.Content)
			}
			newLine++
		case diff.HunkDeleted:
			if !newSide {
				add(oldLine, l.Content)
			}
			oldLine++
		}
	}
	return result
}

// matchConsecutive finds the first consecutive run in sideLines equal to target.
func matchConsecutive(sideLines []indexedLine, target []string) (startLine, endLine int, found bool) {
	if len(target) == 0 || len(sideLines) < len(target) {
		return 0, 0, false
	}
	for i := 0; i <= len(sideLines)-len(target); i++ {
		matched := true
		for j, t := range target {
			if sideLines[i+j].content != t {
				matched = false
				break
			}
		}
		if matched {
			return sideLines[i].lineNum, sideLines[i+len(target)-1].lineNum, true
		}
	}
	return 0, 0, false
}

// resolveFromFileContent scans the mode-correct NewFileContent for a consecutive
// match. First-match-wins; tried only after both hunk sides miss (hunk-side
// preferred). Blank-after-normalize lines are dropped (matching target), so quoted
// code spanning interior blanks still anchors. Duplicated snippets are an accepted
// ambiguity for M1.
func resolveFromFileContent(f *engine.Finding, d *diff.Diff, target []string) bool {
	if d.NewFileContent == "" {
		return false
	}
	rawLines := strings.Split(d.NewFileContent, "\n")
	fileLines := make([]indexedLine, 0, len(rawLines))
	for i, raw := range rawLines {
		if n := normalizeLine(strings.TrimRight(raw, "\r")); n != "" {
			fileLines = append(fileLines, indexedLine{i + 1, n})
		}
	}
	if start, end, ok := matchConsecutive(fileLines, target); ok {
		f.Line, f.EndLine = start, end
		return true
	}
	return false
}

// splitAndNormalize splits code into normalized non-blank lines.
// Invariant: blank-after-normalize lines are DROPPED here (so quoted code with
// interior blanks still matches), whereas resolveFromFileContent keeps file
// blanks in place — both sides are compared only on the surviving target lines.
func splitAndNormalize(code string) []string {
	raw := strings.Split(code, "\n")
	result := make([]string, 0, len(raw))
	for _, line := range raw {
		if n := normalizeLine(line); n != "" {
			result = append(result, n)
		}
	}
	return result
}

// normalizeLine trims surrounding whitespace and strips a single leading +/-
// diff marker, then trims again.
func normalizeLine(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "+")
	s = strings.TrimPrefix(s, "-")
	return strings.TrimSpace(s)
}
