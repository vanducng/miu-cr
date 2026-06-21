//go:build pg_integration

package postgres

import (
	"context"
	"os"
	"testing"

	"github.com/vanducng/miu-cr/internal/store"
)

// Manual key-gated live smoke: run with -tags pg_integration against a real
// Postgres reachable via MIUCR_TEST_PG_DSN. Never runs in the default suite.
//
//	go test -tags pg_integration ./internal/store/postgres -count=1
func TestPGIntegrationSmoke(t *testing.T) {
	dsn := os.Getenv("MIUCR_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("MIUCR_TEST_PG_DSN not set")
	}
	ctx := context.Background()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	id, err := s.SaveReview(ctx, store.ReviewRecord{RepoDir: "/r", Mode: "staged", HeadSHA: "deadbeef"})
	if err != nil {
		t.Fatalf("SaveReview: %v", err)
	}
	if _, err := s.GetReview(ctx, id); err != nil {
		t.Fatalf("GetReview: %v", err)
	}
}
