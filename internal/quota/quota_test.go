package quota

import (
	stdctx "context"
	"errors"
	"testing"
	"time"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/store"
)

// fakeUsage is an in-memory ProviderUsageStore; readErr forces a fail-closed path.
type fakeUsage struct {
	c       store.ProviderUsageCount
	readErr error
	adds    int
}

func (f *fakeUsage) ProviderUsage(_ stdctx.Context, _, _ string) (store.ProviderUsageCount, error) {
	if f.readErr != nil {
		return store.ProviderUsageCount{}, f.readErr
	}
	return f.c, nil
}

func (f *fakeUsage) AddProviderUsage(_ stdctx.Context, _, _ string, in, out, reqs int64) error {
	f.adds++
	f.c.InputTokens += in
	f.c.OutputTokens += out
	f.c.Requests += reqs
	return nil
}

func fixedClock(s string) func() time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return func() time.Time { return t }
}

func newGate(t *testing.T, f *fakeUsage, cfg config.QuotaConfig, warn func(string)) engine.QuotaGate {
	t.Helper()
	return New(f, "openai", &cfg, fixedClock("2026-06-28T10:00:00Z"), warn)
}

func TestNewNilCfgIsNoGate(t *testing.T) {
	if g := New(&fakeUsage{}, "openai", nil, nil, nil); g != nil {
		t.Fatal("nil cfg should yield a nil gate (no quota)")
	}
}

func TestCheckPassesUnderLimit(t *testing.T) {
	f := &fakeUsage{c: store.ProviderUsageCount{InputTokens: 100, OutputTokens: 50}}
	g := newGate(t, f, config.QuotaConfig{Dimension: "tokens", Limit: 1000, Window: "5h"}, nil)
	if err := g.Check(stdctx.Background()); err != nil {
		t.Fatalf("under limit should pass, got %v", err)
	}
}

func TestCheckBlocksAtLimit(t *testing.T) {
	f := &fakeUsage{c: store.ProviderUsageCount{InputTokens: 700, OutputTokens: 300}} // 1000 == limit
	g := newGate(t, f, config.QuotaConfig{Dimension: "tokens", Limit: 1000, Window: "5h"}, nil)
	err := g.Check(stdctx.Background())
	if err == nil || codeOf(err) != "quota.exceeded" {
		t.Fatalf("at/over limit must block with quota.exceeded, got %v", err)
	}
}

func TestCheckWarnsAtEightyPercentOnce(t *testing.T) {
	f := &fakeUsage{c: store.ProviderUsageCount{InputTokens: 800}} // 80% of 1000
	var warns int
	g := newGate(t, f, config.QuotaConfig{Limit: 1000, Window: "24h"}, func(string) { warns++ })
	if err := g.Check(stdctx.Background()); err != nil {
		t.Fatalf("80%% should pass (not block), got %v", err)
	}
	_ = g.Check(stdctx.Background()) // still under limit; warn must not refire
	if warns != 1 {
		t.Fatalf("warn should fire exactly once, got %d", warns)
	}
}

func TestCheckFailClosedOnReadError(t *testing.T) {
	f := &fakeUsage{readErr: errors.New("db down")}
	g := newGate(t, f, config.QuotaConfig{Limit: 1000, Window: "5h"}, nil)
	err := g.Check(stdctx.Background())
	// Fail-closed (blocks) but as a RETRYABLE store.unavailable, not quota.exceeded,
	// so a transient DB blip is retried by serve, not terminally skipped.
	if err == nil || codeOf(err) != "store.unavailable" {
		t.Fatalf("unreadable counter must fail-closed with store.unavailable, got %v", err)
	}
}

func TestRequestsDimension(t *testing.T) {
	f := &fakeUsage{c: store.ProviderUsageCount{InputTokens: 1_000_000, Requests: 4}}
	// tokens are huge but the requests dimension caps at 5: 4 < 5 passes.
	g := newGate(t, f, config.QuotaConfig{Dimension: "requests", Limit: 5, Window: "monthly"}, nil)
	if err := g.Check(stdctx.Background()); err != nil {
		t.Fatalf("4/5 requests should pass, got %v", err)
	}
	f.c.Requests = 5
	if err := g.Check(stdctx.Background()); err == nil {
		t.Fatal("5/5 requests must block")
	}
}

func TestRecordAddsTokensAndOneRequest(t *testing.T) {
	f := &fakeUsage{}
	g := newGate(t, f, config.QuotaConfig{Dimension: "tokens", Limit: 1000, Window: "5h"}, nil)
	if err := g.Record(stdctx.Background(), engine.Usage{InputTokens: 30, OutputTokens: 12}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if f.c.InputTokens != 30 || f.c.OutputTokens != 12 || f.c.Requests != 1 {
		t.Fatalf("record should add tokens + 1 request, got %+v", f.c)
	}
}

func TestPeriodKey(t *testing.T) {
	jun := fixedClock("2026-06-28T10:00:00Z")()
	if got := PeriodKey("monthly", jun); got != "monthly-2026-06" {
		t.Fatalf("monthly key = %q", got)
	}
	// 5h window: 2026-06-28T10:00Z is unix 1782640800; /18000 = 99035600.
	want5h := "5h-" + itoa(jun.Unix()/int64((5*time.Hour).Seconds()))
	if got := PeriodKey("5h", jun); got != want5h {
		t.Fatalf("5h key = %q want %q", got, want5h)
	}
	// Same 5h bucket for two times 1h apart; different across the boundary.
	plus1h := jun.Add(time.Hour)
	if PeriodKey("5h", jun) != PeriodKey("5h", plus1h) {
		t.Fatal("times within the same 5h window should share a bucket")
	}
	plus6h := jun.Add(6 * time.Hour)
	if PeriodKey("5h", jun) == PeriodKey("5h", plus6h) {
		t.Fatal("times 6h apart must fall in different 5h buckets")
	}
}

func TestPeriodKeySubSecondNoPanic(t *testing.T) {
	// Validation rejects sub-second windows, but PeriodKey must never panic on one
	// (nanosecond bucketing, not int64(d.Seconds()) which truncates to 0).
	jun := fixedClock("2026-06-28T10:00:00Z")()
	if got := PeriodKey("500ms", jun); got == "" {
		t.Fatal("sub-second window should bucket, not panic/empty")
	}
}

func codeOf(err error) string {
	var c *clierr.CLIError
	if errors.As(err, &c) {
		return c.Code
	}
	return ""
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
