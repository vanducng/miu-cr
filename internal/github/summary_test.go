package github

import (
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine"
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
