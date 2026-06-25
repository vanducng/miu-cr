package github

import (
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine"
)

func TestPriorityBadgeMapping(t *testing.T) {
	cases := []struct{ sev, want string }{
		{"critical", "🔴 **P0**"},
		{"high", "🟠 **P1**"},
		{"medium", "🟡 **P2**"},
		{"low", "🔵 **P3**"},
		{"info", "⚪ **P4**"},
		{"", "⚪ **P4**"},
		{"bogus", "⚪ **P4**"},
		{"HIGH", "🟠 **P1**"},
		{"  high  ", "🟠 **P1**"},
	}
	for _, c := range cases {
		if got := priorityBadge(c.sev); got != c.want {
			t.Errorf("priorityBadge(%q) = %q, want %q", c.sev, got, c.want)
		}
	}
}

func TestSeverityEmojiMapping(t *testing.T) {
	cases := []struct{ sev, want string }{
		{"critical", "🔴"},
		{"high", "🟠"},
		{"medium", "🟡"},
		{"low", "🔵"},
		{"info", "⚪"},
		{"", "⚪"},
		{"bogus", "⚪"},
	}
	for _, c := range cases {
		if got := severityEmoji(c.sev); got != c.want {
			t.Errorf("severityEmoji(%q) = %q, want %q", c.sev, got, c.want)
		}
	}
}

// severityCounts emits emoji chips critical/high-first; unknown folds into ⚪.
func TestSeverityCounts(t *testing.T) {
	if got := severityCounts(nil); got != "" {
		t.Errorf("no findings → empty chip line, got %q", got)
	}
	findings := []engine.Finding{
		{Severity: "medium"}, {Severity: "medium"},
		{Severity: "low"},
		{Severity: "high"},
		{Severity: "bogus"}, // folds into info ⚪
	}
	got := severityCounts(findings)
	want := "🟠 1 · 🟡 2 · 🔵 1 · ⚪ 1"
	if got != want {
		t.Errorf("severityCounts = %q, want %q", got, want)
	}
}

// commentBody leads with the emoji+P-level badge (not the severity word) and
// keeps the category; untrusted category/rationale stay escaped.
func TestCommentBodyLeadsWithBadge(t *testing.T) {
	f := engine.Finding{Severity: "high", Category: "concurrency", Rationale: "racy map write"}
	body, _ := commentBody(nil, f, "", PostReviewOptions{}, false)
	if !strings.HasPrefix(body, "🟠 **P1** · concurrency") {
		t.Errorf("body must lead with the badge + category:\n%s", body)
	}
	if strings.Contains(body, "**HIGH**") {
		t.Errorf("body must drop the severity word in favor of the badge:\n%s", body)
	}
}

func TestCommentBodyBadgeNoCategory(t *testing.T) {
	f := engine.Finding{Severity: "low", Rationale: "x"}
	body, _ := commentBody(nil, f, "", PostReviewOptions{}, false)
	if !strings.HasPrefix(body, "🔵 **P3**\n\n") {
		t.Errorf("no-category body must lead with the bare badge:\n%s", body)
	}
}

// Unknown severity → ⚪ P4 badge (never a blank/NOTE lead).
func TestCommentBodyUnknownSeverityBadge(t *testing.T) {
	f := engine.Finding{Severity: "", Category: "bug", Rationale: "x"}
	body, _ := commentBody(nil, f, "", PostReviewOptions{}, false)
	if !strings.HasPrefix(body, "⚪ **P4** · bug") {
		t.Errorf("unknown severity must fall back to ⚪ P4:\n%s", body)
	}
}

// The badge is display-only: severity stays the gate value (engine unchanged).
func TestSeverityStaysGateValue(t *testing.T) {
	f := engine.Finding{Severity: "high"}
	if f.Severity != "high" {
		t.Fatal("rendering must not mutate the finding severity")
	}
	commentBody(nil, f, "", PostReviewOptions{}, false)
	if f.Severity != "high" {
		t.Fatalf("commentBody mutated severity to %q", f.Severity)
	}
}
