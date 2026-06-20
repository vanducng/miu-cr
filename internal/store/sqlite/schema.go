package sqlite

const schemaSQL = `
CREATE TABLE IF NOT EXISTS reviews (
	id            TEXT PRIMARY KEY,
	repo_dir      TEXT NOT NULL,
	mode          TEXT NOT NULL,
	head_sha      TEXT NOT NULL,
	created_at    TEXT NOT NULL,
	findings_json TEXT NOT NULL,
	stats_json    TEXT NOT NULL
);
`
