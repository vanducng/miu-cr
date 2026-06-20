package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestRenderReviewTableEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := renderReviewTable(&buf, ReviewOutcome{}); err != nil {
		t.Fatalf("renderReviewTable: %v", err)
	}
	if strings.TrimSpace(buf.String()) != "No findings." {
		t.Fatalf("empty render = %q, want 'No findings.'", buf.String())
	}
}

func TestRenderReviewTableMultiSeverity(t *testing.T) {
	out := ReviewOutcome{Findings: []ReviewFinding{
		{File: "a.go", Line: 10, Severity: "high", Category: "bug", Rationale: "boom\nsecond line"},
		{File: "b.go", Line: 5, EndLine: 8, Severity: "low", Category: "style"},
		{File: "c.go", Line: 1, Severity: "high", Category: "bug"},
	}}
	var buf bytes.Buffer
	if err := renderReviewTable(&buf, out); err != nil {
		t.Fatalf("renderReviewTable: %v", err)
	}
	s := buf.String()
	if !strings.Contains(s, "HIGH") || !strings.Contains(s, "a.go:10") {
		t.Fatalf("missing finding row: %q", s)
	}
	if !strings.Contains(s, "b.go:5-8") {
		t.Fatalf("range location not rendered: %q", s)
	}
	if !strings.Contains(s, "boom") || strings.Contains(s, "second line") {
		t.Fatalf("rationale must render only first line: %q", s)
	}
	if !strings.Contains(s, "3 finding(s):") {
		t.Fatalf("missing total: %q", s)
	}
	// severityCounts sorted: high=2, low=1
	if !strings.Contains(s, "high") || !strings.Contains(s, "2") || !strings.Contains(s, "low") {
		t.Fatalf("severity counts missing: %q", s)
	}
}

func TestRenderReviewTableWriteError(t *testing.T) {
	out := ReviewOutcome{Findings: []ReviewFinding{{File: "a.go", Line: 1, Severity: "high", Category: "bug"}}}
	if err := renderReviewTable(failWriter{}, out); err == nil {
		t.Fatal("expected write error to propagate")
	}
}

func TestSeverityCountsOrdering(t *testing.T) {
	got := severityCounts([]ReviewFinding{
		{Severity: "low"}, {Severity: "high"}, {Severity: "high"}, {Severity: "critical"},
	})
	// header + 3 distinct severities, sorted alphabetically.
	if len(got) != 4 {
		t.Fatalf("want 4 lines, got %d: %v", len(got), got)
	}
	if !strings.HasPrefix(got[1], "  critical") {
		t.Fatalf("expected critical first (alpha), got %q", got[1])
	}
}

func TestFirstLine(t *testing.T) {
	if firstLine("a\nb") != "a" {
		t.Fatal("firstLine should cut at newline")
	}
	if firstLine("single") != "single" {
		t.Fatal("firstLine should passthrough no-newline")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }
