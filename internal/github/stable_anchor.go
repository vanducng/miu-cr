package github

import (
	"strings"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/diff"
)

// findingOnChangedCode is true when the finding describes text this delta added
// or deleted. A non-empty quote decides it: the line number is used only when
// the quote is empty. A new file is in scope as a whole.
func findingOnChangedCode(f engine.Finding, diffs []diff.Diff) bool {
	if f.File == "" {
		return false
	}
	for i := range diffs {
		d := &diffs[i]
		if d.NewPath == f.File && d.IsNew {
			return true
		}
	}
	if quoteHitsChanged(f, diffs) {
		return true
	}
	if strings.TrimSpace(f.QuotedCode) != "" {
		return false
	}
	return findingOnAddedLine(f, diffs)
}

// quoteHitsChanged reports whether every non-empty trimmed quote line equals a
// trimmed added or deleted line in the finding's file. One shared token such as
// a brace is not enough to keep the rest of an unchanged quote.
func quoteHitsChanged(f engine.Finding, diffs []diff.Diff) bool {
	changed := map[string]bool{}
	for i := range diffs {
		d := &diffs[i]
		if d.NewPath != f.File && d.OldPath != f.File {
			continue
		}
		for _, h := range diff.ParseHunks(d.Diff) {
			for _, l := range h.Lines {
				if l.Type == diff.HunkContext {
					continue
				}
				if t := strings.TrimSpace(l.Content); t != "" {
					changed[t] = true
				}
			}
		}
	}
	if len(changed) == 0 {
		return false
	}
	matched := false
	for _, line := range strings.Split(normalizeForFingerprint(f.QuotedCode), "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if !changed[t] {
			return false
		}
		matched = true
	}
	return matched
}

// UntouchedAnchors are prior open, reopened, or deferred fingerprints whose
// line the follow-up commits left in place. Nil delta returns nil. A deleted
// file, a file-level finding on a changed file, or a deleted line is touched.
func UntouchedAnchors(prior []LedgerEntry, delta []diff.Diff) map[string]bool {
	if delta == nil {
		return nil
	}
	out := map[string]bool{}
	for _, e := range prior {
		switch e.Status {
		case statusOpen, statusReopened, statusDeferred:
		default:
			continue
		}
		if e.FP != "" && !anchorRemoved(e, delta) {
			out[e.FP] = true
		}
	}
	return out
}

func anchorRemoved(e LedgerEntry, delta []diff.Diff) bool {
	_, _, removed := remapAnchor(e, delta)
	return removed
}

func findingOnAddedLine(f engine.Finding, diffs []diff.Diff) bool {
	if f.File == "" {
		return false
	}
	end := f.EndLine
	if end < f.Line {
		end = f.Line
	}
	for i := range diffs {
		d := &diffs[i]
		if d.NewPath != f.File {
			continue
		}
		if d.IsNew && f.Line <= 0 {
			return true
		}
		for _, h := range diff.ParseHunks(d.Diff) {
			n := h.NewStart
			for _, l := range h.Lines {
				switch l.Type {
				case diff.HunkContext:
					n++
				case diff.HunkAdded:
					if f.Line > 0 && n >= f.Line && n <= end {
						return true
					}
					n++
				}
			}
		}
	}
	return false
}
