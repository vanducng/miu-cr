package store_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/store"
	"github.com/vanducng/miu-cr/internal/store/postgres"
	"github.com/vanducng/miu-cr/internal/store/sqlite"
)

// backend pairs a store.Store with its store.PRThreadStore view for the shared
// conformance suite. SQLite always runs (temp file); Postgres runs only when
// MIUCR_TEST_PG_DSN is set (real CI service container or a manual DSN).
type backend struct {
	name string
	rev  store.Store
	pr   store.PRThreadStore
}

// This is package store_test (external) so it can import both sqlite and
// postgres without the store -> subpkg import cycle that an in-package test
// would create.
func backends(t *testing.T) []backend {
	t.Helper()
	var out []backend

	sq, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("sqlite open: %v", err)
	}
	t.Cleanup(func() { _ = sq.Close() })
	out = append(out, backend{name: "sqlite", rev: sq, pr: sq.PRThread()})

	if dsn := os.Getenv("MIUCR_TEST_PG_DSN"); dsn != "" {
		pg, err := postgres.Open(context.Background(), dsn)
		if err != nil {
			t.Fatalf("postgres open (MIUCR_TEST_PG_DSN set): %v", err)
		}
		t.Cleanup(func() { _ = pg.Close() })
		out = append(out, backend{name: "postgres", rev: pg, pr: pg.PRThread()})
	}
	return out
}

func TestConformanceSaveGetRoundTrip(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			ctx := context.Background()
			in := store.ReviewRecord{
				RepoDir: "/repo",
				Mode:    "staged",
				HeadSHA: "abc123",
				Findings: []engine.Finding{
					{File: "a.go", Line: 10, EndLine: 12, Severity: "high", Category: "bug", Rationale: "nil deref"},
				},
				Stats: map[string]any{"files_reviewed": float64(2)},
			}
			id, err := b.rev.SaveReview(ctx, in)
			if err != nil {
				t.Fatalf("SaveReview: %v", err)
			}
			got, err := b.rev.GetReview(ctx, id)
			if err != nil {
				t.Fatalf("GetReview: %v", err)
			}
			if got.RepoDir != in.RepoDir || got.Mode != in.Mode || got.HeadSHA != in.HeadSHA {
				t.Fatalf("scalar mismatch: %+v", got)
			}
			if !reflect.DeepEqual(got.Findings, in.Findings) {
				t.Fatalf("findings mismatch:\n got %+v\nwant %+v", got.Findings, in.Findings)
			}
			if !reflect.DeepEqual(got.Stats, in.Stats) {
				t.Fatalf("stats mismatch:\n got %+v\nwant %+v", got.Stats, in.Stats)
			}
		})
	}
}

func TestConformanceGetMissing(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			if _, err := b.rev.GetReview(context.Background(), "nope"); err == nil {
				t.Fatal("expected error for missing id")
			}
		})
	}
}

func TestConformancePRThreadUpsertResolveReopen(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			ctx := context.Background()
			key := store.PRKey{Owner: "o", Repo: "r", Number: 7}

			if err := b.pr.UpsertPosted(ctx, key, []store.PRFinding{{Fingerprint: "fp1", Path: "a.go", Status: "posted"}}); err != nil {
				t.Fatalf("UpsertPosted: %v", err)
			}
			got, err := b.pr.ListFindings(ctx, key)
			if err != nil {
				t.Fatalf("ListFindings: %v", err)
			}
			if len(got) != 1 || got[0].Status != "posted" || got[0].Path != "a.go" {
				t.Fatalf("after upsert want one posted fp, got %+v", got)
			}

			if err := b.pr.MarkResolved(ctx, key, []string{"fp1"}); err != nil {
				t.Fatalf("MarkResolved: %v", err)
			}
			got, _ = b.pr.ListFindings(ctx, key)
			if len(got) != 1 || got[0].Status != "resolved" {
				t.Fatalf("after resolve want resolved, got %+v", got)
			}

			// Reopen: re-upsert the same fingerprint flips status back to posted and
			// keeps the row (ON CONFLICT path), preserving first_seen.
			if err := b.pr.UpsertPosted(ctx, key, []store.PRFinding{{Fingerprint: "fp1", Path: "a.go", Status: "posted"}}); err != nil {
				t.Fatalf("re-UpsertPosted: %v", err)
			}
			got, _ = b.pr.ListFindings(ctx, key)
			if len(got) != 1 || got[0].Status != "posted" {
				t.Fatalf("after reopen want one posted fp, got %+v", got)
			}
		})
	}
}

// TestConformanceEmptyWrites: empty upsert/resolve are no-ops, never error.
func TestConformanceEmptyWrites(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			ctx := context.Background()
			key := store.PRKey{Owner: "o", Repo: "r", Number: 1}
			if err := b.pr.UpsertPosted(ctx, key, nil); err != nil {
				t.Fatalf("empty upsert: %v", err)
			}
			if err := b.pr.MarkResolved(ctx, key, nil); err != nil {
				t.Fatalf("empty resolve: %v", err)
			}
			got, _ := b.pr.ListFindings(ctx, key)
			if len(got) != 0 {
				t.Fatalf("empty writes must record nothing, got %+v", got)
			}
		})
	}
}
