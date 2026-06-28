package sqlite

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
)

func openUsageStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestProviderUsageMissingRowIsZero(t *testing.T) {
	s := openUsageStore(t)
	got, err := s.ProviderUsage(context.Background(), "zai", "2026-06")
	if err != nil {
		t.Fatalf("ProviderUsage: %v", err)
	}
	if got.InputTokens != 0 || got.OutputTokens != 0 || got.Requests != 0 {
		t.Fatalf("missing row should be zero, got %+v", got)
	}
}

func TestProviderUsageAddRoundTripAndAccumulates(t *testing.T) {
	s := openUsageStore(t)
	ctx := context.Background()
	if err := s.AddProviderUsage(ctx, "zai", "2026-06", 100, 50, 30, 10, 1); err != nil {
		t.Fatalf("add1: %v", err)
	}
	if err := s.AddProviderUsage(ctx, "zai", "2026-06", 10, 5, 3, 1, 1); err != nil {
		t.Fatalf("add2: %v", err)
	}
	got, err := s.ProviderUsage(ctx, "zai", "2026-06")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.InputTokens != 110 || got.OutputTokens != 55 || got.CacheReadTokens != 33 || got.CacheCreationTokens != 11 || got.Requests != 2 {
		t.Fatalf("want {in110 out55 cr33 cc11 req2}, got %+v", got)
	}
	// Distinct period bucket is isolated.
	other, _ := s.ProviderUsage(ctx, "zai", "2026-07")
	if other.InputTokens != 0 || other.Requests != 0 {
		t.Fatalf("other period should be zero, got %+v", other)
	}
}

func TestProviderUsageConcurrentAddsSum(t *testing.T) {
	s := openUsageStore(t)
	ctx := context.Background()
	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.AddProviderUsage(ctx, "openai", "2026-06-28", 2, 1, 3, 0, 1)
		}()
	}
	wg.Wait()
	got, err := s.ProviderUsage(ctx, "openai", "2026-06-28")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.InputTokens != 2*n || got.OutputTokens != n || got.CacheReadTokens != 3*n || got.Requests != n {
		t.Fatalf("concurrent adds lost increments: got %+v (want in=%d out=%d cr=%d req=%d)", got, 2*n, n, 3*n, n)
	}
}
