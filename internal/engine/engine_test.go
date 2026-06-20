package engine

import "testing"

func TestDedupeKeepsDistinctSameLine(t *testing.T) {
	in := []Finding{
		{File: "a.go", Line: 10, Category: "bug", Rationale: "off-by-one"},
		{File: "a.go", Line: 10, Category: "bug", Rationale: "off-by-one"}, // exact dup
		{File: "a.go", Line: 10, Category: "bug", Rationale: "nil deref"},  // distinct prose
	}
	out := dedupe(in)
	if len(out) != 2 {
		t.Fatalf("dedupe: want 2 (collapse exact dup, keep distinct prose), got %d", len(out))
	}
}

func TestGateFailed(t *testing.T) {
	findings := []Finding{{Severity: "medium"}}
	if GateFailed(findings, "high") {
		t.Error("medium must not trip a high gate")
	}
	if !GateFailed(findings, "medium") {
		t.Error("medium must trip a medium gate")
	}
	if GateFailed(findings, "none") {
		t.Error("none gate never fails")
	}
	if !GateFailed([]Finding{{Severity: "critical"}}, "high") {
		t.Error("critical must trip a high gate")
	}
	// An unrecognized gate must fail loudly, not silently disable gating.
	if !GateFailed([]Finding{{Severity: "info"}}, "hgih") {
		t.Error("unknown gate must fail loudly, not pass")
	}
	if !GateFailed(nil, "High") {
		t.Error("wrong-case gate is unknown and must fail loudly")
	}
}
