package postgres

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/store"
)

// TestMigrateEmbeddingsRejectsInvalidDim needs no live PG: the dim guard returns
// a typed config.invalid before any DDL touches the (nil) db handle.
func TestMigrateEmbeddingsRejectsInvalidDim(t *testing.T) {
	s := &Store{}
	for _, dim := range []int{0, -1, config.MaxEmbeddingDim + 1} {
		err := s.migrateEmbeddings(context.Background(), dim)
		if err == nil {
			t.Fatalf("dim %d must be rejected before DDL", dim)
		}
		ce, ok := err.(*clierr.CLIError)
		if !ok || ce.Code != "config.invalid" {
			t.Fatalf("dim %d: want config.invalid CLIError, got %T %v", dim, err, err)
		}
	}
}

const testDim = 4

// openEmb opens an embedding-capable store against MIUCR_TEST_PG_DSN or skips.
// Each test isolates its rows by a unique repo string so a shared DB stays clean.
func openEmb(t *testing.T, dim int) *Store {
	t.Helper()
	dsn := os.Getenv("MIUCR_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("MIUCR_TEST_PG_DSN not set")
	}
	s, err := OpenWithEmbeddings(context.Background(), dsn, dim)
	if err != nil {
		var ce *clierr.CLIError
		if asCLIError(err, &ce) && ce.Code == "store.unavailable" && strings.Contains(ce.Message, `extension "vector" is not available`) {
			t.Skip("pgvector extension not available")
		}
		t.Fatalf("OpenWithEmbeddings: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// unit vector helper: e_i in testDim space (orthonormal, so cosine distances are
// well separated and ordering is deterministic).
func basis(i int) []float32 {
	v := make([]float32, testDim)
	v[i] = 1
	return v
}

func TestEmbeddingRoundTripAndCosineOrder(t *testing.T) {
	s := openEmb(t, testDim)
	ctx := context.Background()
	repo := "owner/round-trip-" + t.Name()

	rows := []store.EmbeddingRow{
		{Repo: repo, Fingerprint: "fp-near", Model: "m1", Category: "bug", Rationale: "near", ContentHash: "h1", Vec: basis(0)},
		{Repo: repo, Fingerprint: "fp-mid", Model: "m1", Category: "style", Rationale: "mid", ContentHash: "h2", Vec: []float32{0.7, 0.7, 0, 0}},
		{Repo: repo, Fingerprint: "fp-far", Model: "m1", Category: "perf", Rationale: "far", ContentHash: "h3", Vec: basis(1)},
	}
	for _, r := range rows {
		if err := s.UpsertFindingEmbedding(ctx, r); err != nil {
			t.Fatalf("upsert %s: %v", r.Fingerprint, err)
		}
	}

	hits, err := s.SimilarFindings(ctx, repo, "m1", basis(0), 3)
	if err != nil {
		t.Fatalf("SimilarFindings: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("want 3 hits, got %d", len(hits))
	}
	if hits[0].Fingerprint != "fp-near" || hits[2].Fingerprint != "fp-far" {
		t.Fatalf("cosine order wrong: %v", []string{hits[0].Fingerprint, hits[1].Fingerprint, hits[2].Fingerprint})
	}
	if hits[0].Rationale != "near" {
		t.Fatalf("advisory prose not returned: %q", hits[0].Rationale)
	}
	for i := 1; i < len(hits); i++ {
		if hits[i].Distance < hits[i-1].Distance {
			t.Fatalf("distances not ascending: %v < %v", hits[i].Distance, hits[i-1].Distance)
		}
	}
}

func TestEmbeddingUpsertOverwrites(t *testing.T) {
	s := openEmb(t, testDim)
	ctx := context.Background()
	repo := "owner/upsert-" + t.Name()

	r := store.EmbeddingRow{Repo: repo, Fingerprint: "fp", Model: "m1", Category: "bug", Rationale: "first", ContentHash: "h1", Vec: basis(0)}
	if err := s.UpsertFindingEmbedding(ctx, r); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	r.Rationale = "second"
	if err := s.UpsertFindingEmbedding(ctx, r); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	hits, err := s.SimilarFindings(ctx, repo, "m1", basis(0), 5)
	if err != nil {
		t.Fatalf("SimilarFindings: %v", err)
	}
	if len(hits) != 1 || hits[0].Rationale != "second" {
		t.Fatalf("upsert did not overwrite single row: %+v", hits)
	}
}

func TestEmbeddingCrossModelIsolation(t *testing.T) {
	s := openEmb(t, testDim)
	ctx := context.Background()
	repo := "owner/cross-model-" + t.Name()

	must := func(r store.EmbeddingRow) {
		if err := s.UpsertFindingEmbedding(ctx, r); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	must(store.EmbeddingRow{Repo: repo, Fingerprint: "a", Model: "m1", Category: "bug", Rationale: "m1", ContentHash: "h", Vec: basis(0)})
	must(store.EmbeddingRow{Repo: repo, Fingerprint: "b", Model: "m2", Category: "bug", Rationale: "m2", ContentHash: "h", Vec: basis(0)})

	hits, err := s.SimilarFindings(ctx, repo, "m1", basis(0), 10)
	if err != nil {
		t.Fatalf("SimilarFindings: %v", err)
	}
	if len(hits) != 1 || hits[0].Fingerprint != "a" {
		t.Fatalf("cross-model leak: %+v", hits)
	}
}

func TestEmbeddingDimMismatch(t *testing.T) {
	// First open creates the column at testDim; a second open with a different dim
	// must surface store.dim_mismatch (dim is immutable per DB).
	s := openEmb(t, testDim)
	_ = s
	dsn := os.Getenv("MIUCR_TEST_PG_DSN")
	_, err := OpenWithEmbeddings(context.Background(), dsn, testDim+1)
	if err == nil {
		t.Fatal("expected dim mismatch error")
	}
	ce, ok := err.(*clierr.CLIError)
	if !ok {
		t.Fatalf("want *clierr.CLIError, got %T: %v", err, err)
	}
	if ce.Code != "store.dim_mismatch" {
		t.Fatalf("code = %q, want store.dim_mismatch", ce.Code)
	}
	if ce.Hint == "" {
		t.Fatal("store.dim_mismatch must carry an actionable hint")
	}
}
