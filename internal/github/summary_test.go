package github

import (
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/diff"
)

func TestBlobURL(t *testing.T) {
	info := &PRInfo{HTMLBase: "https://github.com/o/r", HeadSHA: "abc123"}
	tests := []struct {
		name          string
		path          string
		line, endLine int
		want          string
	}{
		{"single line", "pkg/a.go", 12, 0, "https://github.com/o/r/blob/abc123/pkg/a.go#L12"},
		{"range", "pkg/a.go", 12, 15, "https://github.com/o/r/blob/abc123/pkg/a.go#L12-L15"},
		{"endline equal to line is single", "pkg/a.go", 12, 12, "https://github.com/o/r/blob/abc123/pkg/a.go#L12"},
		{"no line", "pkg/a.go", 0, 0, "https://github.com/o/r/blob/abc123/pkg/a.go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := blobURL(info, tt.path, tt.line, tt.endLine); got != tt.want {
				t.Fatalf("blobURL = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBlobURLEmptyWhenBaseMissing(t *testing.T) {
	if got := blobURL(&PRInfo{HeadSHA: "abc"}, "a.go", 1, 0); got != "" {
		t.Fatalf("missing HTMLBase must yield empty URL, got %q", got)
	}
	if got := blobURL(&PRInfo{HTMLBase: "https://github.com/o/r"}, "a.go", 1, 0); got != "" {
		t.Fatalf("missing HeadSHA must yield empty URL, got %q", got)
	}
}

func TestRenderSummaryOverflowListsOmittedWithPermalinks(t *testing.T) {
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "abc123", HTMLBase: "https://github.com/o/r"}
	omitted := []engine.Finding{
		{File: "pkg/a.go", Line: 12, EndLine: 15, Severity: "high", Category: "bug", Title: "Resource leak", Rationale: "leak\nhere"},
		{File: "pkg/b.go", Line: 3, Severity: "medium", Category: "style", Rationale: "rename"},
	}
	out := RenderSummaryWithOverflow(info, nil, nil, 2, omitted, nil)
	if !strings.Contains(out, "<details>") || !strings.Contains(out, "Omitted inline findings (2)") {
		t.Fatalf("want a <details> overflow block:\n%s", out)
	}
	if !strings.Contains(out, "https://github.com/o/r/blob/abc123/pkg/a.go#L12-L15") {
		t.Fatalf("want a range permalink for the first omitted finding:\n%s", out)
	}
	if !strings.Contains(out, "https://github.com/o/r/blob/abc123/pkg/b.go#L3") {
		t.Fatalf("want a single-line permalink for the second omitted finding:\n%s", out)
	}
	if !strings.Contains(out, "**HIGH** (bug)") || !strings.Contains(out, "leak here") {
		t.Fatalf("want severity/category + one-line rationale:\n%s", out)
	}
	if !strings.Contains(out, "**Resource leak**") {
		t.Fatalf("want the finding title in the overflow entry:\n%s", out)
	}
}

func TestRenderSummaryNoOverflowWhenNoneOmitted(t *testing.T) {
	out := RenderSummary(&PRInfo{HeadSHA: "h"}, nil, nil, 0)
	if strings.Contains(out, "<details>") {
		t.Fatalf("no omitted findings must not emit a details block:\n%s", out)
	}
}

func TestRenderSummaryOverflowOmitsLinkWithoutBase(t *testing.T) {
	out := RenderSummaryWithOverflow(&PRInfo{HeadSHA: "h"}, nil, nil, 1,
		[]engine.Finding{{File: "a.go", Line: 4, Severity: "low", Rationale: "x"}}, nil)
	if strings.Contains(out, "](http") {
		t.Fatalf("no HTMLBase must fall back to a plain code-span location:\n%s", out)
	}
	if !strings.Contains(out, "`a.go:4`") {
		t.Fatalf("want a plain code-span file:line:\n%s", out)
	}
}

func presentationFixture() (*PRInfo, []diff.Diff, []engine.Finding) {
	info := &PRInfo{Owner: "o", Repo: "r", Number: 7, HeadSHA: "abc123", HTMLBase: "https://github.com/o/r"}
	diffs := []diff.Diff{
		{NewPath: "pkg/a.go", Insertions: 10, Deletions: 4},
		{NewPath: "pkg/b.go", Insertions: 2, Deletions: 0},
		{NewPath: "/dev/null"}, // deleted file: must not count as a changed file
	}
	findings := []engine.Finding{
		{File: "pkg/a.go", Line: 12, Severity: "high", Category: "bug", Rationale: "leak"},
		{File: "pkg/a.go", Line: 20, Severity: "low", Category: "style", Rationale: "rename"},
	}
	return info, diffs, findings
}

func TestRenderSummaryFullChangesTable(t *testing.T) {
	info, diffs, findings := presentationFixture()
	out := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{Diffs: diffs})
	if !strings.Contains(out, "<summary>Important Files Changed (2)</summary>") {
		t.Fatalf("want a changes table for 2 changed files (deleted excluded):\n%s", out)
	}
	if !strings.Contains(out, "| +10/-4 | 1 high, 1 low |") {
		t.Fatalf("want per-file +/- + severity-ordered finding counts for a.go:\n%s", out)
	}
	if !strings.Contains(out, "| +2/-0 | — |") {
		t.Fatalf("want an em-dash for a file with no findings:\n%s", out)
	}
	if !strings.Contains(out, "https://github.com/o/r/blob/abc123/pkg/a.go") {
		t.Fatalf("want a blob permalink for the file cell:\n%s", out)
	}
}

func TestRenderSummaryFullMetaQuote(t *testing.T) {
	info, diffs, findings := presentationFixture()
	out := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{Diffs: diffs})
	if !strings.Contains(out, "> 2 files · +12/−4 · effort S · context full") {
		t.Fatalf("want a compact metadata quote line:\n%s", out)
	}
}

