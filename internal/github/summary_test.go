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
	if !strings.Contains(out, "<summary>Changed files (2)</summary>") {
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

func TestRenderSummaryFullEffortBadge(t *testing.T) {
	info, diffs, findings := presentationFixture()
	out := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{Diffs: diffs})
	if !strings.Contains(out, "Review effort: **S** · 2 file(s) · +12/-4 · max severity high") {
		t.Fatalf("want a deterministic effort badge:\n%s", out)
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

func TestMaxSeverityNoneWhenEmpty(t *testing.T) {
	if got := maxSeverity(nil); got != "none" {
		t.Fatalf("no findings must yield max severity none, got %q", got)
	}
	if got := maxSeverity([]engine.Finding{{Severity: "medium"}, {Severity: "critical"}}); got != "critical" {
		t.Fatalf("want the highest severity, got %q", got)
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
	if !strings.Contains(out, "No findings.") {
		t.Fatalf("empty-findings review must still render a clean summary:\n%s", out)
	}
	if !strings.Contains(out, "Changed files (2)") || !strings.Contains(out, "Hand off to an agent") {
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
	if strings.Contains(full, "Changed files") || strings.Contains(full, "Review effort") || strings.Contains(full, "Hand off") {
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
	if !strings.Contains(out, "## Walkthrough") {
		t.Fatalf("want a leading walkthrough section:\n%s", out)
	}
	if !strings.Contains(out, "This PR refactors the parser and adds a cache.") {
		t.Fatalf("want the walkthrough text:\n%s", out)
	}
	// Walkthrough leads the body, before the changes table.
	if strings.Index(out, "## Walkthrough") > strings.Index(out, "Changed files") {
		t.Fatalf("walkthrough must lead the changes table:\n%s", out)
	}
}

func TestRenderSummaryFullWalkthroughOmittedWhenEmpty(t *testing.T) {
	info, diffs, findings := presentationFixture()
	withWT := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{Diffs: diffs, ReviewID: "rev_x"})
	if strings.Contains(withWT, "## Walkthrough") {
		t.Fatalf("empty walkthrough must omit the section:\n%s", withWT)
	}
	// Whitespace-only walkthrough also collapses to empty (no section).
	if strings.Contains(RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{Diffs: diffs, ReviewID: "rev_x", Walkthrough: "   \n  "}), "## Walkthrough") {
		t.Fatalf("whitespace-only walkthrough must omit the section")
	}
}

func TestRenderSummaryFullPerFileDigest(t *testing.T) {
	info, diffs, findings := presentationFixture()
	summaries := map[string]string{"pkg/a.go": "adds a leak guard"}
	out := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{Diffs: diffs, FileSummaries: summaries})
	if !strings.Contains(out, "| File | Δ | Findings | Summary |") {
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
	if strings.Contains(out, "Summary |") {
		t.Fatalf("no digests must keep the 3-column table:\n%s", out)
	}
	if !strings.Contains(out, "| File | Δ | Findings |") {
		t.Fatalf("want the legacy 3-column header:\n%s", out)
	}
	// A summary map with only an entry for a non-changed file adds no column.
	out2 := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{Diffs: diffs, FileSummaries: map[string]string{"other.go": "x"}})
	if strings.Contains(out2, "Summary |") {
		t.Fatalf("a digest only for an unchanged file must add no column:\n%s", out2)
	}
}

func TestRenderSummaryFullEscapesUntrustedText(t *testing.T) {
	info, diffs, findings := presentationFixture()
	breakout := "# Injected Heading </details>**bad**|col`x`[y](z)<script>"
	out := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{
		Diffs: diffs, ReviewID: "rev_x", Walkthrough: breakout, FileSummaries: map[string]string{"pkg/a.go": breakout}})
	if strings.Contains(out, "<script>") {
		t.Fatalf("untrusted text must be HTML-escaped, found raw <script>:\n%s", out)
	}
	if strings.Contains(out, "</details>**bad**") {
		t.Fatalf("untrusted text must not break out of the block:\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatalf("want HTML-escaped angle brackets:\n%s", out)
	}
	// A leading '#' must not inject a Markdown heading at the start of a line.
	if strings.Contains(out, "\n# Injected Heading") {
		t.Fatalf("untrusted text must not inject a heading:\n%s", out)
	}
	if !strings.Contains(out, "\\# Injected Heading") {
		t.Fatalf("want the leading '#' backslash-escaped:\n%s", out)
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
