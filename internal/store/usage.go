package store

import "context"

// ProviderUsageCount is the accumulated usage for one (provider, period) bucket.
// A missing bucket reads back as the zero value, not an error. InputTokens is the
// uncached input; CacheReadTokens/CacheCreationTokens are the cached buckets — the
// tokens quota meters their sum plus output so cached input is not undercounted.
type ProviderUsageCount struct {
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	Requests            int64
}

// ProviderUsageStore meters per-provider usage for the quota gate. It is a
// separate view (like PRThreadStore), obtained via UsageStore() on the concrete
// backend, so the core Store interface and its test fakes stay unchanged. The
// caller owns the period key (a window bucket); the store only sums by it.
type ProviderUsageStore interface {
	// ProviderUsage returns the accumulated usage for (provider, period). A missing
	// row is the zero value, not an error (fail-closed enforcement is the caller's).
	ProviderUsage(ctx context.Context, provider, period string) (ProviderUsageCount, error)
	// AddProviderUsage atomically increments the (provider, period) counter by the
	// given deltas (upsert). Negative deltas are not expected.
	AddProviderUsage(ctx context.Context, provider, period string, inputTokens, outputTokens, cacheReadTokens, cacheCreationTokens, requests int64) error
}