func TestRenderSummaryHeaderCountsHighFirst(t *testing.T) {
	info := &PRInfo{HeadSHA: "abc123"}
	findings := []engine.Finding{
		{Severity: "low"}, {Severity: "high"}, {Severity: "high"}, {Severity: "medium"},
	}
	out := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{})
	// High-first chips, then the finding count.
	if !strings.Contains(out, "## Code Review · 🟠 2 · 🟡 1 · 🔵 1  (4 findings)") {
		t.Fatalf("want emoji-severity counts (high-first) + finding count:\n%s", out)
	}
	// The severity histogram list is gone from the body.
	if strings.Contains(out, "- high: 2") {
		t.Fatalf("the old per-severity list must be gone:\n%s", out)
	}
}

func TestRenderSummaryHeaderNoFindings(t *testing.T) {
	out := RenderSummaryFull(&PRInfo{HeadSHA: "h"}, nil, nil, 0, nil, nil, SummaryOptions{})
	if !strings.Contains(out, "## Code Review · ✅ no findings") {
		t.Fatalf("zero findings must render the no-findings header:\n%s", out)
	}
	if strings.Contains(out, "(0 finding") {
		t.Fatalf("no-findings header must not append a count:\n%s", out)
	}
}

func TestRenderSummaryFooterReviewedCommit(t *testing.T) {
	out := RenderSummaryFull(&PRInfo{HeadSHA: "deadbeef"}, nil, nil, 0, nil, nil, SummaryOptions{})
	if !strings.Contains(out, "<sub>Reviewed commit `deadbeef` · Posted by miu-cr</sub>") {
		t.Fatalf("want the per-commit attribution footer:\n%s", out)
	}
	// The old upsert footer line must be gone.
	if strings.Contains(out, "Re-runs edit this summary") {
		t.Fatalf("the old upsert footer must be gone:\n%s", out)
	}
	// Head/files/context no longer render as a bulleted list.
	if strings.Contains(out, "- Head: `") || strings.Contains(out, "- Files reviewed:") {
		t.Fatalf("metadata must move to the quote/footer, not a bullet list:\n%s", out)
	}
}

func TestEffortSizeBuckets(t *testing.T) {
	tests := []struct {
		files int
		churn int64
		want  string
	}{
		{1, 10, "S"},
		{2, 50, "S"},
		{2, 51, "M"},
		{10, 400, "M"},
		{11, 100, "L"},
		{30, 1500, "L"},
		{31, 100, "XL"},
		{5, 5000, "XL"},
	}
	for _, tt := range tests {
		if got := effortSize(tt.files, tt.churn); got != tt.want {
			t.Errorf("effortSize(%d, %d) = %q, want %q", tt.files, tt.churn, got, tt.want)
		}
	}
}

