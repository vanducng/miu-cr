package context

import (
	"fmt"
	"strings"

	"github.com/vanducng/miu-cr/internal/engine/diff"
)

// AssembleOptions controls context assembly.
type AssembleOptions struct {
	TokenBudget  int  // approximate token budget (len(text)/4); <=0 disables budgeting
	ExpandWindow int  // context lines added above/below each hunk in the new-content window; <=0 disables expansion
	UseXML       bool // emit XML-tagged file sections instead of markdown === File: === delimiters
}

// Truncation levels, ordered from richest to leanest. Recorded in Stats so
// dogfooding sees truncation rather than a silent miss.
const (
	LevelFull          = "full"           // diff hunks + expansion windows
	LevelHunksOnly     = "hunks_only"     // diff hunks, no expansion windows
	LevelFilenamesOnly = "filenames_only" // file list only
)

// AssembledContext is the deterministic review context plus assembly stats.
type AssembledContext struct {
	Text  string         `json:"text"`
	Stats map[string]any `json:"stats"`
}

// AssembleContext builds the exact text the agent will see from the reviewable
// diffs: per-file diff hunks plus line-numbered new-content windows. It is
// deterministic for a fixed diff set. When the result exceeds TokenBudget it
// degrades through the truncation ladder (drop expansion windows, then
// hunks-only, then filenames-only) and records the applied level in Stats.
func AssembleContext(diffs []diff.Diff, opts AssembleOptions) AssembledContext {
	renderFn := render
	renderFilesFn := renderFilenames
	if opts.UseXML {
		renderFn = renderXML
		renderFilesFn = renderFilenamesXML
	}
	full := renderFn(diffs, opts.ExpandWindow, true)
	stats := map[string]any{
		"files":            len(diffs),
		"truncation_level": LevelFull,
		"est_tokens":       estTokens(full),
		"token_budget":     opts.TokenBudget,
	}
	if !overBudget(full, opts.TokenBudget) {
		return AssembledContext{Text: full, Stats: stats}
	}

	hunks := renderFn(diffs, 0, false)
	if !overBudget(hunks, opts.TokenBudget) {
		stats["truncation_level"] = LevelHunksOnly
		stats["est_tokens"] = estTokens(hunks)
		return AssembledContext{Text: hunks, Stats: stats}
	}

	names := renderFilesFn(diffs)
	stats["truncation_level"] = LevelFilenamesOnly
	stats["est_tokens"] = estTokens(names)
	return AssembledContext{Text: names, Stats: stats}
}

func estTokens(s string) int { return len(s) / 4 }

func overBudget(s string, budget int) bool {
	return budget > 0 && estTokens(s) > budget
}

// render emits per-file sections. When withWindows is true and expand>=0 a
// line-numbered new-content window around the changed lines is appended.
func render(diffs []diff.Diff, expand int, withWindows bool) string {
	var sb strings.Builder
	for _, d := range diffs {
		sb.WriteString(fmt.Sprintf("=== File: %s ===\n", d.NewPath))
		sb.WriteString("--- Diff ---\n")
		sb.WriteString(strings.TrimRight(d.Diff, "\n"))
		sb.WriteString("\n")
		if withWindows && d.NewFileContent != "" {
			win := newContentWindow(d, expand)
			if win != "" {
				sb.WriteString("--- New content ---\n")
				sb.WriteString(win)
			}
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func renderFilenames(diffs []diff.Diff) string {
	var sb strings.Builder
	sb.WriteString("=== Files changed ===\n")
	for _, d := range diffs {
		sb.WriteString(d.NewPath)
		sb.WriteString("\n")
	}
	return sb.String()
}

// xmlEscBody escapes text for XML element body content.
func xmlEscBody(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// xmlEscAttr escapes text for an XML attribute value (additionally escapes ").
func xmlEscAttr(s string) string {
	s = xmlEscBody(s)
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}

// renderXML emits per-file sections using XML-tagged structure instead of the
// markdown === File: === delimiters; file paths and content bodies are escaped.
func renderXML(diffs []diff.Diff, expand int, withWindows bool) string {
	var sb strings.Builder
	for _, d := range diffs {
		sb.WriteString(fmt.Sprintf("<file path=%q>\n", xmlEscAttr(d.NewPath)))
		sb.WriteString("<diff>")
		sb.WriteString(xmlEscBody(strings.TrimRight(d.Diff, "\n")))
		sb.WriteString("</diff>\n")
		if withWindows && d.NewFileContent != "" {
			win := newContentWindow(d, expand)
			if win != "" {
				sb.WriteString("<new_content>")
				sb.WriteString(xmlEscBody(win))
				sb.WriteString("</new_content>\n")
			}
		}
		sb.WriteString("</file>\n\n")
	}
	return sb.String()
}

// renderFilenamesXML emits the filenames-only fallback in XML.
func renderFilenamesXML(diffs []diff.Diff) string {
	var sb strings.Builder
	sb.WriteString("<files_changed>\n")
	for _, d := range diffs {
		sb.WriteString(fmt.Sprintf("<file path=%q/>\n", xmlEscAttr(d.NewPath)))
	}
	sb.WriteString("</files_changed>\n")
	return sb.String()
}

// newContentWindow emits the line-numbered new-file lines covering the union of
// changed-line ranges (from the hunks) expanded by `expand` on each side.
func newContentWindow(d diff.Diff, expand int) string {
	if expand < 0 {
		expand = 0
	}
	lines := strings.Split(d.NewFileContent, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return ""
	}

	keep := make([]bool, len(lines)+1) // 1-based
	for _, h := range diff.ParseHunks(d.Diff) {
		ln := h.NewStart
		for _, hl := range h.Lines {
			switch hl.Type {
			case diff.HunkDeleted:
				// no new-side line consumed
			case diff.HunkAdded, diff.HunkContext:
				lo := ln - expand
				if lo < 1 {
					lo = 1
				}
				hi := ln + expand
				if hi > len(lines) {
					hi = len(lines)
				}
				for i := lo; i <= hi; i++ {
					keep[i] = true
				}
				ln++
			}
		}
	}

	var sb strings.Builder
	prev := 0
	for i := 1; i <= len(lines); i++ {
		if !keep[i] {
			continue
		}
		if prev != 0 && i != prev+1 {
			sb.WriteString("...\n")
		}
		sb.WriteString(fmt.Sprintf("%d|%s\n", i, lines[i-1]))
		prev = i
	}
	return sb.String()
}
