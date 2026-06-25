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
	if strings.Contains(out, "Omitted inline findings") {
		t.Fatalf("no omitted findings must not emit the overflow details block:\n%s", out)
	}
}

func TestRenderSummaryOverflowOmitsLinkWithoutBase(t *testing.T) {
	out := RenderSummaryWithOverflow(&PRInfo{HeadSHA: "h"}, nil, nil, 1,
		[]engine.Finding{{File: "a.go", Line: 4, Severity: "low", Rationale: "x"}}, nil)
	if strings.Contains(out, "/blob/") {
		t.Fatalf("no HTMLBase must fall back to a plain code-span location (no blob link):\n%s", out)
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
	if !strings.Contains(out, "| +2/-0 | - |") {
		t.Fatalf("want a hyphen for a file with no findings:\n%s", out)
	}
	if !strings.Contains(out, "https://github.com/o/r/blob/abc123/pkg/a.go") {
		t.Fatalf("want a blob permalink for the file cell:\n%s", out)
	}
}

func TestRenderSummaryFullReviewInternals(t *testing.T) {
	info, diffs, findings := presentationFixture()
	out := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{Diffs: diffs})
	// Metadata now lives in a collapsed Review internals details as bullets.
	for _, want := range []string{
		"<summary>Agent handoff & review internals</summary>",
		"**Files** `2`",
		"**Churn** `+12 / −4`",
		"effort-S-brightgreen",
		"context-full-brightgreen",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("want %q in the Review internals block:\n%s", want, out)
		}
	}
	// The old visible meta line must be gone.
	if strings.Contains(out, "**2 files** · ") {
		t.Fatalf("the old visible meta line must be gone:\n%s", out)
	}
}

func TestRenderSummaryHeaderCountsHighFirst(t *testing.T) {
	info := &PRInfo{HeadSHA: "abc123"}
	findings := []engine.Finding{
		{Severity: "low"}, {Severity: "high"}, {Severity: "high"}, {Severity: "medium"},
	}
	out := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{})
	// The H2 header is clean; severity count badges + total ride the compact Result line.
	if !strings.Contains(out, "## Code Review Summary\n") {
		t.Fatalf("want a clean H2 header:\n%s", out)
	}
	if strings.Contains(out, "## Code Review Summary · ") {
		t.Fatalf("severity must NOT be on the H2 header:\n%s", out)
	}
	want := "**Result:** " + shieldsCount("P1", 2, "orange") + " " + shieldsCount("P2", 1, "yellow") + " " + shieldsCount("P3", 1, "blue") + " · 4 findings"
	if !strings.Contains(out, want) {
		t.Fatalf("want count badges (high-first) + total in the quote:\n%s", out)
	}
	if strings.Contains(out, "- high: 2") {
		t.Fatalf("the old per-severity list must be gone:\n%s", out)
	}
}

func TestRenderSummaryHeaderNoFindings(t *testing.T) {
	out := RenderSummaryFull(&PRInfo{HeadSHA: "h"}, nil, nil, 0, nil, nil, SummaryOptions{})
	if !strings.Contains(out, "## Code Review Summary\n") {
		t.Fatalf("want a clean H2 header:\n%s", out)
	}
	if !strings.Contains(out, "No_findings-brightgreen") {
		t.Fatalf("zero findings must render the no-findings marker in the Result line:\n%s", out)
	}
	if strings.Contains(out, "(0 finding") {
		t.Fatalf("no-findings must not append a count:\n%s", out)
	}
}