func TestRenderSummaryFullHandoff(t *testing.T) {
	info, diffs, _ := presentationFixture()
	out := RenderSummaryFull(info, nil, nil, 0, nil, nil, SummaryOptions{Diffs: diffs, ReviewID: "rev_abc"})
	if !strings.Contains(out, "<summary>Hand off to an agent</summary>") {
		t.Fatalf("want an agent-handoff block:\n%s", out)
	}
	if !strings.Contains(out, "review_id: `rev_abc`") {
		t.Fatalf("want the review_id in the handoff block:\n%s", out)
	}
	if !strings.Contains(out, "miucr review --pr https://github.com/o/r/pull/7 -o json") {
		t.Fatalf("want a copy-paste re-run pointer:\n%s", out)
	}
	if !strings.Contains(out, "review_run") {
		t.Fatalf("want an MCP pointer:\n%s", out)
	}
	// No secret leakage: a token-shaped string must never appear.
	if strings.Contains(out, "ghp_") || strings.Contains(out, "sk-") {
		t.Fatalf("handoff must not carry secrets:\n%s", out)
	}
}

func TestRenderSummaryFullHandoffSkippedWithoutReviewID(t *testing.T) {
	info, diffs, _ := presentationFixture()
	out := RenderSummaryFull(info, nil, nil, 0, nil, nil, SummaryOptions{Diffs: diffs})
	if strings.Contains(out, "Hand off to an agent") {
		t.Fatalf("empty review_id must skip the handoff block:\n%s", out)
	}
}

func TestRenderSummaryFullEmptyFindingsStillClean(t *testing.T) {
	info, diffs, _ := presentationFixture()
	out := RenderSummaryFull(info, nil, nil, 0, nil, nil, SummaryOptions{Diffs: diffs, ReviewID: "rev_x"})
	if !strings.Contains(out, "## Code Review · ✅ no findings") {
		t.Fatalf("empty-findings review must still render a clean summary:\n%s", out)
	}
	if !strings.Contains(out, "Important Files Changed (2)") || !strings.Contains(out, "Hand off to an agent") {
		t.Fatalf("blocks must still render with zero findings:\n%s", out)
	}
}

func TestRenderSummaryFullDegradesWithoutDiffs(t *testing.T) {
	// nil diffs + empty reviewID = the legacy RenderSummaryWithOverflow body,
	// byte-for-byte, so the comment shape stays back-compatible.
	info := &PRInfo{HeadSHA: "h"}
	full := RenderSummaryFull(info, nil, nil, 0, nil, nil, SummaryOptions{})
	legacy := RenderSummaryWithOverflow(info, nil, nil, 0, nil, nil)
	if full != legacy {
		t.Fatalf("nil diffs/empty id must equal the legacy body:\n--full--\n%s\n--legacy--\n%s", full, legacy)
	}
	if strings.Contains(full, "Important Files Changed") || strings.Contains(full, "Review effort") || strings.Contains(full, "Hand off") {
		t.Fatalf("no diffs/id must emit none of the new blocks:\n%s", full)
	}
}

func TestRenderChangesTableCapsRows(t *testing.T) {
	info := &PRInfo{HeadSHA: "h"}
	diffs := make([]diff.Diff, maxChangesRows+5)
	for i := range diffs {
		diffs[i] = diff.Diff{NewPath: "f", Insertions: 1}
	}
	out := RenderSummaryFull(info, nil, nil, 0, nil, nil, SummaryOptions{Diffs: diffs})
	if !strings.Contains(out, "5 more file(s)") {
		t.Fatalf("want an overflow note for capped rows:\n%s", out)
	}
}

func TestRenderSummaryFullWalkthrough(t *testing.T) {
	info, diffs, findings := presentationFixture()
	out := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{
		Diffs: diffs, ReviewID: "rev_x", Walkthrough: "This PR refactors the parser and adds a cache."})
	if !strings.Contains(out, "This PR refactors the parser and adds a cache.") {
		t.Fatalf("want the walkthrough text (lead prose):\n%s", out)
	}
	if strings.Contains(out, "### Walkthrough") {
		t.Fatalf("walkthrough must be lead prose, NOT a Walkthrough heading:\n%s", out)
	}
	// Walkthrough leads the body, before the changes table.
	if strings.Index(out, "This PR refactors the parser") > strings.Index(out, "Important Files Changed") {
		t.Fatalf("walkthrough must lead the changes table:\n%s", out)
	}
}

func TestRenderSummaryFullWalkthroughBullets(t *testing.T) {
	info, diffs, findings := presentationFixture()
	out := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{
		Diffs: diffs, Walkthrough: "- adds a cache\n- guards a nil deref\n- renames the parser"})
	if !strings.Contains(out, "- adds a cache\n- guards a nil deref\n- renames the parser") {
		t.Fatalf("want the bullet walkthrough (no heading) with newlines preserved:\n%s", out)
	}
	if strings.Contains(out, "### Walkthrough") {
		t.Fatalf("bullets must render without a Walkthrough heading:\n%s", out)
	}
}

