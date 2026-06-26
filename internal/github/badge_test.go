package github

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine"
)

func shieldsBadge(p, color string) string {
	return "<sub><sub>![" + p + "](https://img.shields.io/badge/" + p + "-" + color + "?style=flat)</sub></sub>"
}

func shieldsCount(p string, n int, color string) string {
	sev := map[string]string{"P0": "critical", "P1": "high", "P2": "medium", "P3": "low"}[p]
	if sev == "" {
		sev = "info"
	}
	return fmt.Sprintf("<sub><sub>![%s | %s | %d](https://img.shields.io/badge/%s%%20%%7C%%20%s-%d-lightgrey?labelColor=%s&style=flat)</sub></sub>", p, sev, n, p, sev, n, color)
}

func TestPriorityBadgeMapping(t *testing.T) {
	cases := []struct{ sev, want string }{
		{"critical", shieldsBadge("P0", "red")},
		{"high", shieldsBadge("P1", "orange")},
		{"medium", shieldsBadge("P2", "yellow")},
		{"low", shieldsBadge("P3", "blue")},
		{"info", shieldsBadge("P4", "lightgrey")},
		{"", shieldsBadge("P4", "lightgrey")},
		{"bogus", shieldsBadge("P4", "lightgrey")},
		{"HIGH", shieldsBadge("P1", "orange")},
		{"  high  ", shieldsBadge("P1", "orange")},
	}
	for _, c := range cases {
		if got := priorityBadge(c.sev); got != c.want {
			t.Errorf("priorityBadge(%q) = %q, want %q", c.sev, got, c.want)
		}
	}
}

// severityCounts emits shields count badges critical/high-first; unknown folds into P4.
func TestSeverityCounts(t *testing.T) {
	if got := severityCounts(nil); got != "" {
		t.Errorf("no findings → empty chip line, got %q", got)
	}
	findings := []engine.Finding{
		{Severity: "medium"}, {Severity: "medium"},
		{Severity: "low"},
		{Severity: "high"},
		{Severity: "bogus"}, // folds into info P4
	}
	got := severityCounts(findings)
	want := shieldsCount("P1", 1, "orange") + " " + shieldsCount("P2", 2, "yellow") + " " + shieldsCount("P3", 1, "blue") + " " + shieldsCount("P4", 1, "lightgrey")
	if got != want {
		t.Errorf("severityCounts = %q, want %q", got, want)
	}
}

// commentBody leads with the emoji+P-level badge (not the severity word) and
// keeps the category; untrusted category/rationale stay escaped.
func TestCommentBodyLeadsWithBadge(t *testing.T) {
	f := engine.Finding{Severity: "high", Category: "concurrency", Rationale: "racy map write"}
	body, _ := commentBody(nil, f, "", PostReviewOptions{}, false)
	if !strings.HasPrefix(body, shieldsBadge("P1", "orange")+" · concurrency") {
		t.Errorf("body must lead with the shields badge + category:\n%s", body)
	}
	if strings.Contains(body, "**HIGH**") {
		t.Errorf("body must drop the severity word in favor of the badge:\n%s", body)
	}
}

func TestCommentBodyBadgeNoCategory(t *testing.T) {
	f := engine.Finding{Severity: "low", Rationale: "x"}
	body, _ := commentBody(nil, f, "", PostReviewOptions{}, false)
	if !strings.HasPrefix(body, shieldsBadge("P3", "blue")+"\n\n") {
		t.Errorf("no-category body must lead with the bare badge:\n%s", body)
	}
}

// Unknown severity → ⚪ P4 badge (never a blank/NOTE lead).
func TestCommentBodyUnknownSeverityBadge(t *testing.T) {
	f := engine.Finding{Severity: "", Category: "bug", Rationale: "x"}
	body, _ := commentBody(nil, f, "", PostReviewOptions{}, false)
	if !strings.HasPrefix(body, shieldsBadge("P4", "lightgrey")+" · bug") {
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
