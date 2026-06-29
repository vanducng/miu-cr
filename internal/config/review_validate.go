package config

import (
	"fmt"
	"time"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
)

const maxReviewContextHops = 5
const maxReviewToolRetries = 5
const maxReviewToolTurns = 64
const maxProviderRetryRetries = 20

// gateValidator/filterModeValidator/minSeverityValidator are injected by the cli
// layer (which owns the engine/github enums) so config stays a leaf and the enum
// source of truth is not duplicated. Nil validators (e.g. a bare config test that
// never wires them) accept any value; the cli always wires them before use.
var (
	gateValidator         func(string) bool
	filterModeValidator   func(string) bool
	minSeverityValidator  func(string) bool
	formatValidator       func(string) bool
	promptFormatValidator func(string) bool
)

// SetReviewValidators wires the enum predicates used by ValidateReview. Called
// once from the cli package init so config can validate [review] values without
// importing engine/github.
func SetReviewValidators(gate, filterMode, minSeverity, format, promptFormat func(string) bool) {
	gateValidator, filterModeValidator, minSeverityValidator, formatValidator, promptFormatValidator = gate, filterMode, minSeverity, format, promptFormat
}

// ValidateReview rejects an out-of-set [review] enum or an unparsable timeout,
// returning a typed config.invalid CLIError (Exit 2), the right namespace for a
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
	if r.Format != "" && formatValidator != nil && !formatValidator(r.Format) {
		return invalidReview("format", r.Format, "full|minimal")
	}
	if r.PromptFormat != "" && promptFormatValidator != nil && !promptFormatValidator(r.PromptFormat) {
		return invalidReview("prompt_format", r.PromptFormat, "markdown|xml")
	}
	if r.Timeout != "" {
		if _, err := time.ParseDuration(r.Timeout); err != nil {
			return invalidReview("timeout", r.Timeout, "a Go duration like 300s or 5m")
		}
	}
	if r.StalledTimeout != "" {
		d, err := time.ParseDuration(r.StalledTimeout)
		if err != nil || d < 0 {
			return invalidReview("stalled_timeout", r.StalledTimeout, "a non-negative Go duration like 0s, 180s, or 5m")
		}
	}
	if err := validateProviderRetry("provider_retry", r.ProviderRetry, invalidReview); err != nil {
		return err
	}
	if r.Temperature != nil && (*r.Temperature < 0 || *r.Temperature > 2) {
		return invalidReview("temperature", fmt.Sprintf("%v", *r.Temperature), "a number between 0 and 2")
	}
	switch r.Thinking {
	case "", "auto", "off", "low", "medium", "high":
	default:
		return invalidReview("thinking", r.Thinking, "auto|off|low|medium|high")
	}
	if err := validateReviewApproval(r.Approval); err != nil {
		return err
	}
	if err := validateReviewTools("tools", r.Tools, invalidReview); err != nil {
		return err
	}
	if r.Expand != nil && *r.Expand < 0 {
		return invalidReview("expand", fmt.Sprint(*r.Expand), "an integer >= 0")
	}
	if r.TokenBudget != nil && *r.TokenBudget < 0 {
		return invalidReview("token_budget", fmt.Sprint(*r.TokenBudget), "an integer >= 0")
	}
	if r.ContextHops != nil && (*r.ContextHops < 0 || *r.ContextHops > maxReviewContextHops) {
		return invalidReview("context_hops", fmt.Sprint(*r.ContextHops), fmt.Sprintf("an integer in [0,%d]", maxReviewContextHops))
	}
	if err := validateReviewSubagents(r.Subagents); err != nil {
		return err
	}
	if err := validateHostPRFilter(FilePathOrEmpty(), "review.pr_filter", r.PRFilter); err != nil {
		return err
	}
	return nil
}

func validateProviderRetry(field string, retry ProviderRetry, invalid func(string, string, string) error) error {
	if retry.MaxRetries != nil && (*retry.MaxRetries < 0 || *retry.MaxRetries > maxProviderRetryRetries) {
		return invalid(field+".max_retries", fmt.Sprint(*retry.MaxRetries), fmt.Sprintf("an integer in [0,%d]", maxProviderRetryRetries))
	}
	for _, d := range []struct {
		name string
		val  string
		want string
	}{
		{"initial_backoff", retry.InitialBackoff, "a non-negative Go duration like 0s, 5s, or 1m"},
		{"max_backoff", retry.MaxBackoff, "a non-negative Go duration like 0s, 30s, or 2m"},
		{"max_elapsed", retry.MaxElapsed, "a non-negative Go duration like 0s, 5m, or 10m"},
	} {
		if d.val == "" {
			continue
		}
		parsed, err := time.ParseDuration(d.val)
		if err != nil || parsed < 0 {
			return invalid(field+"."+d.name, d.val, d.want)
		}
	}
	return nil
}

