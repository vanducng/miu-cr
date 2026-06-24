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
func renderPresentation(b *strings.Builder, info *PRInfo, findings []engine.Finding, diffs []diff.Diff, reviewID string, fileSummaries map[string]string) {
	renderEffortBadge(b, diffs, findings)
	renderChangesTable(b, info, diffs, findings, fileSummaries)
	renderHandoff(b, info, reviewID)
}

// mermaidKeywords are the diagram-type keywords a GitHub-rendered ```mermaid
// block may legitimately start with. A model diagram that doesn't begin with one
// degrades to a plain note instead of a broken fenced block.
var mermaidKeywords = []string{"flowchart", "graph", "sequenceDiagram", "classDiagram", "stateDiagram", "erDiagram", "gitGraph", "mindmap", "journey", "gantt"}

// renderDiagram writes the opt-in mermaid change diagram as a fenced ```mermaid
// block GitHub renders, but ONLY when the model text starts with a known mermaid
// keyword (a degrade-safe sanity check); otherwise — and on empty — it renders a
// plain note, never a broken block. The block content is emitted verbatim (mermaid
// is not markdown; mdInline would corrupt the diagram), so the keyword gate is the
// guard: a non-diagram payload can never reach the fenced block.
func renderDiagram(b *strings.Builder, diagram string) {
	d := strings.TrimSpace(diagram)
	if d == "" {
		return
	}
	if !startsWithMermaidKeyword(d) || strings.Contains(d, "```") {
		b.WriteString("> Diagram omitted: the model did not return a valid mermaid diagram.\n\n")
		return
	}
	b.WriteString("```mermaid\n")
	b.WriteString(d)
	b.WriteString("\n```\n\n")
}

// startsWithMermaidKeyword reports whether the first non-blank line of d begins
// with a recognized mermaid diagram-type keyword.
func startsWithMermaidKeyword(d string) bool {
	first := d
	if i := strings.IndexByte(d, '\n'); i >= 0 {
		first = d[:i]
	}
	first = strings.TrimSpace(first)
	for _, kw := range mermaidKeywords {
		if strings.HasPrefix(first, kw) {
			return true
		}
	}
	return false
}

// renderWalkthrough writes a leading "## Walkthrough" section from the same
// review pass's PR-level summary. Empty walkthrough omits the section entirely
// (byte-for-byte back-compatible with the prior layout). The text is untrusted
// model output, escaped via mdInline so it can't inject markup or break out.
func renderWalkthrough(b *strings.Builder, walkthrough string) {
	w := mdInline(walkthrough)
	if w == "" {
		return
	}
	b.WriteString("## Walkthrough\n\n")
	b.WriteString(w)
	b.WriteString("\n\n")
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
// findings-by-severity · one-line digest) from the changed-file set + findings
// grouped by file + the same review pass's per-file summaries. Rows are capped;
// the file path + digest are escaped via mdInline (table-cell safe). The Summary
// column is dropped when no file has a digest, keeping the prior 3-column layout.
func renderChangesTable(b *strings.Builder, info *PRInfo, diffs []diff.Diff, findings []engine.Finding, fileSummaries map[string]string) {
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

	overflow := 0
	if len(rows) > maxChangesRows {
		overflow = len(rows) - maxChangesRows
		rows = rows[:maxChangesRows]
	}

	withSummary := false
	for _, r := range rows {
		if mdInline(fileSummaries[r.path]) != "" {
			withSummary = true
			break
		}
	}

	fmt.Fprintf(b, "\n<details>\n<summary>Changed files (%d)</summary>\n\n", len(rows)+overflow)
	if withSummary {
		b.WriteString("| File | Δ | Findings | Summary |\n| --- | --- | --- | --- |\n")
	} else {
		b.WriteString("| File | Δ | Findings |\n| --- | --- | --- |\n")
	}
	for _, r := range rows {
		file := mdInline(r.path)
		if url := blobURL(info, r.path, 0, 0); url != "" {
			file = "[" + file + "](<" + url + ">)"
		}
		if withSummary {
			fmt.Fprintf(b, "| %s | +%d/-%d | %s | %s |\n", file, r.adds, r.dels, findingCounts(byFile[r.path]), summaryCell(fileSummaries[r.path]))
		} else {
			fmt.Fprintf(b, "| %s | +%d/-%d | %s |\n", file, r.adds, r.dels, findingCounts(byFile[r.path]))
		}
	}
	if overflow > 0 {
		if withSummary {
			fmt.Fprintf(b, "| _… %d more file(s)_ | | | |\n", overflow)
		} else {
			fmt.Fprintf(b, "| _… %d more file(s)_ | | |\n", overflow)
		}
	}
	b.WriteString("\n</details>\n")
}

// summaryCell renders a file's one-line digest for the changes table, escaped
// via mdInline (untrusted model text); empty renders an em-dash.
func summaryCell(s string) string {
	if v := mdInline(s); v != "" {
		return v
	}
	return "—"
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
