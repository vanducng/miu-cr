package store

import "context"

// EmbeddingRow is one finding code-anchor embedding to upsert. Repo+Fingerprint+
// Model is the row key; Model is part of the key so two same-dim models never
// share a cosine space. ContentHash lets a caller skip re-embedding unchanged
// anchors. Vec is the secret-scrubbed code-anchor vector. Category/Rationale are
// stored alongside so SimilarFindings returns advisory prose without a join.
type EmbeddingRow struct {
	Repo        string
	Fingerprint string
	Model       string
	Category    string
	Rationale   string
	ContentHash string
	Vec         []float32
}

// EmbeddingHit is one retrieved prior finding from a cosine top-K query. Distance
// is the pgvector cosine distance (smaller = nearer).
type EmbeddingHit struct {
	Fingerprint string
	Category    string
	Rationale   string
	Distance    float64
}

// EmbeddingStore persists and queries finding code-anchor embeddings for M7's
// opt-in semantic recall. It is a NEW optional interface implemented only by the
// Postgres backend (sqlite omits it), intentionally separate from Store and
// PRThreadStore so the default M6 path is untouched. SimilarFindings is scoped
// by repo AND model (no cross-model cosine).
type EmbeddingStore interface {
	UpsertFindingEmbedding(ctx context.Context, row EmbeddingRow) error
	SimilarFindings(ctx context.Context, repo, model string, vec []float32, k int) ([]EmbeddingHit, error)
}
