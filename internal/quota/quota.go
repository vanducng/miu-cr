// Package quota enforces a per-provider-instance usage quota at the engine
// review chokepoint. The engine defines the QuotaGate interface and stays
// storage-agnostic; this package implements it over store.ProviderUsageStore,
// so quota counting lives outside the engine (which does no filesystem/DB I/O
// for it). Built per review for one resolved provider; nil = no quota.
package quota

import (
	stdctx "context"
	"fmt"
	"time"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/store"
)

// warnThresholdPct is the percent of the limit at which Check emits a one-shot
// warning (tiered enforcement: warn before the hard cutoff).
const warnThresholdPct = 80

// Gate enforces one provider's quota for the life of a single review. Not safe
// for concurrent use; each review builds its own (the host runs one per job).
type Gate struct {
	store    store.ProviderUsageStore
	provider string
	cfg      config.QuotaConfig
	now      func() time.Time
	warn     func(string) // optional one-shot >=80% sink (stderr/log); nil = silent
	warned   bool
}

// New builds a Gate for the resolved provider's quota. now defaults to time.Now;
// warn may be nil. Returns nil when cfg is nil (no quota), so callers can assign
// the result straight to engine.Request.Quota.
func New(s store.ProviderUsageStore, provider string, cfg *config.QuotaConfig, now func() time.Time, warn func(string)) engine.QuotaGate {
	if cfg == nil || s == nil {
		return nil
	}
	if now == nil {
		now = time.Now
	}
	return &Gate{store: s, provider: provider, cfg: *cfg, now: now, warn: warn}
}

// Check blocks (fail-closed) when the accumulated usage for the current period is
// at/over the limit, warning once at >=80%. A store read error blocks too (a
// quota that can't be verified must not let usage through), but surfaces as a
// retryable store.unavailable — NOT quota.exceeded — so the serve path retries the
// job after a transient DB blip instead of marking it terminally skipped.
func (g *Gate) Check(ctx stdctx.Context) error {
	period := PeriodKey(g.cfg.Window, g.now())
	c, err := g.store.ProviderUsage(ctx, g.provider, period)
	if err != nil {
		return &clierr.CLIError{
			Code:      "store.unavailable",
			Message:   config.RedactString(fmt.Sprintf("provider %q quota check failed (fail-closed): %v", g.provider, err)),
			Hint:      "retry once the quota counter store is reachable",
			Exit:      1,
			SafeRetry: true,
			Cause:     err,
		}
	}
	used := g.used(c)
	if used >= g.cfg.Limit {
		return g.exceeded(period, used)
	}
	// float64 avoids the int64 overflow of used*100 / Limit*pct at huge counters.
	if g.warn != nil && !g.warned && float64(used) >= float64(g.cfg.Limit)*(float64(warnThresholdPct)/100) {
		g.warned = true
		g.warn(fmt.Sprintf("provider %q quota at %d%% (%d/%d %s used, period %s)",
			g.provider, int(float64(used)*100/float64(g.cfg.Limit)), used, g.cfg.Limit, g.cfg.QuotaDimension(), period))
	}
	return nil
}

// Record adds the completed pass's usage (tokens + one request) to the current
// period's counter.
func (g *Gate) Record(ctx stdctx.Context, u engine.Usage) error {
	return g.store.AddProviderUsage(ctx, g.provider, PeriodKey(g.cfg.Window, g.now()),
		u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheCreationTokens, 1)
}

// used projects the counter onto the configured dimension. tokens (default) sums
// ALL tokens processed — uncached input + both cache buckets + output — so cached
// input is metered, not undercounted; requests counts reviews.
func (g *Gate) used(c store.ProviderUsageCount) int64 {
	if g.cfg.QuotaDimension() == "requests" {
		return c.Requests
	}
	return c.InputTokens + c.CacheReadTokens + c.CacheCreationTokens + c.OutputTokens
}

func (g *Gate) exceeded(period string, used int64) error {
	return &clierr.CLIError{
		Code: "quota.exceeded",
		Message: fmt.Sprintf("provider %q usage quota exhausted: %d/%d %s used in period %s",
			g.provider, used, g.cfg.Limit, g.cfg.QuotaDimension(), period),
		Hint: "raise providers." + g.provider + ".quota.limit or wait for the window to reset",
		Exit: 2,
	}
}

// PeriodKey buckets now into the quota window. "monthly" => the calendar UTC
// month; any Go duration ("1h","5h","24h") => a fixed window indexed off the unix
// epoch (floor(unix/window)). The window string is part of the key so changing
// the configured window starts a fresh bucket rather than reusing a stale count.
func PeriodKey(window string, now time.Time) string {
	if window == "monthly" {
		return "monthly-" + now.UTC().Format("2006-01")
	}
	d, err := time.ParseDuration(window)
	if err != nil || d <= 0 {
		return window // validated upstream; defensive fallback
	}
	// Bucket in nanoseconds, not seconds: int64(d.Seconds()) truncates a
	// sub-second window to 0 (divide-by-zero) and loses precision on fractional
	// seconds. d.Nanoseconds() is >=1 for any d>0, so this never panics.
	return fmt.Sprintf("%s-%d", window, now.UTC().UnixNano()/d.Nanoseconds())
}