func TestRenderSummaryFullWalkthroughOmittedWhenEmpty(t *testing.T) {
	info, diffs, findings := presentationFixture()
	const wt = "DISTINCTIVE-WALKTHROUGH-LINE"
	withWT := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{Diffs: diffs, ReviewID: "rev_x", Walkthrough: wt})
	if !strings.Contains(withWT, wt) {
		t.Fatalf("a present walkthrough must render:\n%s", withWT)
	}
	empty := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{Diffs: diffs, ReviewID: "rev_x"})
	ws := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{Diffs: diffs, ReviewID: "rev_x", Walkthrough: "   \n  "})
	if strings.Contains(empty, wt) || empty != ws {
		t.Fatalf("empty/whitespace walkthrough must render nothing, identically:\nempty=%s\nws=%s", empty, ws)
	}
}

func TestRenderSummaryFullPerFileDigest(t *testing.T) {
	info, diffs, findings := presentationFixture()
	summaries := map[string]string{"pkg/a.go": "adds a leak guard"}
	out := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{Diffs: diffs, FileSummaries: summaries})
	if !strings.Contains(out, "| File | Δ | Findings | Overview |") {
		t.Fatalf("want a Summary column header when any file has a digest:\n%s", out)
	}
	if !strings.Contains(out, "adds a leak guard") {
		t.Fatalf("want the per-file digest in the row:\n%s", out)
	}
	// A file without a digest renders an em-dash in the Summary cell.
	if !strings.Contains(out, "| +2/-0 | — | — |") {
		t.Fatalf("want an em-dash digest for a file with no summary:\n%s", out)
	}
}

func TestRenderSummaryFullNoSummaryColumnWhenAbsent(t *testing.T) {
	info, diffs, findings := presentationFixture()
	// No file_summaries → the table keeps the legacy 3-column layout byte-for-byte.
	out := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{Diffs: diffs})
	if strings.Contains(out, "Overview |") {
		t.Fatalf("no digests must keep the 3-column table:\n%s", out)
	}
	if !strings.Contains(out, "| File | Δ | Findings |") {
		t.Fatalf("want the legacy 3-column header:\n%s", out)
	}
	// A summary map with only an entry for a non-changed file adds no column.
	out2 := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{Diffs: diffs, FileSummaries: map[string]string{"other.go": "x"}})
	if strings.Contains(out2, "Overview |") {
		t.Fatalf("a digest only for an unchanged file must add no column:\n%s", out2)
	}
}

func TestRenderSummaryFullEscapesUntrustedWalkthrough(t *testing.T) {
	info, diffs, findings := presentationFixture()
	// Bullets with HTML/comment + fence breakout vectors; newlines must survive.
	walkthrough := "- adds a guard\n- closes </details><script>alert(1)</script>\n- ```fence``` should not open a block"
	out := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{
		Diffs: diffs, ReviewID: "rev_x", Walkthrough: walkthrough})
	if strings.Contains(out, "<script>") || strings.Contains(out, "</script>") {
		t.Fatalf("walkthrough HTML breakout must be neutralized:\n%s", out)
	}
	if !strings.Contains(out, "&lt;/details&gt;&lt;script&gt;") {
		t.Fatalf("want the walkthrough's </details><script> HTML-escaped:\n%s", out)
	}
	// The triple-backtick fence must be neutralized (mdProse escapes backticks)
	// so it can't open a code block that swallows the rest of the body.
	if strings.Contains(out, "```fence```") {
		t.Fatalf("walkthrough fence must be neutralized:\n%s", out)
	}
	// Bullet newlines survive (mdProse preserves them; mdInline would collapse).
	if !strings.Contains(out, "- adds a guard\n") {
		t.Fatalf("walkthrough bullet newlines must survive:\n%s", out)
	}
}

func TestRenderSummaryFullEscapesUntrustedTableCell(t *testing.T) {
	info, diffs, findings := presentationFixture()
	breakout := "# Injected Heading </details>**bad**|col`x`[y](z)<script>"
	out := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{
		Diffs: diffs, ReviewID: "rev_x", FileSummaries: map[string]string{"pkg/a.go": breakout}})
	if strings.Contains(out, "<script>") {
		t.Fatalf("table cell must be HTML-escaped, found raw <script>:\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatalf("want HTML-escaped angle brackets in the cell:\n%s", out)
	}
	if !strings.Contains(out, "\\# Injected Heading") {
		t.Fatalf("want the table cell '#' backslash-escaped (mdInline):\n%s", out)
	}
}

