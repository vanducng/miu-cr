package engine

import (
	"fmt"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
)

// validGates is the closed set accepted by --gate / the gate input; anything
// else is rejected so a typo can never silently disable gating.
var validGates = map[string]bool{
	"none": true, "info": true, "low": true,
	"medium": true, "high": true, "critical": true,
}

// ValidGate reports whether gate is in the closed gate set.
func ValidGate(gate string) bool { return validGates[gate] }

var validPromptFormats = map[string]bool{"legacy": true, "xml": true}

// ValidPromptFormat reports whether f is a known prompt format.
func ValidPromptFormat(f string) bool { return validPromptFormats[f] }

// ValidateInvocation rejects an invalid gate and any mode combination that is
// not exactly one of staged / from+to / commit. It is the single contract both
// external boundaries (CLI flags, MCP review_run) enforce, so a host can never
// reach the pipeline with an ambiguous mode or an out-of-set gate. Returned
// errors are clierr.CLIError with stable codes (Exit 2).
func ValidateInvocation(staged bool, from, to, commit, gate string) error {
	if !validGates[gate] {
		return &clierr.CLIError{
			Code:    "review.bad_gate",
			Message: fmt.Sprintf("invalid gate %q: want one of none|info|low|medium|high|critical", gate),
			Hint:    "use --gate none|info|low|medium|high|critical",
			Exit:    2,
		}
	}
	hasRange := from != "" || to != ""
	if (from == "") != (to == "") {
		return &clierr.CLIError{Code: "review.bad_flags", Message: "from and to must be used together", Exit: 2}
	}
	modes := 0
	if staged {
		modes++
	}
	if hasRange {
		modes++
	}
	if commit != "" {
		modes++
	}
	if modes == 0 {
		return &clierr.CLIError{Code: "review.no_mode", Message: "select exactly one mode: staged, from/to, or commit", Exit: 2}
	}
	if modes > 1 {
		return &clierr.CLIError{Code: "review.bad_flags", Message: "modes are mutually exclusive: use only one of staged, from/to, commit", Exit: 2}
	}
	return nil
}
