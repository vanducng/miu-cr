package store_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

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

func TestConformanceSaveDefaultsStatusDone(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			ctx := context.Background()
			id, err := b.rev.SaveReview(ctx, store.ReviewRecord{RepoDir: "/r", Mode: "staged", HeadSHA: "h"})
			if err != nil {
				t.Fatalf("SaveReview: %v", err)
			}
			got, err := b.rev.GetReview(ctx, id)
			if err != nil {
				t.Fatalf("GetReview: %v", err)
			}
			if got.Status != "done" {
				t.Fatalf("status = %q, want done (default)", got.Status)
			}
		})
	}
}

func TestConformanceUpsertPendingToDone(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			ctx := context.Background()
			id, err := b.rev.UpsertReview(ctx, store.ReviewRecord{ID: "rev-1", Status: "pending", RepoDir: "/r", Mode: "pr", HeadSHA: ""})
			if err != nil {
				t.Fatalf("upsert pending: %v", err)
			}
			if id != "rev-1" {
				t.Fatalf("id = %q, want rev-1", id)
			}
			got, _ := b.rev.GetReview(ctx, id)
			if got.Status != "pending" {
				t.Fatalf("after pending upsert status = %q", got.Status)
			}
			if _, err := b.rev.UpsertReview(ctx, store.ReviewRecord{
				ID: "rev-1", Status: "done", RepoDir: "/r", Mode: "pr", HeadSHA: "abc",
				Findings: []engine.Finding{{File: "a.go", Line: 1, Severity: "high", Category: "bug", Rationale: "x"}},
				Stats:    map[string]any{"files_reviewed": float64(1)},
			}); err != nil {
				t.Fatalf("upsert done: %v", err)
			}
			got, err = b.rev.GetReview(ctx, id)
			if err != nil {
				t.Fatalf("GetReview after done: %v", err)
			}
			if got.Status != "done" || got.HeadSHA != "abc" || len(got.Findings) != 1 {
				t.Fatalf("after done upsert: %+v", got)
			}
		})
	}
}

func TestConformanceHistoryFieldsRoundTrip(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			ctx := context.Background()
			in := store.ReviewRecord{
				RepoDir:     "/repo",
				Mode:        "pr",
				HeadSHA:     "deadbeef",
				Owner:       "acme",
				Repo:        "widget",
				Number:      42,
				Provider:    "anthropic",
				Model:       "fable-1",
				Findings:    []engine.Finding{{File: "a.go", Line: 1, Severity: "high", Category: "bug", Rationale: "x"}},
				Stats:       map[string]any{"files_reviewed": float64(1)},
				Transcript:  []byte(`[{"turn":1,"tool":"grep","args":"foo"}]`),
				RawPrompt:   "review this diff",
				RawResponse: "found one issue",
			}
			id, err := b.rev.SaveReview(ctx, in)
			if err != nil {
				t.Fatalf("SaveReview: %v", err)
			}
			got, err := b.rev.GetReview(ctx, id)
			if err != nil {
				t.Fatalf("GetReview: %v", err)
			}
			if got.Owner != in.Owner || got.Repo != in.Repo || got.Number != in.Number ||
				got.Provider != in.Provider || got.Model != in.Model ||
				got.RawPrompt != in.RawPrompt || got.RawResponse != in.RawResponse {
				t.Fatalf("history scalar mismatch: %+v", got)
			}
			if string(got.Transcript) != string(in.Transcript) {
				t.Fatalf("transcript mismatch:\n got %s\nwant %s", got.Transcript, in.Transcript)
			}
		})
	}
}

