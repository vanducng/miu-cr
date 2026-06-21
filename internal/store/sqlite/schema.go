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
CREATE TABLE IF NOT EXISTS pr_findings (
	owner       TEXT NOT NULL,
	repo        TEXT NOT NULL,
	number      INTEGER NOT NULL,
	fingerprint TEXT NOT NULL,
	path        TEXT NOT NULL,
	status      TEXT NOT NULL CHECK(status IN ('posted','resolved')),
	first_seen  TEXT NOT NULL,
	last_seen   TEXT NOT NULL,
	PRIMARY KEY (owner, repo, number, fingerprint)
);
`
