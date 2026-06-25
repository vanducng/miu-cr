package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/store"
)

var _ store.EmbeddingStore = (*Store)(nil)

// OpenWithEmbeddings opens the Store like Open, then additionally migrates the
// M7 finding-embeddings schema for the given vector dimension. A missing pgvector
// extension maps to a typed store.unavailable (never a panic); a dimension that
// conflicts with the already-created column maps to store.dim_mismatch. The base
// Open path is unchanged (M6), so the default (non-semantic) store never runs this.
func OpenWithEmbeddings(ctx context.Context, dsn string, dim int) (*Store, error) {
	s, err := Open(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := s.migrateEmbeddings(ctx, dim); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// migrateEmbeddings runs EmbeddingSchemaSQL then verifies the live column dim via
// atttypmod (after create-if-missing, so a pre-existing differing dim is caught).
func (s *Store) migrateEmbeddings(ctx context.Context, dim int) error {
	if dim < 1 || dim > config.MaxEmbeddingDim {
		return invalidDim(dim)
	}
	if err := migrate(ctx, s.db, EmbeddingSchemaSQL(dim), "migrate embedding schema"); err != nil {
		return err
	}
	got, err := s.embeddingColumnDim(ctx)
	if err != nil {
		return unavailable("inspect embedding dim", err)
	}
	if got != dim {
		return dimMismatch(dim, got)
	}
	return nil
}

// embeddingColumnDim reads the live vector dimension of finding_embeddings.embedding.
// For pgvector, atttypmod carries the declared dimension directly (no -4 offset).
func (s *Store) embeddingColumnDim(ctx context.Context) (int, error) {
	var mod int
	err := s.db.QueryRowContext(ctx,
		`SELECT a.atttypmod FROM pg_attribute a
		 JOIN pg_class c ON c.oid = a.attrelid
		 WHERE c.relname = 'finding_embeddings' AND a.attname = 'embedding'`,
	).Scan(&mod)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("finding_embeddings.embedding column not found")
	}
	if err != nil {
		return 0, err
	}
	return mod, nil
}

// dimMismatch is a typed, redacted store.dim_mismatch CLIError. Dim is immutable
// per DB; M7 fails loud rather than silently re-embedding into the wrong space.
func dimMismatch(want, got int) error {
	return &clierr.CLIError{
		Code:    "store.dim_mismatch",
		Message: fmt.Sprintf("embedding dimension mismatch: configured %d but finding_embeddings stores %d", want, got),
		Hint:    fmt.Sprintf("set [embedding] dim back to %d, or migrate to a fresh DB to change the vector space", got),
		Exit:    1,
	}
}

// invalidDim rejects a non-positive / over-max dim with a typed config.invalid
// before any vector(N) DDL is rendered, so a misconfig fails clearly instead of
// surfacing a cryptic pgvector parse error.
func invalidDim(dim int) error {
	return &clierr.CLIError{
		Code:    "config.invalid",
		Message: fmt.Sprintf("invalid embedding dim %d (must be in [1,%d])", dim, config.MaxEmbeddingDim),
		Exit:    2,
	}
}

// Embedding returns the store.EmbeddingStore view of this Store (mirrors PRThread).
func (s *Store) Embedding() store.EmbeddingStore { return s }

// UpsertFindingEmbedding writes one finding code-anchor embedding, keyed by
// (repo, fingerprint, model). On conflict it refreshes the vector and advisory
// columns (a re-review may sharpen the rationale). The vector is bound as a raw
// '[…]'::vector text cast, zero new modules.
//
// MVP (deferred): ON CONFLICT is idempotent, so re-embedding a byte-identical
// anchor is wasteful but correct; a content_hash-skip is a future cost opt.
func (s *Store) UpsertFindingEmbedding(ctx context.Context, row store.EmbeddingRow) error {
	if len(row.Vec) == 0 {
		return fmt.Errorf("upsert embedding: empty vector")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO finding_embeddings
		   (repo, fingerprint, model, category, rationale, content_hash, embedding, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7::vector, $8)
		 ON CONFLICT (repo, fingerprint, model)
		 DO UPDATE SET category=EXCLUDED.category, rationale=EXCLUDED.rationale,
		   content_hash=EXCLUDED.content_hash, embedding=EXCLUDED.embedding`,
		row.Repo, row.Fingerprint, row.Model, row.Category, row.Rationale,
		row.ContentHash, vectorLiteral(row.Vec), now,
	)
	if err != nil {
		return fmt.Errorf("upsert embedding: %w", err)
	}
	return nil
}

// SimilarFindings returns the top-K nearest prior findings by cosine distance,
// scoped strictly by repo AND model (no cross-model cosine). The query vector is
// bound as a raw '[…]'::vector cast; ordering uses pgvector's <=> cosine operator.
func (s *Store) SimilarFindings(ctx context.Context, repo, model string, vec []float32, k int) ([]store.EmbeddingHit, error) {
	if len(vec) == 0 || k <= 0 {
		return nil, nil
	}
	// embedding IS NOT NULL + NULLS LAST keep a degenerate row (NULL, or a NaN
	// cosine distance from an all-zero vector) from ever sorting to the top hit.
	rows, err := s.db.QueryContext(ctx,
		`SELECT fingerprint, category, rationale, embedding <=> $1::vector AS distance
		 FROM finding_embeddings
		 WHERE repo = $2 AND model = $3 AND embedding IS NOT NULL
		 ORDER BY embedding <=> $1::vector ASC NULLS LAST
		 LIMIT $4`,
		vectorLiteral(vec), repo, model, k,
	)
	if err != nil {
		return nil, fmt.Errorf("similar findings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []store.EmbeddingHit
	for rows.Next() {
		var h store.EmbeddingHit
		if err := rows.Scan(&h.Fingerprint, &h.Category, &h.Rationale, &h.Distance); err != nil {
			return nil, fmt.Errorf("scan embedding hit: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// vectorLiteral renders a []float32 as the pgvector text form "[v0,v1,…]" for the
// ::vector cast. Kept here (not the embed package) so the store owns its wire form.
func vectorLiteral(vec []float32) string {
	var b strings.Builder
	b.Grow(len(vec) * 12)
	b.WriteByte('[')
	for i, v := range vec {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(v), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}
