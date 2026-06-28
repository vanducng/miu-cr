package postgres

import "fmt"

// SchemaSQL is the idempotent Postgres schema, mirroring sqlite.SchemaSQL
// table-for-table and column-for-column (types modulo dialect; time stays TEXT
// for byte-parity with the SQLite rows). No vector/embeddings column, that is
// M7. A schema-parity test asserts both backends define the same shape.
const SchemaSQL = `
CREATE TABLE IF NOT EXISTS reviews (
	id              TEXT PRIMARY KEY,
	repo_dir        TEXT NOT NULL,
	mode            TEXT NOT NULL,
	head_sha        TEXT NOT NULL,
	status          TEXT NOT NULL DEFAULT 'done' CHECK(status IN ('pending','done','failed')),
	created_at      TEXT NOT NULL,
	findings_json   TEXT NOT NULL,
	stats_json      TEXT NOT NULL,
	owner           TEXT NOT NULL DEFAULT '',
	repo            TEXT NOT NULL DEFAULT '',
	number          BIGINT NOT NULL DEFAULT 0,
	provider        TEXT NOT NULL DEFAULT '',
	model           TEXT NOT NULL DEFAULT '',
	transcript_json TEXT NOT NULL DEFAULT '',
	raw_prompt      TEXT NOT NULL DEFAULT '',
	raw_response    TEXT NOT NULL DEFAULT '',
	trace_json      TEXT NOT NULL DEFAULT ''
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
` + ProviderUsageSchemaSQL

// ProviderUsageSchemaSQL is the per-provider usage-counter table. Split into its
// own const so it can also run as the 0004 migration (SchemaSQL=0001 won't re-run
// on an existing DB), while staying part of SchemaSQL for the schema-parity test.
const ProviderUsageSchemaSQL = `
CREATE TABLE IF NOT EXISTS provider_usage (
	provider      TEXT NOT NULL,
	period        TEXT NOT NULL,
	input_tokens  BIGINT NOT NULL DEFAULT 0,
	output_tokens BIGINT NOT NULL DEFAULT 0,
	requests      BIGINT NOT NULL DEFAULT 0,
	updated_at    TEXT NOT NULL,
	PRIMARY KEY (provider, period)
);
`

const HostSchemaSQL = `
CREATE TABLE IF NOT EXISTS host_repos (
	id             BIGSERIAL PRIMARY KEY,
	name           TEXT NOT NULL UNIQUE,
	owner          TEXT NOT NULL,
	repo           TEXT NOT NULL,
	slug           TEXT NOT NULL UNIQUE,
	git_url        TEXT NOT NULL,
	default_branch TEXT NOT NULL,
	github_account TEXT NOT NULL,
	enabled        BOOLEAN NOT NULL DEFAULT true,
	poll           BOOLEAN NOT NULL DEFAULT true,
	config_hash    TEXT NOT NULL DEFAULT '',
	created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE(owner, repo)
);

CREATE TABLE IF NOT EXISTS host_pr_sessions (
	id         BIGSERIAL PRIMARY KEY,
	repo_id    BIGINT NOT NULL REFERENCES host_repos(id) ON DELETE CASCADE,
	number     BIGINT NOT NULL,
	state      TEXT NOT NULL DEFAULT 'open' CHECK(state IN ('open','closed','merged')),
	head_sha   TEXT NOT NULL DEFAULT '',
	base_sha   TEXT NOT NULL DEFAULT '',
	branch     TEXT NOT NULL DEFAULT '',
	title      TEXT NOT NULL DEFAULT '',
	review_id  TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE(repo_id, number)
);

CREATE TABLE IF NOT EXISTS host_jobs (
	id           BIGSERIAL PRIMARY KEY,
	repo_id      BIGINT NOT NULL REFERENCES host_repos(id) ON DELETE CASCADE,
	session_id   BIGINT REFERENCES host_pr_sessions(id) ON DELETE SET NULL,
	number       BIGINT NOT NULL,
	head_sha     TEXT NOT NULL,
	base_sha     TEXT NOT NULL DEFAULT '',
	policy_hash  TEXT NOT NULL,
	prompt_hash  TEXT NOT NULL,
	rules_hash   TEXT NOT NULL,
	dedupe_key   TEXT NOT NULL UNIQUE,
	status       TEXT NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','running','done','failed','canceled')),
	priority     INTEGER NOT NULL DEFAULT 0,
	attempts     INTEGER NOT NULL DEFAULT 0,
	lease_owner  TEXT NOT NULL DEFAULT '',
	lease_until  TIMESTAMPTZ,
	review_id    TEXT NOT NULL DEFAULT '',
	error        TEXT NOT NULL DEFAULT '',
	available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
	completed_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS host_job_attempts (
	id          BIGSERIAL PRIMARY KEY,
	job_id      BIGINT NOT NULL REFERENCES host_jobs(id) ON DELETE CASCADE,
	attempt     INTEGER NOT NULL,
	worker_id   TEXT NOT NULL,
	status      TEXT NOT NULL DEFAULT 'running' CHECK(status IN ('running','done','failed','canceled')),
	error       TEXT NOT NULL DEFAULT '',
	started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
	finished_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS host_workspaces (
	id           BIGSERIAL PRIMARY KEY,
	repo_id      BIGINT NOT NULL REFERENCES host_repos(id) ON DELETE CASCADE,
	session_id   BIGINT REFERENCES host_pr_sessions(id) ON DELETE SET NULL,
	number       BIGINT NOT NULL,
	path         TEXT NOT NULL UNIQUE,
	state        TEXT NOT NULL DEFAULT 'active' CHECK(state IN ('active','inactive','deleted')),
	head_sha     TEXT NOT NULL DEFAULT '',
	size_bytes   BIGINT NOT NULL DEFAULT 0,
	last_used_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS host_poll_cursors (
	repo_id        BIGINT NOT NULL REFERENCES host_repos(id) ON DELETE CASCADE,
	source         TEXT NOT NULL,
	cursor_value   TEXT NOT NULL DEFAULT '',
	last_polled_at TIMESTAMPTZ,
	updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY(repo_id, source)
);

CREATE INDEX IF NOT EXISTS host_jobs_claim_idx ON host_jobs (status, available_at, priority DESC, created_at);
CREATE INDEX IF NOT EXISTS host_jobs_lease_idx ON host_jobs (status, lease_until);
CREATE UNIQUE INDEX IF NOT EXISTS host_job_attempts_job_attempt_idx ON host_job_attempts (job_id, attempt);
CREATE INDEX IF NOT EXISTS host_sessions_state_idx ON host_pr_sessions (state, updated_at);
CREATE INDEX IF NOT EXISTS host_workspaces_prune_idx ON host_workspaces (state, last_used_at);
`

// embeddingSchemaTemplate is the M7 finding-embeddings schema, run conditionally
// (NOT part of SchemaSQL, so the schema-parity test stays untouched). The vector
// dimension is templated from config at create time; it is immutable per DB. The
// category/rationale columns carry the advisory prose so SimilarFindings needs no
// join. created_at stays TEXT for byte-parity with the other tables.
//
// MVP (deferred): no ivfflat/hnsw ANN index, a seq scan is fine at the small
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
