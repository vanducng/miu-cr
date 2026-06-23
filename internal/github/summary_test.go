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
		{File: "pkg/a.go", Line: 12, EndLine: 15, Severity: "high", Category: "bug", Rationale: "leak\nhere"},
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
	out := RenderSummaryFull(info, findings, nil, 0, nil, nil, diffs, "")
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
	out := RenderSummaryFull(info, findings, nil, 0, nil, nil, diffs, "")
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
	out := RenderSummaryFull(info, nil, nil, 0, nil, nil, diffs, "rev_abc")
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
	out := RenderSummaryFull(info, nil, nil, 0, nil, nil, diffs, "")
	if strings.Contains(out, "Hand off to an agent") {
		t.Fatalf("empty review_id must skip the handoff block:\n%s", out)
	}
}

func TestRenderSummaryFullEmptyFindingsStillClean(t *testing.T) {
	info, diffs, _ := presentationFixture()
	out := RenderSummaryFull(info, nil, nil, 0, nil, nil, diffs, "rev_x")
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
	full := RenderSummaryFull(info, nil, nil, 0, nil, nil, nil, "")
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
	out := RenderSummaryFull(info, nil, nil, 0, nil, nil, diffs, "")
	if !strings.Contains(out, "5 more file(s)") {
		t.Fatalf("want an overflow note for capped rows:\n%s", out)
	}
}
