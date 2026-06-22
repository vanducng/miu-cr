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
// unique ID (no ON CONFLICT — no duplicate is expected); the ON CONFLICT upsert
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
	if err := migrate(ctx, db, SchemaSQL, "migrate postgres schema"); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// migrationLockKey is the fixed advisory-lock key serializing schema migrations.
// CREATE {EXTENSION,TABLE} IF NOT EXISTS is NOT concurrency-safe: two sessions
// both pass the existence check, then both insert the object into pg_catalog →
// "duplicate key value violates unique constraint pg_type_typname_nsp_index"
// (SQLSTATE 23505). The CI conformance + embedding suites share one DB opened
// from parallel test binaries, hitting exactly this race.
const migrationLockKey int64 = 0x6d697563725f3031 // "miucr_01"

// migrate runs ddl under migrationLockKey inside a transaction so concurrent
// Open/OpenWithEmbeddings calls serialize their CREATE … IF NOT EXISTS DDL. The
// xact-scoped advisory lock auto-releases at COMMIT.
func migrate(ctx context.Context, db *sql.DB, ddl, op string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return unavailable(op, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationLockKey); err != nil {
		return unavailable(op, err)
	}
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return unavailable(op, err)
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
		`INSERT INTO reviews (id, repo_dir, mode, head_sha, status, created_at, findings_json, stats_json)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		rec.ID, rec.RepoDir, rec.Mode, rec.HeadSHA, rec.Status,
		rec.CreatedAt.UTC().Format(time.RFC3339Nano), findingsJSON, statsJSON,
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
		`INSERT INTO reviews (id, repo_dir, mode, head_sha, status, created_at, findings_json, stats_json)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT(id) DO UPDATE SET
		   repo_dir=excluded.repo_dir, mode=excluded.mode, head_sha=excluded.head_sha,
		   status=excluded.status, findings_json=excluded.findings_json, stats_json=excluded.stats_json`,
		rec.ID, rec.RepoDir, rec.Mode, rec.HeadSHA, rec.Status,
		rec.CreatedAt.UTC().Format(time.RFC3339Nano), findingsJSON, statsJSON,
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
		rec          store.ReviewRecord
		createdAt    string
		findingsJSON string
		statsJSON    string
	)
	row := s.db.QueryRowContext(ctx,
		`SELECT id, repo_dir, mode, head_sha, status, created_at, findings_json, stats_json
		 FROM reviews WHERE id = $1`, id)
	err := row.Scan(&rec.ID, &rec.RepoDir, &rec.Mode, &rec.HeadSHA, &rec.Status, &createdAt, &findingsJSON, &statsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ReviewRecord{}, fmt.Errorf("review %q not found", id)
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
	return rec, nil
}

// EngineStore adapts this Store to engine.Store (engine.PersistRecord <->
// store.ReviewRecord), mirroring the sqlite adapter so the engine persists
// without importing the store package's record type.
type EngineStore struct{ S *Store }

// SaveReview adapts an engine.PersistRecord to the store record and persists it.
func (e EngineStore) SaveReview(ctx context.Context, rec engine.PersistRecord) (string, error) {
	return e.S.SaveReview(ctx, store.ReviewRecord{
		ID:        rec.ID,
		RepoDir:   rec.RepoDir,
		Mode:      rec.Mode,
		HeadSHA:   rec.HeadSHA,
		CreatedAt: rec.CreatedAt,
		Findings:  rec.Findings,
		Stats:     rec.Stats,
	})
}

// GetReview loads a review by id and adapts it to an engine.PersistRecord.
func (e EngineStore) GetReview(ctx context.Context, id string) (engine.PersistRecord, error) {
	r, err := e.S.GetReview(ctx, id)
	if err != nil {
		return engine.PersistRecord{}, err
	}
	return engine.PersistRecord{
		ID:        r.ID,
		RepoDir:   r.RepoDir,
		Mode:      r.Mode,
		HeadSHA:   r.HeadSHA,
		CreatedAt: r.CreatedAt,
		Findings:  r.Findings,
		Stats:     r.Stats,
	}, nil
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
