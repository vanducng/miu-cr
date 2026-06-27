// Package postgres is the opt-in Postgres store backend behind the unchanged M5
// store interfaces. It uses pgx/v5 via its database/sql stdlib adapter so the
// *sql.DB/Tx shape is shared with the sqlite backend. It is pure-Go (pgx is
// pure-Go) so CGO_ENABLED=0 still builds. It never persists credentials.
package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/store"
)

// connectTimeout bounds the initial Ping so a bad host fast-fails instead of
// hanging on the default TCP connect timeout.
const connectTimeout = 10 * time.Second

// Store is the Postgres-backed review store. It holds no per-process write lock:
// Postgres serializes via MVCC, and a per-process lock would defeat a
// multi-instance serve. SaveReview is a plain INSERT of a freshly-generated
// unique ID (no ON CONFLICT, no duplicate is expected); the ON CONFLICT upsert
// serialization applies to pr_findings (see UpsertPosted). Multi-row writes stay
// transactional (BeginTx/Commit).
type Store struct {
	db *sql.DB
}

var (
	_ store.Store         = (*Store)(nil)
	_ store.PRThreadStore = (*Store)(nil)
	_ engine.Store        = EngineStore{}
)

// Open dials the Postgres DSN via pgx's database/sql adapter, Pings within a
// bounded timeout, and idempotently migrates the schema. Any failure is mapped
// to a typed store.unavailable CLIError with a fully redacted message so the DSN
// password never leaks. Driver name "pgx" is the stdlib registration.
func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, unavailable("open postgres", err)
	}
	// Postgres is a network DB shared across instances; bound the pool so a
	// long-running multi-instance serve can't exhaust max_connections. The
	// single-file SQLite backend needs no such limits (its busy_timeout/WAL story
	// is separate).
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)
	pingCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, unavailable("connect postgres", err)
	}
	if err := migrate(ctx, db, postgresMigrations, "migrate postgres schema"); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

var postgresMigrations = []schemaMigration{
	{Name: "0001_reviews_pr_findings", SQL: SchemaSQL},
	{Name: "0002_reviews_extended_columns", SQL: alterAddReviewColumnsSQL},
	{Name: "0003_host_schema", SQL: HostSchemaSQL},
}

// alterAddReviewColumnsSQL idempotently adds the post-ship reviews columns to a
// pre-migration DB. Each is ADD COLUMN IF NOT EXISTS so re-open is a no-op.
const alterAddReviewColumnsSQL = `
ALTER TABLE reviews ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'done' CHECK(status IN ('pending','done','failed'));
ALTER TABLE reviews ADD COLUMN IF NOT EXISTS owner TEXT NOT NULL DEFAULT '';
ALTER TABLE reviews ADD COLUMN IF NOT EXISTS repo TEXT NOT NULL DEFAULT '';
ALTER TABLE reviews ADD COLUMN IF NOT EXISTS number BIGINT NOT NULL DEFAULT 0;
ALTER TABLE reviews ADD COLUMN IF NOT EXISTS provider TEXT NOT NULL DEFAULT '';
ALTER TABLE reviews ADD COLUMN IF NOT EXISTS model TEXT NOT NULL DEFAULT '';
ALTER TABLE reviews ADD COLUMN IF NOT EXISTS transcript_json TEXT NOT NULL DEFAULT '';
ALTER TABLE reviews ADD COLUMN IF NOT EXISTS raw_prompt TEXT NOT NULL DEFAULT '';
ALTER TABLE reviews ADD COLUMN IF NOT EXISTS raw_response TEXT NOT NULL DEFAULT '';
ALTER TABLE reviews ADD COLUMN IF NOT EXISTS trace_json TEXT NOT NULL DEFAULT '';`

// migrationLockKey is the fixed advisory-lock key serializing schema migrations.
// CREATE {EXTENSION,TABLE} IF NOT EXISTS is NOT concurrency-safe: two sessions
// both pass the existence check, then both insert the object into pg_catalog →
// "duplicate key value violates unique constraint pg_type_typname_nsp_index"
// (SQLSTATE 23505). The CI conformance + embedding suites share one DB opened
// from parallel test binaries, hitting exactly this race.
const migrationLockKey int64 = 0x6d697563725f3031 // "miucr_01"

type schemaMigration struct {
	Name string
	SQL  string
}

const schemaMigrationsSQL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
	name       TEXT PRIMARY KEY,
	applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);`

// migrate runs migrations under migrationLockKey inside a transaction so concurrent
// Open/OpenWithEmbeddings calls serialize their schema changes. The xact-scoped
// advisory lock auto-releases at COMMIT.
func migrate(ctx context.Context, db *sql.DB, migrations []schemaMigration, op string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return unavailable(op, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationLockKey); err != nil {
		return unavailable(op, err)
	}
	if _, err := tx.ExecContext(ctx, schemaMigrationsSQL); err != nil {
		return unavailable(op, err)
	}
	for _, m := range migrations {
		if m.Name == "" || m.SQL == "" {
			return unavailable(op, fmt.Errorf("invalid schema migration"))
		}
		applied := false
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name=$1)`, m.Name).Scan(&applied); err != nil {
			return unavailable(op, err)
		}
		if applied {
			continue
		}
		if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
			return unavailable(op, fmt.Errorf("%s: %w", m.Name, err))
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, m.Name); err != nil {
			return unavailable(op, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return unavailable(op, err)
	}
	return nil
}

