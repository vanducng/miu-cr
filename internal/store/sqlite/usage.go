package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vanducng/miu-cr/internal/store"
)

// UsageStore returns the store.ProviderUsageStore view of this Store (the same
// *sqlite.Store satisfies it), mirroring PRThread().
func (s *Store) UsageStore() store.ProviderUsageStore { return s }

// ProviderUsage reads the accumulated usage for (provider, period); a missing row
// is the zero value, not an error.
func (s *Store) ProviderUsage(ctx context.Context, provider, period string) (store.ProviderUsageCount, error) {
	var c store.ProviderUsageCount
	err := s.db.QueryRowContext(ctx,
		`SELECT input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, requests FROM provider_usage WHERE provider = ? AND period = ?`,
		provider, period,
	).Scan(&c.InputTokens, &c.OutputTokens, &c.CacheReadTokens, &c.CacheCreationTokens, &c.Requests)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ProviderUsageCount{}, nil
	}
	if err != nil {
		return store.ProviderUsageCount{}, fmt.Errorf("read provider_usage: %w", err)
	}
	return c, nil
}

// AddProviderUsage atomically increments the (provider, period) counter. The
// per-process prMu serializes writes against this single-file DB.
func (s *Store) AddProviderUsage(ctx context.Context, provider, period string, inputTokens, outputTokens, cacheReadTokens, cacheCreationTokens, requests int64) error {
	s.prMu.Lock()
	defer s.prMu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO provider_usage (provider, period, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, requests, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(provider, period) DO UPDATE SET
			input_tokens          = input_tokens          + excluded.input_tokens,
			output_tokens         = output_tokens         + excluded.output_tokens,
			cache_read_tokens     = cache_read_tokens     + excluded.cache_read_tokens,
			cache_creation_tokens = cache_creation_tokens + excluded.cache_creation_tokens,
			requests              = requests              + excluded.requests,
			updated_at            = excluded.updated_at`,
		provider, period, inputTokens, outputTokens, cacheReadTokens, cacheCreationTokens, requests, now,
	)
	if err != nil {
		return fmt.Errorf("add provider_usage: %w", err)
	}
	return nil
}
