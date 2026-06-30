package cli

import (
	"testing"

	"github.com/vanducng/miu-cr/internal/config"
)

// A per-repo code_summary override must survive mergeHostReview (the chain that
// builds each repo's effective host review) — not just the global default.
func TestMergeHostReviewCarriesCodeSummary(t *testing.T) {
	tr, f := true, false
	base := config.HostReview{CodeSummary: config.CodeSummary{Walkthrough: &tr, FileChangeSummary: &f}}

	// Override turns the file table ON and omits walkthrough (inherits base).
	out := mergeHostReview(base, config.HostReview{CodeSummary: config.CodeSummary{FileChangeSummary: &tr}})
	if !out.CodeSummary.WantFileChangeSummary() {
		t.Fatal("per-repo file_change_summary=true override must win")
	}
	if !out.CodeSummary.WantWalkthrough() {
		t.Fatal("walkthrough must inherit base (true) when the override omits it")
	}

	// An override that omits code_summary entirely inherits the base.
	out2 := mergeHostReview(base, config.HostReview{})
	if out2.CodeSummary.WantFileChangeSummary() {
		t.Fatal("base file_change_summary=false must persist when the override omits it")
	}
}
