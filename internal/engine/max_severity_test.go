package engine

import "testing"

// Regression: stats.max_severity must equal the highest-ranked finding's
// severity whenever findings exist, and "none" for the empty set, the bug was
// it coming back empty even with findings present.
func TestMaxSeverityHighestWins(t *testing.T) {
	cases := []struct {
		name     string
		findings []Finding
		want     string
	}{
		{"empty", nil, "none"},
		{"single high", []Finding{{Severity: "high"}}, "high"},
		{
			"mixed picks critical",
			[]Finding{{Severity: "low"}, {Severity: "critical"}, {Severity: "medium"}},
			"critical",
		},
		{"info floor", []Finding{{Severity: "info"}}, "info"},
		{"unrecognized only", []Finding{{Severity: "spicy"}}, "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := maxSeverity(tc.findings); got != tc.want {
				t.Fatalf("maxSeverity(%v) = %q, want %q", tc.findings, got, tc.want)
			}
		})
	}
}

// And the rank used for max_severity agrees with the gate's ranking, so the
// reported max and the gate decision never disagree.
func TestMaxSeverityAgreesWithGateRank(t *testing.T) {
	findings := []Finding{{Severity: "medium"}, {Severity: "high"}}
	if maxSeverity(findings) != "high" {
		t.Fatalf("max_severity should be high, got %q", maxSeverity(findings))
	}
	if !GateFailed(findings, "high") {
		t.Fatal("high finding must trip the high gate")
	}
	if GateFailed(findings, "critical") {
		t.Fatal("high finding must not trip the critical gate")
	}
}