func validateReviewTools(field string, tools ReviewTools, invalid func(string, string, string) error) error {
	if tools.MaxRetries != nil && (*tools.MaxRetries < 0 || *tools.MaxRetries > maxReviewToolRetries) {
		return invalid(field+".max_retries", fmt.Sprint(*tools.MaxRetries), fmt.Sprintf("an integer in [0,%d]", maxReviewToolRetries))
	}
	if tools.MaxTurns != nil && (*tools.MaxTurns < 0 || *tools.MaxTurns > maxReviewToolTurns) {
		return invalid(field+".max_turns", fmt.Sprint(*tools.MaxTurns), fmt.Sprintf("an integer in [0,%d]", maxReviewToolTurns))
	}
	if tools.RetryBackoff != "" {
		d, err := time.ParseDuration(tools.RetryBackoff)
		if err != nil || d < 0 {
			return invalid(field+".retry_backoff", tools.RetryBackoff, "a non-negative Go duration like 0s, 250ms, or 1s")
		}
	}
	return validateSymbolContext(field+".symbol_context", tools.SymbolContext, invalid)
}

func validateSymbolContext(field string, s SymbolContext, invalid func(string, string, string) error) error {
	if s.MaxBytes < 0 {
		return invalid(field+".max_bytes", fmt.Sprint(s.MaxBytes), "an integer >= 0")
	}
	if s.MaxFiles < 0 {
		return invalid(field+".max_files", fmt.Sprint(s.MaxFiles), "an integer >= 0")
	}
	if s.MaxParallel < 0 {
		return invalid(field+".max_parallel", fmt.Sprint(s.MaxParallel), "an integer >= 0")
	}
	return nil
}

func validateReviewSubagents(s ReviewSubagents) error {
	switch s.Mode {
	case "", "off", "auto", "always":
	default:
		return invalidReview("subagents.mode", s.Mode, "off|auto|always")
	}
	if s.MaxParallel < 0 {
		return invalidReview("subagents.max_parallel", fmt.Sprint(s.MaxParallel), "an integer >= 0")
	}
	if s.MinFiles < 0 {
		return invalidReview("subagents.min_files", fmt.Sprint(s.MinFiles), "an integer >= 0")
	}
	if s.MinContextBytes < 0 {
		return invalidReview("subagents.min_context_bytes", fmt.Sprint(s.MinContextBytes), "an integer >= 0")
	}
	if len(s.Agents) > 8 {
		return invalidReview("subagents.agents", fmt.Sprint(len(s.Agents)), "at most 8 agents")
	}
	seen := make(map[string]bool, len(s.Agents))
	for i, a := range s.Agents {
		if a.Name == "" {
			return invalidReview(fmt.Sprintf("subagents.agents[%d].name", i), "", "a non-empty name")
		}
		if seen[a.Name] {
			return invalidReview(fmt.Sprintf("subagents.agents[%d].name", i), a.Name, "unique names")
		}
		seen[a.Name] = true
		if len(a.Include) == 0 {
			return invalidReview(fmt.Sprintf("subagents.agents[%d].include", i), "", "at least one glob")
		}
		for j, g := range a.Include {
			if g == "" {
				return invalidReview(fmt.Sprintf("subagents.agents[%d].include[%d]", i, j), "", "a non-empty glob")
			}
		}
		for j, g := range a.Exclude {
			if g == "" {
				return invalidReview(fmt.Sprintf("subagents.agents[%d].exclude[%d]", i, j), "", "a non-empty glob")
			}
		}
	}
	return nil
}

func validateReviewApproval(a ApprovalPolicy) error {
	switch a.Mode {
	case "", "off", "clean", "threshold":
	default:
		return invalidReview("approval.mode", a.Mode, "off|clean|threshold")
	}
	if a.MaxPriority != "" {
		if a.Mode != "threshold" {
			return invalidReview("approval.max_priority", a.MaxPriority, "only used when approval.mode is \"threshold\"")
		}
		if !validApprovalPriority(a.MaxPriority) {
			return invalidReview("approval.max_priority", a.MaxPriority, "P0|P1|P2|P3|P4")
		}
	}
	switch a.Note {
	case "", "none", "on_findings", "always":
	default:
		return invalidReview("approval.note", a.Note, "none|on_findings|always")
	}
	return nil
}

func validApprovalPriority(s string) bool {
	switch s {
	case "P0", "P1", "P2", "P3", "P4":
		return true
	default:
		return false
	}
}

func invalidReview(field, value, want string) error {
	return &clierr.CLIError{
		Code:    "config.invalid",
		Message: fmt.Sprintf("[review].%s %q is invalid: want %s", field, RedactString(value), want),
		Hint:    "fix [review]." + field + " in " + FilePathOrEmpty(),
		Exit:    2,
	}
}
