package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// renderReviewTable prints findings as a per-finding table plus severity counts.
func renderReviewTable(w io.Writer, out ReviewOutcome) {
	if len(out.Findings) == 0 {
		fmt.Fprintln(w, "No findings.")
		return
	}
	for _, f := range out.Findings {
		loc := fmt.Sprintf("%s:%d", f.File, f.Line)
		if f.EndLine > f.Line {
			loc = fmt.Sprintf("%s:%d-%d", f.File, f.Line, f.EndLine)
		}
		fmt.Fprintf(w, "%-6s %-12s %s\n", strings.ToUpper(f.Severity), f.Category, loc)
		if r := strings.TrimSpace(f.Rationale); r != "" {
			fmt.Fprintf(w, "    %s\n", firstLine(r))
		}
	}
	fmt.Fprintln(w)
	for _, line := range severityCounts(out.Findings) {
		fmt.Fprintln(w, line)
	}
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
