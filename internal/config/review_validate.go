package config

import (
	"fmt"
	"time"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
)

// gateValidator/filterModeValidator/minSeverityValidator are injected by the cli
// layer (which owns the engine/github enums) so config stays a leaf and the enum
// source of truth is not duplicated. Nil validators (e.g. a bare config test that
// never wires them) accept any value; the cli always wires them before use.
var (
	gateValidator        func(string) bool
	filterModeValidator  func(string) bool
	minSeverityValidator func(string) bool
)

// SetReviewValidators wires the enum predicates used by ValidateReview. Called
// once from the cli package init so config can validate [review] values without
// importing engine/github.
func SetReviewValidators(gate, filterMode, minSeverity func(string) bool) {
	gateValidator, filterModeValidator, minSeverityValidator = gate, filterMode, minSeverity
}

// ValidateReview rejects an out-of-set [review] enum or an unparsable timeout,
// returning a typed config.invalid CLIError (Exit 2) — the right namespace for a
// config-sourced value (not flags.invalid_*). An empty field is the documented
// "no default" and passes. Only validates fields a config can set.
func ValidateReview(r Review) error {
	if r.Gate != "" && gateValidator != nil && !gateValidator(r.Gate) {
		return invalidReview("gate", r.Gate, "none|info|low|medium|high|critical")
	}
	if r.FilterMode != "" && filterModeValidator != nil && !filterModeValidator(r.FilterMode) {
		return invalidReview("filter_mode", r.FilterMode, "added|diff_context|file|nofilter")
	}
	if r.MinSeverity != "" && minSeverityValidator != nil && !minSeverityValidator(r.MinSeverity) {
		return invalidReview("min_severity", r.MinSeverity, "none|info|low|medium|high|critical")
	}
	if r.Timeout != "" {
		if _, err := time.ParseDuration(r.Timeout); err != nil {
			return invalidReview("timeout", r.Timeout, "a Go duration like 300s or 5m")
		}
	}
	return nil
}

func invalidReview(field, value, want string) error {
	return &clierr.CLIError{
		Code:    "config.invalid",
		Message: fmt.Sprintf("[review].%s %q is invalid: want %s", field, RedactString(value), want),
		Hint:    "fix [review]." + field + " in " + FilePathOrEmpty(),
		Exit:    2,
	}
}
