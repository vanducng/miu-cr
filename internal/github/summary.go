package github

import (
	"fmt"
	"strings"

	"github.com/vanducng/miu-cr/internal/engine"
)

// severityOrder ranks severities high→low for a stable histogram.
var severityOrder = []string{"critical", "high", "medium", "low", "info"}

// RenderSummary builds the sentinel-headed PR summary body: the hidden
// SummarySentinel must be the first line (UpsertSummaryComment relies on it),
// followed by a severity histogram, truncation level, head SHA, files reviewed,
// and a short footer.
func RenderSummary(info *PRInfo, findings []engine.Finding, stats map[string]any) string {
	var b strings.Builder
	b.WriteString(SummarySentinel)
	b.WriteString("\n## miu-cr review\n\n")

	counts := map[string]int{}
	for _, f := range findings {
		sev := strings.ToLower(strings.TrimSpace(f.Severity))
		if sev == "" {
			sev = "info"
		}
		counts[sev]++
	}

	if len(findings) == 0 {
		b.WriteString("No findings.\n\n")
	} else {
		fmt.Fprintf(&b, "**%d finding(s):**\n\n", len(findings))
		for _, sev := range severityOrder {
			if n := counts[sev]; n > 0 {
				fmt.Fprintf(&b, "- %s: %d\n", sev, n)
			}
		}
		for sev, n := range counts {
			if !known(sev) {
				fmt.Fprintf(&b, "- %s: %d\n", sev, n)
			}
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "- Head: `%s`\n", info.HeadSHA)
	fmt.Fprintf(&b, "- Files reviewed: %s\n", statInt(stats, "files_reviewed"))
	fmt.Fprintf(&b, "- Context: %s\n", truncationLevel(stats))
	if info.IsFork {
		b.WriteString("- Source: fork (comments posted to the base repo)\n")
	}

	b.WriteString("\n<sub>Posted by miu-cr. Re-runs edit this summary and skip already-posted inline comments.</sub>")
	return b.String()
}

func known(sev string) bool {
	for _, s := range severityOrder {
		if s == sev {
			return true
		}
	}
	return false
}

func truncationLevel(stats map[string]any) string {
	if stats == nil {
		return "full"
	}
	if v, ok := stats["truncation_level"]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return "full"
}

func statInt(stats map[string]any, key string) string {
	if stats == nil {
		return "0"
	}
	switch v := stats[key].(type) {
	case float64:
		return fmt.Sprintf("%d", int(v))
	case int:
		return fmt.Sprintf("%d", v)
	case nil:
		return "0"
	default:
		return fmt.Sprintf("%v", v)
	}
}
