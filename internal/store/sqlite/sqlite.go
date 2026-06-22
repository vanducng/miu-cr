// Package sqlite is the pure-Go (modernc.org/sqlite, CGO_ENABLED=0) store
// implementation writing to a local state DB. It never persists credentials.
package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/store"
)

// Store is the pure-Go SQLite-backed review store; it persists findings/stats but
// never credentials.
type Store struct {
	db   *sql.DB
	prMu sync.Mutex
}

// DefaultPath returns ~/.config/miu/cr/state.db, sharing config.Dir() with the
// config file so both live under one miu-cr directory.
func DefaultPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.db"), nil
}

// dsn builds the modernc DSN as a file: URI so a '?'/'#' or other special char in
// path can't be mis-parsed as the query/fragment (string concatenation breaks
// there). The path is percent-escaped via url.URL; the pragmas stay DSN-level so
// busy_timeout + WAL apply to EVERY pooled connection.
func dsn(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	// Forward slashes + a leading slash so the file: URI is valid on Windows too:
	// C:\x -> /C:/x -> file:///C:/x. Without this, url.URL emits "file:C:/x" (no
	// authority), which modernc/SQLite rejects on Windows.
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{
		Scheme:   "file",
		Path:     p,
		RawQuery: "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)",
	}
	return u.String()
}

// Open opens (creating parent dirs) the state DB at path and idempotently
// migrates the schema. Driver name "sqlite" is modernc's pure-Go registration.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	// DSN-level pragmas so EVERY pooled connection inherits them (busy_timeout
	// is per-connection — a one-shot db.Exec only sets it on one connection,
	// leaving other/cross-process writers to fail SQLITE_BUSY immediately).
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(SchemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	if err := migrateReviewStatus(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate reviews.status: %w", err)
	}
	return &Store{db: db}, nil
}

// migrateReviewStatus backfills the reviews.status column on a DB created before
// status existed: CREATE TABLE IF NOT EXISTS is a no-op on an existing table, so
// the column would otherwise be missing and every status-referencing query would
// fail. Idempotent — a no-op once the column is present.
func migrateReviewStatus(db *sql.DB) error {
	has, err := hasReviewStatusColumn(db)
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	_, err = db.Exec(`ALTER TABLE reviews ADD COLUMN status TEXT NOT NULL DEFAULT 'done' CHECK(status IN ('pending','done','failed'))`)
	return err
}

func hasReviewStatusColumn(db *sql.DB) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(reviews)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == "status" {
			return true, nil
		}
	}
	return false, rows.Err()
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
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.RepoDir, rec.Mode, rec.HeadSHA, rec.Status,
		rec.CreatedAt.UTC().Format(time.RFC3339Nano), findingsJSON, statsJSON,
	)
	if err != nil {
		return "", fmt.Errorf("insert review: %w", err)
	}
	return rec.ID, nil
}

// UpsertReview inserts rec or, on id conflict, updates the existing row. The REST
// path persists a pending record up front then re-saves the final (done/failed)
// record under the same id (id is the PK).
func (s *Store) UpsertReview(ctx context.Context, rec store.ReviewRecord) (string, error) {
	rec, findingsJSON, statsJSON, err := prepReview(rec)
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO reviews (id, repo_dir, mode, head_sha, status, created_at, findings_json, stats_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
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
		 FROM reviews WHERE id = ?`, id)
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
// store.ReviewRecord), letting the engine persist without importing the store
// package's record type.
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
