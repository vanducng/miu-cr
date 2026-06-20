// Package store defines the persistence interface for review records and the
// SQLite-backed implementation. The interface is the seam the M6 Postgres
// backend swaps in behind unchanged engine/CLI code.
package store

import (
	"context"
	"time"

	"github.com/vanducng/miu-cr/internal/engine"
)

// ReviewRecord is one persisted review. Findings and Stats are stored as JSON.
// No credential field exists or is ever written.
type ReviewRecord struct {
	ID        string
	RepoDir   string
	Mode      string
	HeadSHA   string
	CreatedAt time.Time
	Findings  []engine.Finding
	Stats     map[string]any
}

// Store persists and retrieves review records. ListReviews is deferred to a
// later milestone when a paginated consumer exists.
type Store interface {
	SaveReview(ctx context.Context, rec ReviewRecord) (string, error)
	GetReview(ctx context.Context, id string) (ReviewRecord, error)
}
