// Package store defines the persistence interface for review records and the
// SQLite-backed implementation. The interface is the seam the M6 Postgres
// backend swaps in behind unchanged engine/CLI code.
package store

import (
	"context"
	"time"

	"github.com/vanducng/miu-cr/internal/engine"
)

// ReviewRecord is one persisted review. Findings, Stats, and Transcript are
// stored as JSON. No credential field exists or is ever written. Status is
// pending|done|failed; it defaults to "done" (CLI/engine writes one terminal
// record), and the REST path uses UpsertReview to flip a pending row to
// done/failed. Owner/Repo/Number carry PR context (zero/empty for a local
// review). Transcript/RawPrompt/RawResponse are the full audit trail for
// `history show <id>` (the user's own reviewed code, local only; never tokens).
type ReviewRecord struct {
	ID          string
	RepoDir     string
	Mode        string
	HeadSHA     string
	Status      string
	Owner       string
	Repo        string
	Number      int
	Provider    string
	Model       string
	CreatedAt   time.Time
	Findings    []engine.Finding
	Stats       map[string]any
	Transcript  []byte // JSON per-turn tool calls; nil/empty when not captured
	RawPrompt   string
	RawResponse string
	// TraceJSON is the full redacted review trace (system+user prompts, diff meta,
	// selected files, injected rules, model/provider, final response) as JSON.
	// LOCAL-only; never in the review envelope or a posted comment.
	TraceJSON string
}

// ReviewSummary is the list row for `history` — the scalar columns only (no
// findings/stats/transcript blobs), with the findings count + max severity
// projected for at-a-glance triage.
type ReviewSummary struct {
	ID            string
	CreatedAt     time.Time
	RepoDir       string
	Owner         string
	Repo          string
	Number        int
	Mode          string
	FindingsCount int
	MaxSeverity   string
	Status        string
}

// ReviewFilter narrows ListReviews. Zero values mean "no filter on this field";
// Limit<=0 means no limit.
type ReviewFilter struct {
	Repo   string // matches repo (PR) OR repo_dir (local)
	Owner  string
	Number int
	Since  time.Time
	Limit  int
}

// PrunePolicy selects records to delete. Keep>0 keeps the newest Keep records
// (deletes the rest); OlderThan deletes records created before it. Both may be
// set; a record is deleted if it matches either rule.
type PrunePolicy struct {
	Keep      int
	OlderThan time.Time
}

// Store persists and retrieves review records. UpsertReview inserts or, on id
// conflict, updates an existing row — the REST POST persists a pending record up
// front and the worker fills the final (done/failed) record under the same id.
// ListReviews/PruneReviews back the `history` command group.
type Store interface {
	SaveReview(ctx context.Context, rec ReviewRecord) (string, error)
	GetReview(ctx context.Context, id string) (ReviewRecord, error)
	UpsertReview(ctx context.Context, rec ReviewRecord) (string, error)
	ListReviews(ctx context.Context, f ReviewFilter) ([]ReviewSummary, error)
	PruneReviews(ctx context.Context, p PrunePolicy) (int, error)
	LatestReviewForPR(ctx context.Context, key PRKey) (LatestReview, bool, error)
}

// LatestReview is the minimal projection of the most-recent review for a PR key,
// over the existing reviews columns (no schema change): the saved record id and
// the head SHA it reviewed. It backs the incremental re-review skip.
type LatestReview struct {
	ID      string
	HeadSHA string
}