// unavailable wraps a backend failure as a typed, redacted store.unavailable
// CLIError. The DSN/password can appear in the underlying error (host, userinfo)
// so the message is funneled through config.RedactString before it escapes.
func unavailable(op string, err error) error {
	return &clierr.CLIError{
		Code:      "store.unavailable",
		Message:   config.RedactString(fmt.Sprintf("%s: %v", op, err)),
		Hint:      "check the postgres backend is reachable and MIUCR_PG_DSN / [store] dsn is correct",
		Exit:      1,
		SafeRetry: true,
	}
}

// Close releases the underlying database handle.
func (s *Store) Close() error { return s.db.Close() }

// SaveReview persists rec (assigning an ID and timestamp when absent) and returns
// the record ID.
func (s *Store) SaveReview(ctx context.Context, rec store.ReviewRecord) (string, error) {
	rec, findingsJSON, statsJSON, err := prepReview(rec)
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO reviews (id, repo_dir, mode, head_sha, status, created_at, findings_json, stats_json,
		   owner, repo, number, provider, model, transcript_json, raw_prompt, raw_response, trace_json)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
		rec.ID, rec.RepoDir, rec.Mode, rec.HeadSHA, rec.Status,
		rec.CreatedAt.UTC().Format(time.RFC3339Nano), findingsJSON, statsJSON,
		rec.Owner, rec.Repo, rec.Number, rec.Provider, rec.Model,
		string(rec.Transcript), rec.RawPrompt, rec.RawResponse, rec.TraceJSON,
	)
	if err != nil {
		return "", fmt.Errorf("insert review: %w", err)
	}
	return rec.ID, nil
}