func TestConformanceListReviewsFilterOrderLimit(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			ctx := context.Background()
			clearReviews(t, ctx, b.rev) // postgres conformance DB is shared across tests; sqlite is fresh
			base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			seed := func(id, owner, repo string, num int, at time.Time, fs []engine.Finding) {
				if _, err := b.rev.SaveReview(ctx, store.ReviewRecord{
					ID: id, RepoDir: "/r", Mode: "pr", Owner: owner, Repo: repo, Number: num,
					CreatedAt: at, Findings: fs,
				}); err != nil {
					t.Fatalf("seed %s: %v", id, err)
				}
			}
			seed("r1", "acme", "widget", 1, base, nil)
			seed("r2", "acme", "widget", 2, base.Add(time.Hour),
				[]engine.Finding{{File: "a.go", Severity: "high", Category: "bug", Rationale: "x"}})
			seed("r3", "other", "thing", 9, base.Add(2*time.Hour), nil)

			all, err := b.rev.ListReviews(ctx, store.ReviewFilter{})
			if err != nil {
				t.Fatalf("ListReviews all: %v", err)
			}
			if len(all) != 3 || all[0].ID != "r3" || all[2].ID != "r1" {
				t.Fatalf("expected newest-first r3,r2,r1; got %+v", ids(all))
			}
			if all[1].ID == "r2" {
				if all[1].FindingsCount != 1 || all[1].MaxSeverity != "high" {
					t.Fatalf("r2 projection wrong: %+v", all[1])
				}
			}

			byRepo, _ := b.rev.ListReviews(ctx, store.ReviewFilter{Repo: "widget"})
			if len(byRepo) != 2 {
				t.Fatalf("repo filter: want 2, got %d", len(byRepo))
			}
			byPR, _ := b.rev.ListReviews(ctx, store.ReviewFilter{Owner: "acme", Number: 2})
			if len(byPR) != 1 || byPR[0].ID != "r2" {
				t.Fatalf("owner+number filter: %+v", ids(byPR))
			}
			since, _ := b.rev.ListReviews(ctx, store.ReviewFilter{Since: base.Add(90 * time.Minute)})
			if len(since) != 1 || since[0].ID != "r3" {
				t.Fatalf("since filter: %+v", ids(since))
			}
			lim, _ := b.rev.ListReviews(ctx, store.ReviewFilter{Limit: 2})
			if len(lim) != 2 || lim[0].ID != "r3" {
				t.Fatalf("limit: %+v", ids(lim))
			}
		})
	}
}

func TestConformancePruneReviews(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			ctx := context.Background()
			clearReviews(t, ctx, b.rev) // postgres conformance DB is shared across tests; sqlite is fresh
			base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			for i := 0; i < 5; i++ {
				if _, err := b.rev.SaveReview(ctx, store.ReviewRecord{
					ID: string(rune('a' + i)), RepoDir: "/r", Mode: "staged",
					CreatedAt: base.Add(time.Duration(i) * time.Hour),
				}); err != nil {
					t.Fatalf("seed: %v", err)
				}
			}
			// keep newest 2 -> deletes 3.
			n, err := b.rev.PruneReviews(ctx, store.PrunePolicy{Keep: 2})
			if err != nil {
				t.Fatalf("prune keep: %v", err)
			}
			if n != 3 {
				t.Fatalf("keep=2 deleted %d, want 3", n)
			}
			left, _ := b.rev.ListReviews(ctx, store.ReviewFilter{})
			if len(left) != 2 || left[0].ID != "e" || left[1].ID != "d" {
				t.Fatalf("after keep want e,d; got %+v", ids(left))
			}
			// older-than removes the one before base+4h (id 'd'), leaving 'e'.
			n, err = b.rev.PruneReviews(ctx, store.PrunePolicy{OlderThan: base.Add(4 * time.Hour)})
			if err != nil {
				t.Fatalf("prune older: %v", err)
			}
			if n != 1 {
				t.Fatalf("older-than deleted %d, want 1", n)
			}
			left, _ = b.rev.ListReviews(ctx, store.ReviewFilter{})
			if len(left) != 1 || left[0].ID != "e" {
				t.Fatalf("after older-than want e; got %+v", ids(left))
			}
			// empty policy is a no-op.
			if n, _ := b.rev.PruneReviews(ctx, store.PrunePolicy{}); n != 0 {
				t.Fatalf("empty prune deleted %d, want 0", n)
			}
		})
	}
}

func ids(ss []store.ReviewSummary) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.ID
	}
	return out
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

// clearReviews wipes the reviews table so a test is deterministic regardless of
// rows left by other tests sharing the backend (the postgres conformance DB is
// shared; sqlite is a fresh in-memory DB per backend). An empty PrunePolicy
// no-ops by design, so use a far-future OlderThan to delete every row.
func clearReviews(t *testing.T, ctx context.Context, s store.Store) {
	t.Helper()
	if _, err := s.PruneReviews(ctx, store.PrunePolicy{OlderThan: time.Now().AddDate(1, 0, 0)}); err != nil {
		t.Fatalf("clearReviews: %v", err)
	}
}
