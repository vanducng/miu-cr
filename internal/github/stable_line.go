package github

import "github.com/vanducng/miu-cr/internal/engine/diff"

// ShiftedLines is the new-side line of a prior anchor so the next review's old side still matches it.
func ShiftedLines(prior []LedgerEntry, delta []diff.Diff) map[string]int {
	if delta == nil {
		return nil
	}
	out := map[string]int{}
	for _, e := range prior {
		switch e.Status {
		case statusOpen, statusReopened, statusDeferred:
		default:
			continue
		}
		if e.FP == "" {
			continue
		}
		line, removed := remapAnchor(e, delta)
		if removed || line <= 0 || line == e.Line {
			continue
		}
		out[e.FP] = line
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func applyShiftedLines(entries []LedgerEntry, lines map[string]int) []LedgerEntry {
	if len(lines) == 0 {
		return entries
	}
	out := append([]LedgerEntry(nil), entries...)
	for i := range out {
		n, ok := lines[out[i].FP]
		if !ok || n <= 0 {
			continue
		}
		switch out[i].Status {
		case statusOpen, statusReopened, statusDeferred:
			out[i].Line = n
		}
	}
	return out
}

func remapAnchor(e LedgerEntry, delta []diff.Diff) (line int, removed bool) {
	var match *diff.Diff
	for i := range delta {
		d := &delta[i]
		if d.NewPath == e.Path || d.OldPath == e.Path {
			match = d
			break
		}
	}
	if match == nil {
		return e.Line, false
	}
	if match.IsDeleted || match.NewPath == "/dev/null" || match.IsNew {
		return 0, true
	}
	if e.Line <= 0 {
		return e.Line, true
	}
	return shiftLine(e.Line, diff.ParseHunks(match.Diff))
}

func shiftLine(line int, hunks []diff.Hunk) (int, bool) {
	shift := 0
	for _, h := range hunks {
		if h.OldCount == 0 {
			if line <= h.OldStart {
				return line + shift, false
			}
			shift += h.NewCount
			continue
		}
		if line < h.OldStart {
			return line + shift, false
		}
		if line >= h.OldStart+h.OldCount {
			shift += h.NewCount - h.OldCount
			continue
		}
		oldN, newN := h.OldStart, h.NewStart
		for _, l := range h.Lines {
			switch l.Type {
			case diff.HunkDeleted:
				if oldN == line {
					return 0, true
				}
				oldN++
			case diff.HunkContext:
				if oldN == line {
					return newN, false
				}
				oldN++
				newN++
			case diff.HunkAdded:
				newN++
			}
		}
		return line + shift, false
	}
	return line + shift, false
}
