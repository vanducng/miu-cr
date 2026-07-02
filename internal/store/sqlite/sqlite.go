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
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	// state.db holds raw_prompt (diff+rules), raw_response and transcripts. Force
	// the dir to 0700 — MkdirAll won't downgrade a dir a prior `review` (before
	// init/login) created 0755, which would leave the review corpus and the
	// -wal/-shm sidecars traversable by other local users on a shared host.
	_ = os.Chmod(dir, 0o700)
	// DSN-level pragmas so EVERY pooled connection inherits them (busy_timeout
	// is per-connection, a one-shot db.Exec only sets it on one connection,
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
	// SchemaSQL is the first write, so the DB file — and, in WAL mode, its -wal/-shm
	// sidecars (which also hold committed page data) — now exist. Lock them all to
	// owner-only, defense-in-depth atop the 0700 dir, so the persisted
	// diffs/transcripts aren't world-readable. Sidecars may be absent on some paths,
	// so ignore errors.
	_ = os.Chmod(path, 0o600)
	_ = os.Chmod(path+"-wal", 0o600)
	_ = os.Chmod(path+"-shm", 0o600)
	if err := migrateReviewColumns(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate reviews columns: %w", err)
	}
	if err := migrateProviderUsageColumns(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate provider_usage columns: %w", err)
	}
	return &Store{db: db}, nil
}

// reviewColumnMigrations are the idempotent ADD COLUMN DDLs for columns added
// after the reviews table first shipped. CREATE TABLE IF NOT EXISTS is a no-op on
// an existing table, so these backfill the columns on a pre-migration DB. Each is
// pragma-guarded (only run when the column is absent) so re-open is a no-op.
var reviewColumnMigrations = []struct{ col, ddl string }{
	{"status", `ALTER TABLE reviews ADD COLUMN status TEXT NOT NULL DEFAULT 'done' CHECK(status IN ('pending','done','failed'))`},
	{"owner", `ALTER TABLE reviews ADD COLUMN owner TEXT NOT NULL DEFAULT ''`},
	{"repo", `ALTER TABLE reviews ADD COLUMN repo TEXT NOT NULL DEFAULT ''`},
	{"number", `ALTER TABLE reviews ADD COLUMN number INTEGER NOT NULL DEFAULT 0`},
	{"provider", `ALTER TABLE reviews ADD COLUMN provider TEXT NOT NULL DEFAULT ''`},
	{"model", `ALTER TABLE reviews ADD COLUMN model TEXT NOT NULL DEFAULT ''`},
	{"transcript_json", `ALTER TABLE reviews ADD COLUMN transcript_json TEXT NOT NULL DEFAULT ''`},
	{"raw_prompt", `ALTER TABLE reviews ADD COLUMN raw_prompt TEXT NOT NULL DEFAULT ''`},
	{"raw_response", `ALTER TABLE reviews ADD COLUMN raw_response TEXT NOT NULL DEFAULT ''`},
	{"trace_json", `ALTER TABLE reviews ADD COLUMN trace_json TEXT NOT NULL DEFAULT ''`},
}

// migrateReviewColumns backfills any missing reviews column on a DB created
// before that column existed. Idempotent, a no-op once all columns are present.
func migrateReviewColumns(db *sql.DB) error {
	have, err := tableColumns(db, "reviews")
	if err != nil {
		return err
	}
	for _, m := range reviewColumnMigrations {
		if have[m.col] {
			continue
		}
		if _, err := db.Exec(m.ddl); err != nil {
			return err
		}
	}
	return nil
}

// providerUsageColumnMigrations backfill the cache-token columns on a
// provider_usage table created before they existed (CREATE TABLE IF NOT EXISTS is
// a no-op on an existing table). Idempotent via the column-presence guard.
var providerUsageColumnMigrations = []struct{ col, ddl string }{
	{"cache_read_tokens", `ALTER TABLE provider_usage ADD COLUMN cache_read_tokens INTEGER NOT NULL DEFAULT 0`},
	{"cache_creation_tokens", `ALTER TABLE provider_usage ADD COLUMN cache_creation_tokens INTEGER NOT NULL DEFAULT 0`},
}

// migrateProviderUsageColumns backfills any missing provider_usage column on a DB
// created before that column existed. Idempotent once all columns are present.
func migrateProviderUsageColumns(db *sql.DB) error {
	have, err := tableColumns(db, "provider_usage")
	if err != nil {
		return err
	}
	for _, m := range providerUsageColumnMigrations {
		if have[m.col] {
			continue
		}
		if _, err := db.Exec(m.ddl); err != nil {
			return err
		}
	}
	return nil
}

// tableColumns returns the column set of table. table MUST be a trusted
// compile-time literal (PRAGMA cannot bind a parameter, so it is interpolated);
// never pass user/config/network-derived input here.
func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	have := map[string]bool{}
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
			return nil, err
		}
		have[name] = true
	}
	return have, rows.Err()
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
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
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

// UpsertReview inserts rec or, on id conflict, updates the existing row. The REST
// path persists a pending record up front then re-saves the final (done/failed)
// record under the same id (id is the PK).
func (s *Store) UpsertReview(ctx context.Context, rec store.ReviewRecord) (string, error) {
	rec, findingsJSON, statsJSON, err := prepReview(rec)
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO reviews (id, repo_dir, mode, head_sha, status, created_at, findings_json, stats_json,
		   owner, repo, number, provider, model, transcript_json, raw_prompt, raw_response, trace_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
		 FROM reviews WHERE id = ?`, id)
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
	q, args := store.ListReviewsQuery(f, store.SqlitePlaceholder)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list reviews: %w", err)
	}
	return store.ScanSummaries(rows)
}

// PruneReviews deletes records per p and returns the deleted count.
func (s *Store) PruneReviews(ctx context.Context, p store.PrunePolicy) (int, error) {
	return store.ExecPruneSqlite(ctx, s.db, p)
}

// LatestReviewForPR returns the newest review's id + head SHA for the PR key, or
// ok=false when none exists. Over the existing columns, no schema change.
func (s *Store) LatestReviewForPR(ctx context.Context, key store.PRKey) (store.LatestReview, bool, error) {
	var lr store.LatestReview
	row := s.db.QueryRowContext(ctx, store.LatestReviewForPRQuery(store.SqlitePlaceholder), key.Owner, key.Repo, key.Number)
	switch err := row.Scan(&lr.ID, &lr.HeadSHA); {
	case errors.Is(err, sql.ErrNoRows):
		return store.LatestReview{}, false, nil
	case err != nil:
		return store.LatestReview{}, false, fmt.Errorf("latest review for pr: %w", err)
	}
	return lr, true, nil
}

// EngineStore adapts this Store to engine.Store (engine.PersistRecord <->
// store.ReviewRecord), letting the engine persist without importing the store
// package's record type.
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
