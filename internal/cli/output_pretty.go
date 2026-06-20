package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// renderReviewTable prints findings as a per-finding table plus severity counts,
// returning the first write error so the caller can report a broken stdout pipe.
func renderReviewTable(w io.Writer, out ReviewOutcome) error {
	ew := &errWriter{w: w}
	if len(out.Findings) == 0 {
		ew.printf("No findings.\n")
		return ew.err
	}
	for _, f := range out.Findings {
		loc := fmt.Sprintf("%s:%d", f.File, f.Line)
		if f.EndLine > f.Line {
			loc = fmt.Sprintf("%s:%d-%d", f.File, f.Line, f.EndLine)
		}
		ew.printf("%-6s %-12s %s\n", strings.ToUpper(f.Severity), f.Category, loc)
		if r := strings.TrimSpace(f.Rationale); r != "" {
			ew.printf("    %s\n", firstLine(r))
		}
	}
	ew.printf("\n")
	for _, line := range severityCounts(out.Findings) {
		ew.printf("%s\n", line)
	}
	return ew.err
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

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
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
