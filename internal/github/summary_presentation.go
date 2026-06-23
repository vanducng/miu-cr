package github

import (
	"fmt"
	"sort"
	"strings"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/diff"
)

// maxChangesRows caps the per-file changes table so a large PR can't render an
// enormous comment; overflow is noted on a trailing row, mirroring the inline cap.
const maxChangesRows = 60

// renderPresentation appends the LLM-free reviewer-trust blocks — effort badge,
// per-file changes table, agent-handoff — to the summary body. Every block is
// derived from local data (diff stats + findings), so it costs zero model calls.
// All three degrade cleanly: nil diffs skip the table/badge, an empty reviewID
// skips the handoff.
func renderPresentation(b *strings.Builder, info *PRInfo, findings []engine.Finding, diffs []diff.Diff, reviewID string) {
	renderEffortBadge(b, diffs, findings)
	renderChangesTable(b, info, diffs, findings)
	renderHandoff(b, info, reviewID)
}

// renderEffortBadge writes a one-line, deterministic review-effort badge derived
// purely from diff stats + max finding severity (no model call).
func renderEffortBadge(b *strings.Builder, diffs []diff.Diff, findings []engine.Finding) {
	if len(diffs) == 0 {
		return
	}
	var adds, dels int64
	files := 0
	for i := range diffs {
		if p := diffs[i].NewPath; p == "" || p == "/dev/null" {
			continue
		}
		files++
		adds += diffs[i].Insertions
		dels += diffs[i].Deletions
	}
	if files == 0 {
		return
	}
	fmt.Fprintf(b, "- Review effort: **%s** · %d file(s) · +%d/-%d · max severity %s\n",
		effortSize(files, adds+dels), files, adds, dels, maxSeverity(findings))
}

// effortSize buckets a PR into S/M/L/XL from file count + total churn — pure
// arithmetic, stable across runs.
func effortSize(files int, churn int64) string {
	switch {
	case files <= 2 && churn <= 50:
		return "S"
	case files <= 10 && churn <= 400:
		return "M"
	case files <= 30 && churn <= 1500:
		return "L"
	default:
		return "XL"
	}
}

// maxSeverity returns the highest finding severity (high→low) or "none".
func maxSeverity(findings []engine.Finding) string {
	best := len(severityOrder)
	for _, f := range findings {
		if r := severityRank(f.Severity); r < best {
			best = r
		}
	}
	if best == len(severityOrder) {
		return "none"
	}
	return severityOrder[best]
}

// renderChangesTable writes a collapsed per-file table (file · +adds/-dels ·
// findings-by-severity) from the changed-file set + findings already grouped by
// file. Rows are capped; the file path is escaped via mdInline (table-cell safe).
func renderChangesTable(b *strings.Builder, info *PRInfo, diffs []diff.Diff, findings []engine.Finding) {
	if len(diffs) == 0 {
		return
	}
	byFile := map[string]map[string]int{}
	for _, f := range findings {
		sev := strings.ToLower(strings.TrimSpace(f.Severity))
		if sev == "" {
			sev = "info"
		}
		if byFile[f.File] == nil {
			byFile[f.File] = map[string]int{}
		}
		byFile[f.File][sev]++
	}

	type row struct {
		path       string
		adds, dels int64
	}
	rows := make([]row, 0, len(diffs))
	for i := range diffs {
		p := diffs[i].NewPath
		if p == "" || p == "/dev/null" {
			continue
		}
		rows = append(rows, row{path: p, adds: diffs[i].Insertions, dels: diffs[i].Deletions})
	}
	if len(rows) == 0 {
		return
	}

	fmt.Fprintf(b, "\n<details>\n<summary>Changed files (%d)</summary>\n\n", len(rows))
	b.WriteString("| File | Δ | Findings |\n| --- | --- | --- |\n")
	overflow := 0
	if len(rows) > maxChangesRows {
		overflow = len(rows) - maxChangesRows
		rows = rows[:maxChangesRows]
	}
	for _, r := range rows {
		file := mdInline(r.path)
		if url := blobURL(info, r.path, 0, 0); url != "" {
			file = "[" + file + "](<" + url + ">)"
		}
		fmt.Fprintf(b, "| %s | +%d/-%d | %s |\n", file, r.adds, r.dels, findingCounts(byFile[r.path]))
	}
	if overflow > 0 {
		fmt.Fprintf(b, "| _… %d more file(s)_ | | |\n", overflow)
	}
	b.WriteString("\n</details>\n")
}

// findingCounts renders a file's finding histogram high→low (e.g. "2 high, 1
// low") or "—" when the file has none.
func findingCounts(counts map[string]int) string {
	if len(counts) == 0 {
		return "—"
	}
	parts := make([]string, 0, len(counts))
	for _, sev := range severityOrder {
		if n := counts[sev]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, sev))
		}
	}
	extra := make([]string, 0)
	for sev, n := range counts {
		if !known(sev) {
			extra = append(extra, fmt.Sprintf("%d %s", n, sev))
		}
	}
	sort.Strings(extra)
	parts = append(parts, extra...)
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, ", ")
}

// renderHandoff writes a collapsed agent-handoff block: the review_id + a
// copy-paste pointer a human can hand to an AI agent. Text only — no secrets.
func renderHandoff(b *strings.Builder, info *PRInfo, reviewID string) {
	if strings.TrimSpace(reviewID) == "" {
		return
	}
	b.WriteString("\n<details>\n<summary>Hand off to an agent</summary>\n\n")
	// Inside a code span markdown chars are inert; only a backtick can break out.
	fmt.Fprintf(b, "- review_id: `%s`\n", strings.ReplaceAll(reviewID, "`", "'"))
	if url := prURL(info); url != "" {
		fmt.Fprintf(b, "- Re-run as JSON: `miucr review --pr %s -o json`\n", url)
	} else {
		b.WriteString("- Re-run as JSON: `miucr review --pr <pr-url> -o json`\n")
	}
	b.WriteString("- MCP: call `review_run` (or `review_get` with the review_id) from an agent host.\n")
	b.WriteString("\n</details>\n")
}

// prURL builds the PR's HTML URL from the base repo URL + number; empty when the
// base URL is unknown.
func prURL(info *PRInfo) string {
	if info == nil || info.HTMLBase == "" || info.Number == 0 {
		return ""
	}
	return fmt.Sprintf("%s/pull/%d", strings.TrimRight(info.HTMLBase, "/"), info.Number)
}
