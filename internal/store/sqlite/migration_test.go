package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/vanducng/miu-cr/internal/store"
)

// TestOpen_MigratesPreStatusReviewsTable proves that opening a DB whose reviews
// table predates the status column succeeds (CREATE TABLE IF NOT EXISTS is a
// no-op on the existing table) and backfills status defaulting to 'done'.
func TestOpen_MigratesPreStatusReviewsTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")

	// Seed a pre-status reviews table directly, the old schema, no status column.
	raw, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE reviews (
		id            TEXT PRIMARY KEY,
		repo_dir      TEXT NOT NULL,
		mode          TEXT NOT NULL,
		head_sha      TEXT NOT NULL,
		created_at    TEXT NOT NULL,
		findings_json TEXT NOT NULL,
		stats_json    TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("create old table: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO reviews (id, repo_dir, mode, head_sha, created_at, findings_json, stats_json)
		VALUES ('old-1', '/r', 'pr', 'sha', '2026-01-01T00:00:00Z', '[]', '{}')`); err != nil {
		t.Fatalf("seed old row: %v", err)
	}
	_ = raw.Close()

	// Open must migrate the missing column and succeed.
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open should migrate pre-status DB: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	got, err := s.GetReview(context.Background(), "old-1")
	if err != nil {
		t.Fatalf("GetReview after migrate: %v", err)
	}
	if got.Status != "done" {
		t.Fatalf("migrated status = %q, want default 'done'", got.Status)
	}

	// New writes carrying a real status still round-trip post-migration.
	if _, err := s.UpsertReview(context.Background(), store.ReviewRecord{
		ID: "new-1", Mode: "pr", Status: "pending", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("UpsertReview post-migrate: %v", err)
	}
	if got, _ := s.GetReview(context.Background(), "new-1"); got.Status != "pending" {
		t.Fatalf("post-migrate status = %q, want pending", got.Status)
	}
}

// TestOpen_MigratesPreHistoryReviewsTable proves a DB whose reviews table
// predates the history columns (owner/repo/number/provider/model/transcript/raw)
// gains them on Open without data loss, and new writes round-trip the new fields.
func TestOpen_MigratesPreHistoryReviewsTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")

	raw, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	// Old schema: status exists but none of the history columns.
	if _, err := raw.Exec(`CREATE TABLE reviews (
		id            TEXT PRIMARY KEY,
		repo_dir      TEXT NOT NULL,
		mode          TEXT NOT NULL,
		head_sha      TEXT NOT NULL,
		status        TEXT NOT NULL DEFAULT 'done',
		created_at    TEXT NOT NULL,
		findings_json TEXT NOT NULL,
		stats_json    TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("create old table: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO reviews (id, repo_dir, mode, head_sha, status, created_at, findings_json, stats_json)
		VALUES ('old-1', '/r', 'pr', 'sha', 'done', '2026-01-01T00:00:00Z', '[]', '{}')`); err != nil {
		t.Fatalf("seed old row: %v", err)
	}
	_ = raw.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open should migrate pre-history DB: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	got, err := s.GetReview(context.Background(), "old-1")
	if err != nil {
		t.Fatalf("GetReview after migrate: %v", err)
	}
	if got.RepoDir != "/r" || got.Owner != "" || got.Number != 0 {
		t.Fatalf("migrated old row wrong: %+v", got)
	}
	// Back-compat: a pre-trace_json row reads back with an empty trace.
	if got.TraceJSON != "" {
		t.Fatalf("old row TraceJSON = %q, want empty", got.TraceJSON)
	}

	if _, err := s.SaveReview(context.Background(), store.ReviewRecord{
		ID: "new-1", RepoDir: "/r", Mode: "pr", Owner: "acme", Number: 7,
		Transcript: []byte(`[{"turn":1}]`), RawPrompt: "p", RawResponse: "r",
		TraceJSON: `{"system_prompt":"sys"}`,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveReview post-migrate: %v", err)
	}
	got, _ = s.GetReview(context.Background(), "new-1")
	if got.Owner != "acme" || got.Number != 7 || string(got.Transcript) != `[{"turn":1}]` ||
		got.TraceJSON != `{"system_prompt":"sys"}` {
		t.Fatalf("post-migrate round-trip wrong: %+v", got)
	}
}

// TestOpen_StatusMigrationIdempotent proves re-opening (column already present)
// is a no-op, not an error.
func TestOpen_StatusMigrationIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	_ = s1.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open (column already present must be no-op): %v", err)
	}
	_ = s2.Close()
}
