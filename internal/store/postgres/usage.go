package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vanducng/miu-cr/internal/store"
)

// UsageStore returns the store.ProviderUsageStore view of this Store (the same
// *postgres.Store satisfies it), mirroring PRThread().
func (s *Store) UsageStore() store.ProviderUsageStore { return s }

// ProviderUsage reads the accumulated usage for (provider, period); a missing row
// is the zero value, not an error.
func (s *Store) ProviderUsage(ctx context.Context, provider, period string) (store.ProviderUsageCount, error) {
	var c store.ProviderUsageCount
	err := s.db.QueryRowContext(ctx,
		`SELECT input_tokens, output_tokens, requests FROM provider_usage WHERE provider = $1 AND period = $2`,
		provider, period,
	).Scan(&c.InputTokens, &c.OutputTokens, &c.Requests)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ProviderUsageCount{}, nil
	}
	if err != nil {
		return store.ProviderUsageCount{}, fmt.Errorf("read provider_usage: %w", err)
	}
	return c, nil
}

// AddProviderUsage atomically increments the (provider, period) counter. Postgres
// serializes the upsert; no per-process lock (mirrors UpsertPosted).
func (s *Store) AddProviderUsage(ctx context.Context, provider, period string, inputTokens, outputTokens, requests int64) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO provider_usage (provider, period, input_tokens, output_tokens, requests, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (provider, period) DO UPDATE SET
			input_tokens  = provider_usage.input_tokens  + EXCLUDED.input_tokens,
			output_tokens = provider_usage.output_tokens + EXCLUDED.output_tokens,
			requests      = provider_usage.requests      + EXCLUDED.requests,
			updated_at    = EXCLUDED.updated_at`,
		provider, period, inputTokens, outputTokens, requests, now,
	)
	if err != nil {
		return fmt.Errorf("add provider_usage: %w", err)
	}
	return nil
}