func TestRenderSummaryFullDegradesByteForByteWithoutNewFields(t *testing.T) {
	info, diffs, findings := presentationFixture()
	withFields := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{Diffs: diffs, ReviewID: "rev_x"})
	legacy := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{Diffs: diffs, ReviewID: "rev_x", FileSummaries: map[string]string{}})
	if withFields != legacy {
		t.Fatalf("empty walkthrough + empty summaries must render byte-for-byte:\n--a--\n%s\n--b--\n%s", withFields, legacy)
	}
}

func TestRenderSummaryFullDiagramRendersFencedBlock(t *testing.T) {
	info, diffs, findings := presentationFixture()
	out := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{Diffs: diffs, ReviewID: "rev_x", Diagram: "flowchart TD\n  A-->B"})
	if !strings.Contains(out, "```mermaid\nflowchart TD\n  A-->B\n```") {
		t.Fatalf("want a fenced mermaid block:\n%s", out)
	}
}

func TestRenderSummaryFullDiagramNonMermaidPlainNote(t *testing.T) {
	info, diffs, findings := presentationFixture()
	out := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{Diffs: diffs, ReviewID: "rev_x", Diagram: "not a real diagram at all"})
	if strings.Contains(out, "```mermaid") {
		t.Fatalf("a non-mermaid diagram must NOT render a fenced block:\n%s", out)
	}
	if !strings.Contains(out, "Diagram omitted") {
		t.Fatalf("a non-mermaid diagram must degrade to a plain note:\n%s", out)
	}
}

func TestRenderSummaryFullDiagramEmptyOmitsSection(t *testing.T) {
	info, diffs, findings := presentationFixture()
	out := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{Diffs: diffs, ReviewID: "rev_x", Diagram: "   \n  "})
	if strings.Contains(out, "```mermaid") || strings.Contains(out, "Diagram omitted") {
		t.Fatalf("an empty diagram must render nothing:\n%s", out)
	}
}

func TestRenderSummaryFullDiagramFenceInjectionFallsBack(t *testing.T) {
	info, diffs, findings := presentationFixture()
	// A mermaid-looking payload that smuggles a closing fence must not emit a block.
	out := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{Diffs: diffs, ReviewID: "rev_x", Diagram: "flowchart TD\n```\n## injected"})
	if strings.Contains(out, "## injected") && !strings.Contains(out, "Diagram omitted") {
		t.Fatalf("a fence-injecting diagram must degrade, not break the comment:\n%s", out)
	}
	if strings.Contains(out, "```mermaid") {
		t.Fatalf("a fence-injecting diagram must not open a mermaid block:\n%s", out)
	}
}

func TestMdProseEscapesBreakoutKeepsFormatting(t *testing.T) {
	out := mdProse("see </details> and <!-- x --> and ```fence``` but keep [link](u) and a < b")
	if strings.Contains(out, "</details>") || strings.Contains(out, "<!--") {
		t.Fatalf("HTML/comment breakout not escaped: %q", out)
	}
	if strings.Contains(out, "```") {
		t.Fatalf("triple-backtick fence not neutralized (would swallow the suggestion block): %q", out)
	}
	if !strings.Contains(out, "[link](u)") { // brackets/links stay readable
		t.Fatalf("intentional Markdown was over-escaped: %q", out)
	}
}

func TestRenderSummaryConfidence(t *testing.T) {
	info, diffs, findings := presentationFixture() // findings include a high
	// model-emitted confidence wins + the reason renders.
	out := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{Diffs: diffs, Confidence: 4, ConfidenceReason: "localized changes, well tested"})
	if !strings.Contains(out, "**Confidence: 4/5**") || !strings.Contains(out, "localized changes, well tested") {
		t.Fatalf("model confidence + reason must render:\n%s", out)
	}
	// derived fallback (Confidence 0): no findings -> 5/5.
	clean := RenderSummaryFull(info, nil, nil, 0, nil, nil, SummaryOptions{Diffs: diffs})
	if !strings.Contains(clean, "**Confidence: 5/5**") {
		t.Fatalf("no findings must derive 5/5:\n%s", clean)
	}
	// derived fallback with a critical -> below 5.
	crit := []engine.Finding{{Severity: "critical", Category: "security", Rationale: "x"}}
	cout := RenderSummaryFull(info, crit, nil, 0, nil, nil, SummaryOptions{Diffs: diffs})
	if strings.Contains(cout, "Confidence: 5/5") {
		t.Fatalf("a critical finding must lower the derived confidence below 5/5:\n%s", cout)
	}
}