func TestRenderSummaryFooterReviewedCommit(t *testing.T) {
	out := RenderSummaryFull(&PRInfo{HeadSHA: "deadbeef"}, nil, nil, 0, nil, nil, SummaryOptions{})
	if !strings.Contains(out, "<sub>Reviewed commit `deadbee` · Posted by [miu-cr](https://github.com/vanducng/miu-cr)</sub>") {
		t.Fatalf("want the per-SHA reviewed-commit attribution footer with the miu-cr repo link:\n%s", out)
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

// indexAfter returns the index of needle at/after from, or -1 (with from=len so a
// later lookup also fails). Used to assert strict top-to-bottom render ordering.
func mustOrder(t *testing.T, body string, seq []string) {
	t.Helper()
	from := 0
	for _, s := range seq {
		i := strings.Index(body[from:], s)
		if i < 0 {
			t.Fatalf("missing %q (or out of order) at/after offset %d:\n%s", s, from, body)
		}
		from += i + len(s)
	}
}

func TestRenderSummaryFullOrder(t *testing.T) {
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "deadbeef", ReviewCount: 2}
	findings := []engine.Finding{{Severity: "high", Rationale: "x"}}
	out := RenderSummaryFull(info, findings, map[string]any{"truncation_level": "full"}, 0, nil, nil, SummaryOptions{
		Walkthrough: "this is the walkthrough lead prose",
	})
	mustOrder(t, out, []string{
		ReviewMarker,
		"<!-- miu-cr-runs:",
		"## Code Review Summary",
		"**Result:**", // result line lead
		"this is the walkthrough lead prose",
		"<summary>Agent handoff & review internals</summary>",
		"<sub>Reviewed commit",
	})
}

func TestRenderSummaryReviewCountIdentity(t *testing.T) {
	// Zero ReviewCount: old identity line gone, no "Reviews (" prefix, no "Last reviewed commit:",
	// footer omits the Review-attempts clause, token seeded to 1.
	zero := RenderSummaryFull(&PRInfo{HeadSHA: "abc"}, nil, nil, 0, nil, nil, SummaryOptions{})
	if strings.Contains(zero, "Reviews (") {
		t.Fatalf("summary must not render the old Reviews (N) identity line:\n%s", zero)
	}
	if strings.Contains(zero, "Last reviewed commit:") {
		t.Fatalf("old top identity line must be gone:\n%s", zero)
	}
	if strings.Contains(zero, "Review attempts:") {
		t.Fatalf("zero ReviewCount must omit the Review-attempts clause:\n%s", zero)
	}
	if !strings.Contains(zero, runsCountToken(1)) {
		t.Fatalf("zero ReviewCount must seed the runs token to 1:\n%s", zero)
	}

	// ReviewCount=3: count relocated to the footer as "Review attempts: 3", token carries 3.
	three := RenderSummaryFull(&PRInfo{HeadSHA: "abc", ReviewCount: 3}, nil, nil, 0, nil, nil, SummaryOptions{})
	if !strings.Contains(three, "Review attempts: 3") {
		t.Fatalf("ReviewCount=3 must render the relocated footer count:\n%s", three)
	}
	if !strings.Contains(three, runsCountToken(3)) {
		t.Fatalf("ReviewCount=3 must carry runs token 3:\n%s", three)
	}
}

func TestRenderSummaryInternalsOmitsChurnWithoutDiffs(t *testing.T) {
	out := RenderSummaryFull(&PRInfo{HeadSHA: "abc"}, nil, map[string]any{"files_reviewed": float64(4)}, 0, nil, nil, SummaryOptions{})
	if !strings.Contains(out, "**Files** `4`") {
		t.Fatalf("no-diffs internals must keep the files-reviewed fallback:\n%s", out)
	}
	if !strings.Contains(out, "context-full") {
		t.Fatalf("no-diffs internals must keep Context:\n%s", out)
	}
	if strings.Contains(out, "**Churn**") || strings.Contains(out, "**Effort**") {
		t.Fatalf("no-diffs internals must omit Churn/Effort bullets:\n%s", out)
	}
}

func TestRenderSummaryNoEmDash(t *testing.T) {
	info := &PRInfo{HeadSHA: "deadbeef", ReviewCount: 5, HTMLBase: "https://github.com/o/r", Number: 1}
	_, diffs, findings := presentationFixture()
	out := RenderSummaryFull(info, findings, map[string]any{"truncation_level": "hunks"}, 0, nil, nil, SummaryOptions{
		Diffs:            diffs,
		Walkthrough:      "lead prose",
		Confidence:       4,
		ConfidenceReason: "looks fine",
	})
	if strings.Contains(out, " — ") {
		t.Fatalf("summary body must not contain an em dash:\n%s", out)
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
	if !strings.Contains(out, "**Hand off to an agent**") {
		t.Fatalf("want an agent-handoff block:\n%s", out)
	}
	if !strings.Contains(out, "Run locally: `miucr review --pr https://github.com/o/r/pull/7`") {
		t.Fatalf("want a copy-paste local run pointer:\n%s", out)
	}
	if !strings.Contains(out, "review_run") {
		t.Fatalf("want an MCP pointer:\n%s", out)
	}
	// No secret leakage: a token-shaped string must never appear.
	if strings.Contains(out, "ghp_") || strings.Contains(out, "sk-") {
		t.Fatalf("handoff must not carry secrets:\n%s", out)
	}
}

func TestRenderSummaryHandoffNeverShowsReviewID(t *testing.T) {
	info, diffs, _ := presentationFixture()
	// review_id only resolves on the machine/store that ran the review, so it must
	// NOT appear in the posted summary (the handoff still renders, gated on the URL).
	out := RenderSummaryFull(info, nil, nil, 0, nil, nil, SummaryOptions{Diffs: diffs, ReviewID: "rev_abc"})
	if strings.Contains(out, "review_id") || strings.Contains(out, "rev_abc") {
		t.Fatalf("review_id must never appear in the posted summary:\n%s", out)
	}
	if !strings.Contains(out, "**Hand off to an agent**") {
		t.Fatalf("handoff block must still render:\n%s", out)
	}
}

func TestRenderSummaryFullEmptyFindingsStillClean(t *testing.T) {
	info, diffs, _ := presentationFixture()
	out := RenderSummaryFull(info, nil, nil, 0, nil, nil, SummaryOptions{Diffs: diffs, ReviewID: "rev_x"})
	if !strings.Contains(out, "No_findings-brightgreen") {
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
	// A file without a digest renders a hyphen in the Overview cell.
	if !strings.Contains(out, "| +2/-0 | - | - |") {
		t.Fatalf("want a hyphen for a file with no summary:\n%s", out)
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
	// Confidence line removed; model confidence + reason must NOT render.
	out := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{Diffs: diffs, Confidence: 4, ConfidenceReason: "localized changes, well tested"})
	if strings.Contains(out, "Confidence:") {
		t.Fatalf("summary must no longer render a Confidence line:\n%s", out)
	}
	// derived fallback (Confidence 0): no findings -> no Confidence line either.
	clean := RenderSummaryFull(info, nil, nil, 0, nil, nil, SummaryOptions{Diffs: diffs})
	if strings.Contains(clean, "Confidence:") {
		t.Fatalf("summary must no longer render a Confidence line:\n%s", clean)
	}
}

func TestRenderChangesTableSortsByImportance(t *testing.T) {
	info := &PRInfo{HeadSHA: "h"}
	diffs := []diff.Diff{
		{NewPath: "big.go", Insertions: 200, Deletions: 0}, // big churn, no findings
		{NewPath: "buggy.go", Insertions: 1, Deletions: 0}, // tiny churn, has a finding
	}
	findings := []engine.Finding{{File: "buggy.go", Line: 1, Severity: "high", Rationale: "x"}}
	out := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{Diffs: diffs})
	bi, gi := strings.Index(out, "buggy.go"), strings.Index(out, "big.go")
	if bi < 0 || gi < 0 || bi > gi {
		t.Fatalf("a file with a finding must sort before a bigger no-finding file:\n%s", out)
	}
}
