package config

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
)

// quotaProblem returns the first invalid field of a provider quota as
// (field, value, want), or empty strings when q is nil or valid. Shared by the
// user-config and host-config validators so the enum/window rules can't drift.
// An empty Dimension defaults to "tokens"; an empty Window is rejected (a quota
// without a window has no reset period).
func quotaProblem(q *QuotaConfig) (field, value, want string) {
	if q == nil {
		return "", "", ""
	}
	switch q.Dimension {
	case "", "tokens", "requests":
	default:
		return "quota.dimension", q.Dimension, "tokens|requests"
	}
	if q.Limit <= 0 {
		return "quota.limit", strconv.FormatInt(q.Limit, 10), "an integer > 0"
	}
	switch q.Window {
	case "monthly":
	case "":
		return "quota.window", "", `a Go duration >= 1s (1h/5h/24h) or "monthly"`
	default:
		// Reject sub-second windows: nonsensical for a usage quota and a likely
		// typo. (PeriodKey is panic-proof regardless; this is the fail-loud UX.)
		if d, err := time.ParseDuration(q.Window); err != nil || d < time.Second {
			return "quota.window", q.Window, `a Go duration >= 1s (1h/5h/24h) or "monthly"`
		}
	}
	return "", "", ""
}

// ValidateProviderQuotas rejects an invalid [providers.X].quota in the user
// config with a typed config.invalid CLIError (Exit 2). A nil quota = no quota.
func ValidateProviderQuotas(providers map[string]Provider) error {
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic first-error across multiple bad quotas
	for _, name := range names {
		if field, value, want := quotaProblem(providers[name].Quota); field != "" {
			return &clierr.CLIError{
				Code:    "config.invalid",
				Message: fmt.Sprintf("config providers.%s.%s %q is invalid: want %s", name, field, RedactString(value), want),
				Hint:    "fix providers." + name + "." + field,
				Exit:    2,
			}
		}
	}
	return nil
}
