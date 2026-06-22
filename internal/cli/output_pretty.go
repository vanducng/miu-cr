package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// ANSI codes; emitted only when the writer is a terminal (isTerminal, from
// progress.go — same stdlib check, no go-isatty dependency). Piped/CI stays plain.
const (
	ansiReset = "\033[0m"
	ansiBold  = "\033[1m"
	ansiDim   = "\033[2m"
	ansiRed   = "\033[31m"
	ansiYel   = "\033[33m"
	ansiBlue  = "\033[34m"
	ansiCyan  = "\033[36m"
)

// renderReviewTable prints findings as a local, editor-jumpable reporter: per
// finding a `file:line` (or file:start-end range), a severity glyph + severity
// (category), the rationale, a quoted-code excerpt, and a suggested-patch preview.
// Color + glyphs are emitted only when w is a terminal; otherwise plain ASCII.
func renderReviewTable(w io.Writer, out ReviewOutcome) error {
	ew := &errWriter{w: w}
	color := isTerminal(w)
	if len(out.Findings) == 0 {
		ew.printf("%s\n", paint(color, ansiDim, "No findings."))
		return ew.err
	}
	for i, f := range out.Findings {
		if i > 0 {
			ew.printf("\n")
		}
		renderFinding(ew, color, f)
	}
	ew.printf("\n")
	for _, line := range severityCounts(out.Findings) {
		ew.printf("%s\n", line)
	}
	return ew.err
}

func renderFinding(ew *errWriter, color bool, f ReviewFinding) {
	loc := fmt.Sprintf("%s:%d", f.File, f.Line)
	if f.EndLine > f.Line {
		loc = fmt.Sprintf("%s:%d-%d", f.File, f.Line, f.EndLine)
	}
	sev := strings.ToUpper(strings.TrimSpace(f.Severity))
	if sev == "" {
		sev = "NOTE"
	}
	head := fmt.Sprintf("%s %s", severityGlyph(color, f.Severity), sev)
	if cat := strings.TrimSpace(f.Category); cat != "" {
		head += " (" + cat + ")"
	}
	ew.printf("%s  %s\n", paint(color, severityColor(f.Severity)+ansiBold, head), paint(color, ansiCyan, loc))

	if r := strings.TrimSpace(f.Rationale); r != "" {
		for _, ln := range strings.Split(r, "\n") {
			ew.printf("    %s\n", ln)
		}
	}
	if code := strings.TrimSpace(f.QuotedCode); code != "" {
		ew.printf("  %s\n", paint(color, ansiDim, "code:"))
		printIndentedBlock(ew, color, code)
	}
	if patch := strings.TrimSpace(f.SuggestedPatch); patch != "" {
		ew.printf("  %s\n", paint(color, ansiDim, "suggested patch:"))
		printIndentedBlock(ew, color, patch)
	}
}

// printIndentedBlock writes a multi-line excerpt indented under a finding, dimmed
// on a terminal. Capped at 8 lines so a large patch can't flood the terminal.
func printIndentedBlock(ew *errWriter, color bool, block string) {
	lines := strings.Split(block, "\n")
	const max = 8
	truncated := false
	if len(lines) > max {
		lines = lines[:max]
		truncated = true
	}
	bar, ellipsis := "| ", "| ..."
	if color {
		bar, ellipsis = "│ ", "│ …"
	}
	for _, ln := range lines {
		ew.printf("    %s\n", paint(color, ansiDim, bar+ln))
	}
	if truncated {
		ew.printf("    %s\n", paint(color, ansiDim, ellipsis))
	}
}

func paint(color bool, code, s string) string {
	if !color {
		return s
	}
	return code + s + ansiReset
}

// severityGlyph returns a severity marker: Unicode glyphs on a terminal, plain
// ASCII when not (piped/CI/log stays UTF-8-safe, matching the doc-comment contract).
func severityGlyph(color bool, sev string) string {
	if !color {
		switch strings.ToLower(strings.TrimSpace(sev)) {
		case "critical", "high":
			return "x"
		case "medium":
			return "!"
		case "low":
			return "*"
		default:
			return "-"
		}
	}
	switch strings.ToLower(strings.TrimSpace(sev)) {
	case "critical", "high":
		return "✖"
	case "medium":
		return "▲"
	case "low":
		return "●"
	default:
		return "·"
	}
}

func severityColor(sev string) string {
	switch strings.ToLower(strings.TrimSpace(sev)) {
	case "critical", "high":
		return ansiRed
	case "medium":
		return ansiYel
	case "low":
		return ansiBlue
	default:
		return ansiDim
	}
}

// errWriter latches the first write error so the table loop need not check each call.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) printf(format string, args ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintf(e.w, format, args...)
}

func severityCounts(findings []ReviewFinding) []string {
	counts := map[string]int{}
	for _, f := range findings {
		counts[strings.ToLower(f.Severity)]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys)+1)
	lines = append(lines, fmt.Sprintf("%d finding(s):", len(findings)))
	for _, k := range keys {
		lines = append(lines, fmt.Sprintf("  %-8s %d", k, counts[k]))
	}
	return lines
}
