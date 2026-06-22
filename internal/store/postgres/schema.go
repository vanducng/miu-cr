package postgres

import "fmt"

// SchemaSQL is the idempotent Postgres schema, mirroring sqlite.SchemaSQL
// table-for-table and column-for-column (types modulo dialect; time stays TEXT
// for byte-parity with the SQLite rows). No vector/embeddings column — that is
// M7. A schema-parity test asserts both backends define the same shape.
const SchemaSQL = `
CREATE TABLE IF NOT EXISTS reviews (
	id            TEXT PRIMARY KEY,
	repo_dir      TEXT NOT NULL,
	mode          TEXT NOT NULL,
	head_sha      TEXT NOT NULL,
	status        TEXT NOT NULL DEFAULT 'done',
	created_at    TEXT NOT NULL,
	findings_json TEXT NOT NULL,
	stats_json    TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS pr_findings (
	owner       TEXT NOT NULL,
	repo        TEXT NOT NULL,
	number      BIGINT NOT NULL,
	fingerprint TEXT NOT NULL,
	path        TEXT NOT NULL,
	status      TEXT NOT NULL CHECK(status IN ('posted','resolved')),
	first_seen  TEXT NOT NULL,
	last_seen   TEXT NOT NULL,
	PRIMARY KEY (owner, repo, number, fingerprint)
);
`

// embeddingSchemaTemplate is the M7 finding-embeddings schema, run conditionally
// (NOT part of SchemaSQL, so the schema-parity test stays untouched). The vector
// dimension is templated from config at create time; it is immutable per DB. The
// category/rationale columns carry the advisory prose so SimilarFindings needs no
// join. created_at stays TEXT for byte-parity with the other tables.
//
// MVP (deferred): no ivfflat/hnsw ANN index — a seq scan is fine at the small
// per-repo corpus M7 targets; an index is a future scale opt (YAGNI).
const embeddingSchemaTemplate = `
CREATE EXTENSION IF NOT EXISTS vector;
CREATE TABLE IF NOT EXISTS finding_embeddings (
	repo         TEXT NOT NULL,
	fingerprint  TEXT NOT NULL,
	model        TEXT NOT NULL,
	category     TEXT NOT NULL,
	rationale    TEXT NOT NULL,
	content_hash TEXT NOT NULL,
	embedding    vector(%d) NOT NULL,
	created_at   TEXT NOT NULL,
	PRIMARY KEY (repo, fingerprint, model)
);
`

// EmbeddingSchemaSQL renders the finding-embeddings DDL for the given vector
// dimension. dim must match the configured embedder's Dim().
func EmbeddingSchemaSQL(dim int) string {
	return fmt.Sprintf(embeddingSchemaTemplate, dim)
}
