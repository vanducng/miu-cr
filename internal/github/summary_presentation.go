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

// renderPresentation appends the per-file changes table to the summary body. It is
// derived from local data (diff stats + findings), so it costs zero model calls and
// degrades cleanly (nil diffs skip the table). The agent handoff moved into the
// combined renderHandoffAndInternals block.
func renderPresentation(b *strings.Builder, info *PRInfo, findings []engine.Finding, diffs []diff.Diff, fileSummaries map[string]string) {
	renderChangesTable(b, info, diffs, findings, fileSummaries)
}

// mermaidKeywords are the diagram-type keywords a GitHub-rendered ```mermaid
// block may legitimately start with. A model diagram that doesn't begin with one
// degrades to a plain note instead of a broken fenced block.
var mermaidKeywords = []string{"flowchart", "graph", "sequencediagram", "classdiagram", "statediagram", "erdiagram", "gitgraph", "mindmap", "journey", "gantt", "pie"}

// renderDiagram writes the opt-in mermaid change diagram as a fenced ```mermaid
// block GitHub renders, but ONLY when the model text starts with a known mermaid
// keyword (a degrade-safe sanity check); otherwise (and on empty) it renders a
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

// startsWithMermaidKeyword reports whether the first line of d begins with a
// recognized mermaid diagram-type keyword. The comparison is case-insensitive
// (e.g. "Flowchart TD" and "graph LR" both parse), so the
// first line is lowercased before the prefix check (mermaidKeywords are stored
// lowercase to match).
func startsWithMermaidKeyword(d string) bool {
	first := d
	if i := strings.IndexByte(d, '\n'); i >= 0 {
		first = d[:i]
	}
	first = strings.ToLower(strings.TrimSpace(first))
	for _, kw := range mermaidKeywords {
		if strings.HasPrefix(first, kw) {
			return true
		}
	}
	return false
}

// renderWalkthrough writes the review pass's PR-level summary under a bold
// "**What changed:**" lead-in (not an H3, to keep the comment compact). Rendered via
// mdProse so the model's bullet newlines survive (mdInline would collapse them) while
// HTML/fence breakout vectors stay neutralized. Empty (after trim) omits it.
// maxWalkthroughBullets caps the "What changed" summary so it stays a quick,
// skimmable lead-in above the tracking tables, never a wall of detail.
const maxWalkthroughBullets = 5

func renderWalkthrough(b *strings.Builder, walkthrough string) {
	walkthrough = capBullets(strings.TrimSpace(walkthrough), maxWalkthroughBullets)
	if walkthrough == "" {
		return
	}
	b.WriteString("**What changed:**\n")
	b.WriteString(mdWalkthrough(walkthrough))
	b.WriteString("\n\n")
}

// capBullets keeps at most n bullet lines (lines whose trimmed form starts with
// "-") plus any preceding non-bullet prose, trimming an over-long model
// walkthrough to a concise lead-in. It STOPS at the (n+1)th bullet and drops it
// along with everything after — including that bullet's indented continuation
// lines — so no orphaned fragment of a dropped bullet survives.
func capBullets(s string, n int) string {
	if s == "" {
		return ""
	}
	var out []string
	bullets := 0
	for _, ln := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "-") {
			bullets++
			if bullets > n {
				break
			}
		}
		out = append(out, ln)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// effortSize buckets a PR into S/M/L/XL from file count + total churn: pure
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
		findings   int
	}
	rows := make([]row, 0, len(diffs))
	for i := range diffs {
		p := diffs[i].NewPath
		if p == "" || p == "/dev/null" {
			continue
		}
		fc := 0
		for _, n := range byFile[p] {
			fc += n
		}
		rows = append(rows, row{path: p, adds: diffs[i].Insertions, dels: diffs[i].Deletions, findings: fc})
	}
	if len(rows) == 0 {
		return
	}

	// Most important first: files with findings, then biggest churn, then path. The
	// row cap then keeps the most important rows, not arbitrary diff order.
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].findings != rows[j].findings {
			return rows[i].findings > rows[j].findings
		}
		di, dj := rows[i].adds+rows[i].dels, rows[j].adds+rows[j].dels
		if di != dj {
			return di > dj
		}
		return rows[i].path < rows[j].path
	})

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

	fmt.Fprintf(b, "\n<details>\n<summary>Important Files Changed (%d)</summary>\n\n", len(rows)+overflow)
	if withSummary {
		b.WriteString("| File | Δ | Findings | Overview |\n| --- | --- | --- | --- |\n")
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
		pipes := " | |"
		if withSummary {
			pipes += " |"
		}
		fmt.Fprintf(b, "| _… %d more file(s)_%s |\n", overflow, pipes)
	}
	b.WriteString("\n</details>\n")
}

// summaryCell renders a file's one-line digest for the changes table, escaped
// via mdInline (untrusted model text); empty renders a hyphen.
func summaryCell(s string) string {
	if v := mdInline(s); v != "" {
		return v
	}
	return "-"
}

// findingCounts renders a file's finding histogram high→low (e.g. "2 high, 1
// low") or "-" when the file has none.
func findingCounts(counts map[string]int) string {
	if len(counts) == 0 {
		return "-"
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
		return "-"
	}
	return strings.Join(parts, ", ")
}

// prURL builds the PR's HTML URL from the base repo URL + number; empty when the
// base URL is unknown.
func prURL(info *PRInfo) string {
	if info == nil || info.HTMLBase == "" || info.Number == 0 {
		return ""
	}
	return fmt.Sprintf("%s/pull/%d", strings.TrimRight(info.HTMLBase, "/"), info.Number)
}
