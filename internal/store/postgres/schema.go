package postgres

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