// UpsertReview inserts rec or, on id conflict, updates the existing row (id is
// the PK). Mirrors the sqlite upsert so the REST pending→done/failed flip works
// on both backends.
func (s *Store) UpsertReview(ctx context.Context, rec store.ReviewRecord) (string, error) {
	rec, findingsJSON, statsJSON, err := prepReview(rec)
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO reviews (id, repo_dir, mode, head_sha, status, created_at, findings_json, stats_json,
		   owner, repo, number, provider, model, transcript_json, raw_prompt, raw_response, trace_json)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		 ON CONFLICT(id) DO UPDATE SET
		   repo_dir=excluded.repo_dir, mode=excluded.mode, head_sha=excluded.head_sha,
		   status=excluded.status, findings_json=excluded.findings_json, stats_json=excluded.stats_json,
		   owner=excluded.owner, repo=excluded.repo, number=excluded.number,
		   provider=excluded.provider, model=excluded.model, transcript_json=excluded.transcript_json,
		   raw_prompt=excluded.raw_prompt, raw_response=excluded.raw_response, trace_json=excluded.trace_json`,
		rec.ID, rec.RepoDir, rec.Mode, rec.HeadSHA, rec.Status,
		rec.CreatedAt.UTC().Format(time.RFC3339Nano), findingsJSON, statsJSON,
		rec.Owner, rec.Repo, rec.Number, rec.Provider, rec.Model,
		string(rec.Transcript), rec.RawPrompt, rec.RawResponse, rec.TraceJSON,
	)
	if err != nil {
		return "", fmt.Errorf("upsert review: %w", err)
	}
	return rec.ID, nil
}

// prepReview fills defaults (id, created_at, status=done) and marshals findings +
// stats, shared by SaveReview and UpsertReview.
func prepReview(rec store.ReviewRecord) (store.ReviewRecord, string, string, error) {
	if rec.ID == "" {
		id, err := newID()
		if err != nil {
			return rec, "", "", err
		}
		rec.ID = id
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	if rec.Status == "" {
		rec.Status = "done"
	}
	findings := rec.Findings
	if findings == nil {
		findings = []engine.Finding{}
	}
	findingsJSON, err := json.Marshal(findings)
	if err != nil {
		return rec, "", "", fmt.Errorf("marshal findings: %w", err)
	}
	stats := rec.Stats
	if stats == nil {
		stats = map[string]any{}
	}
	statsJSON, err := json.Marshal(stats)
	if err != nil {
		return rec, "", "", fmt.Errorf("marshal stats: %w", err)
	}
	return rec, string(findingsJSON), string(statsJSON), nil
}

// GetReview loads a persisted review by id, returning an error when none exists.
func (s *Store) GetReview(ctx context.Context, id string) (store.ReviewRecord, error) {
	var (
		rec            store.ReviewRecord
		createdAt      string
		findingsJSON   string
		statsJSON      string
		transcriptJSON string
	)
	row := s.db.QueryRowContext(ctx,
		`SELECT id, repo_dir, mode, head_sha, status, created_at, findings_json, stats_json,
		   owner, repo, number, provider, model, transcript_json, raw_prompt, raw_response, trace_json
		 FROM reviews WHERE id = $1`, id)
	err := row.Scan(&rec.ID, &rec.RepoDir, &rec.Mode, &rec.HeadSHA, &rec.Status, &createdAt, &findingsJSON, &statsJSON,
		&rec.Owner, &rec.Repo, &rec.Number, &rec.Provider, &rec.Model, &transcriptJSON, &rec.RawPrompt, &rec.RawResponse, &rec.TraceJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ReviewRecord{}, fmt.Errorf("review %q: %w", id, store.ErrReviewNotFound)
	}
	if err != nil {
		return store.ReviewRecord{}, err
	}
	if t, perr := time.Parse(time.RFC3339Nano, createdAt); perr == nil {
		rec.CreatedAt = t
	}
	if err := json.Unmarshal([]byte(findingsJSON), &rec.Findings); err != nil {
		return store.ReviewRecord{}, fmt.Errorf("unmarshal findings: %w", err)
	}
	if err := json.Unmarshal([]byte(statsJSON), &rec.Stats); err != nil {
		return store.ReviewRecord{}, fmt.Errorf("unmarshal stats: %w", err)
	}
	if transcriptJSON != "" {
		rec.Transcript = []byte(transcriptJSON)
	}
	return rec, nil
}

// ListReviews returns summary rows matching f, newest first.
func (s *Store) ListReviews(ctx context.Context, f store.ReviewFilter) ([]store.ReviewSummary, error) {
	q, args := store.ListReviewsQuery(f, store.PostgresPlaceholder)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list reviews: %w", err)
	}
	return store.ScanSummaries(rows)
}

// PruneReviews deletes records per p and returns the deleted count.
func (s *Store) PruneReviews(ctx context.Context, p store.PrunePolicy) (int, error) {
	return store.ExecPrunePostgres(ctx, s.db, p)
}

// LatestReviewForPR returns the newest review's id + head SHA for the PR key, or
// ok=false when none exists. Over the existing columns, no schema change.
func (s *Store) LatestReviewForPR(ctx context.Context, key store.PRKey) (store.LatestReview, bool, error) {
	var lr store.LatestReview
	row := s.db.QueryRowContext(ctx, store.LatestReviewForPRQuery(store.PostgresPlaceholder), key.Owner, key.Repo, key.Number)
	switch err := row.Scan(&lr.ID, &lr.HeadSHA); {
	case errors.Is(err, sql.ErrNoRows):
		return store.LatestReview{}, false, nil
	case err != nil:
		return store.LatestReview{}, false, fmt.Errorf("latest review for pr: %w", err)
	}
	return lr, true, nil
}

// EngineStore adapts this Store to engine.Store (engine.PersistRecord <->
// store.ReviewRecord), mirroring the sqlite adapter so the engine persists
// without importing the store package's record type.
type EngineStore struct{ S *Store }

// SaveReview adapts an engine.PersistRecord to the store record and persists it.
func (e EngineStore) SaveReview(ctx context.Context, rec engine.PersistRecord) (string, error) {
	return e.S.SaveReview(ctx, store.ReviewRecord{
		ID:          rec.ID,
		RepoDir:     rec.RepoDir,
		Mode:        rec.Mode,
		HeadSHA:     rec.HeadSHA,
		Owner:       rec.Owner,
		Repo:        rec.Repo,
		Number:      rec.Number,
		Provider:    rec.Provider,
		Model:       rec.Model,
		CreatedAt:   rec.CreatedAt,
		Findings:    rec.Findings,
		Stats:       rec.Stats,
		Transcript:  rec.Transcript,
		RawPrompt:   rec.RawPrompt,
		RawResponse: rec.RawResponse,
		TraceJSON:   rec.TraceJSON,
	})
}

// GetReview loads a review by id and adapts it to an engine.PersistRecord.
func (e EngineStore) GetReview(ctx context.Context, id string) (engine.PersistRecord, error) {
	r, err := e.S.GetReview(ctx, id)
	if err != nil {
		return engine.PersistRecord{}, err
	}
	return engine.PersistRecord{
		ID:          r.ID,
		RepoDir:     r.RepoDir,
		Mode:        r.Mode,
		HeadSHA:     r.HeadSHA,
		Owner:       r.Owner,
		Repo:        r.Repo,
		Number:      r.Number,
		Provider:    r.Provider,
		Model:       r.Model,
		CreatedAt:   r.CreatedAt,
		Findings:    r.Findings,
		Stats:       r.Stats,
		Transcript:  r.Transcript,
		RawPrompt:   r.RawPrompt,
		RawResponse: r.RawResponse,
		TraceJSON:   r.TraceJSON,
	}, nil
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
