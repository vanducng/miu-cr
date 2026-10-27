package github

import "github.com/vanducng/miu-cr/internal/engine/diff"

// AnchorShift is where a prior anchor sits after the follow-up commits.
type AnchorShift struct {
	Path string
	Line int
}

// ShiftedLines is the new path and line of a prior anchor so the next review's old side still matches it.
func ShiftedLines(prior []LedgerEntry, delta []diff.Diff) map[string]AnchorShift {
	if delta == nil {
		return nil
	}
	out := map[string]AnchorShift{}
	for _, e := range prior {
		switch e.Status {
		case statusOpen, statusReopened, statusDeferred:
		default:
			continue
		}
		if e.FP == "" {
			continue
		}
		path, line, removed := remapAnchor(e, delta)
		if removed || line <= 0 || (line == e.Line && path == e.Path) {
			continue
		}
		out[e.FP] = AnchorShift{Path: path, Line: line}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func applyShiftedLines(entries []LedgerEntry, shifted map[string]AnchorShift) []LedgerEntry {
	if len(shifted) == 0 {
		return entries
	}
	out := append([]LedgerEntry(nil), entries...)
	for i := range out {
		s, ok := shifted[out[i].FP]
		if !ok {
			continue
		}
		switch out[i].Status {
		case statusOpen, statusReopened, statusDeferred:
		default:
			continue
		}
		if s.Line > 0 {
			out[i].Line = s.Line
		}
		if s.Path != "" && s.Path != "/dev/null" {
			out[i].Path = s.Path
		}
	}
	return out
}

func remapAnchor(e LedgerEntry, delta []diff.Diff) (path string, line int, removed bool) {
	var match *diff.Diff
	for i := range delta {
		d := &delta[i]
		if d.NewPath == e.Path || d.OldPath == e.Path {
			match = d
			break
		}
	}
	if match == nil {
		return e.Path, e.Line, false
	}
	if match.IsDeleted || match.NewPath == "/dev/null" || match.IsNew {
		return "", 0, true
	}
	if e.Line <= 0 {
		return e.Path, e.Line, true
	}
	line, removed = shiftLine(e.Line, diff.ParseHunks(match.Diff))
	path = match.NewPath
	if path == "" || path == "/dev/null" {
		path = e.Path
	}
	return path, line, removed
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
