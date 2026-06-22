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
		{File: "a.go", Line: 10, Severity: "high", Category: "bug", Rationale: "boom\nsecond line", QuotedCode: "x := 1", SuggestedPatch: "x := 2"},
		{File: "b.go", Line: 5, EndLine: 8, Severity: "low", Category: "style"},
		{File: "c.go", Line: 1, Severity: "high", Category: "bug"},
	}}
	var buf bytes.Buffer
	if err := renderReviewTable(&buf, out); err != nil {
		t.Fatalf("renderReviewTable: %v", err)
	}
	s := buf.String()
	// A bytes.Buffer is not a terminal, so output must be plain ASCII: no ANSI, no
	// box-drawing/ellipsis glyphs, no Unicode severity glyphs.
	if strings.Contains(s, "\033[") {
		t.Fatalf("color leaked into non-TTY output: %q", s)
	}
	for _, glyph := range []string{"│", "…", "✖", "▲", "●", "·"} {
		if strings.Contains(s, glyph) {
			t.Fatalf("non-ASCII glyph %q leaked into non-TTY output: %q", glyph, s)
		}
	}
	// The quoted-code block uses an ASCII bar separator off a terminal.
	if !strings.Contains(s, "| x := 1") {
		t.Fatalf("expected ASCII bar separator for code block: %q", s)
	}
	if !strings.Contains(s, "HIGH") || !strings.Contains(s, "a.go:10") {
		t.Fatalf("missing finding row: %q", s)
	}
	if !strings.Contains(s, "b.go:5-8") {
		t.Fatalf("range location not rendered: %q", s)
	}
	// The reporter renders the FULL rationale (not just the first line).
	if !strings.Contains(s, "boom") || !strings.Contains(s, "second line") {
		t.Fatalf("rationale must render in full: %q", s)
	}
	if !strings.Contains(s, "x := 1") {
		t.Fatalf("quoted-code excerpt missing: %q", s)
	}
	if !strings.Contains(s, "suggested patch:") || !strings.Contains(s, "x := 2") {
		t.Fatalf("suggested-patch preview missing: %q", s)
	}
	if !strings.Contains(s, "3 finding(s):") {
		t.Fatalf("missing total: %q", s)
	}
	if !strings.Contains(s, "high") || !strings.Contains(s, "2") || !strings.Contains(s, "low") {
		t.Fatalf("severity counts missing: %q", s)
	}
}

func TestRenderReviewTableTruncationASCII(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 12; i++ {
		sb.WriteString("line\n")
	}
	out := ReviewOutcome{Findings: []ReviewFinding{
		{File: "a.go", Line: 1, Severity: "high", Category: "bug", Rationale: "boom", QuotedCode: sb.String()},
	}}
	var buf bytes.Buffer
	if err := renderReviewTable(&buf, out); err != nil {
		t.Fatalf("renderReviewTable: %v", err)
	}
	s := buf.String()
	// A >8-line block truncates; off a terminal the ellipsis is ASCII "...".
	if strings.Contains(s, "…") || strings.Contains(s, "│") {
		t.Fatalf("non-ASCII glyph leaked into truncated non-TTY output: %q", s)
	}
	if !strings.Contains(s, "| ...") {
		t.Fatalf("expected ASCII ellipsis in truncated block: %q", s)
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

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }
