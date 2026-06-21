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
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/store"
)

// Store is the pure-Go SQLite-backed review store; it persists findings/stats but
// never credentials.
type Store struct {
	db *sql.DB
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

// Open opens (creating parent dirs) the state DB at path and idempotently
// migrates the schema. Driver name "sqlite" is modernc's pure-Go registration.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error { return s.db.Close() }

// SaveReview persists rec (assigning an ID and timestamp when absent) and returns
// the record ID.
func (s *Store) SaveReview(ctx context.Context, rec store.ReviewRecord) (string, error) {
	if rec.ID == "" {
		id, err := newID()
		if err != nil {
			return "", err
		}
		rec.ID = id
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	findings := rec.Findings
	if findings == nil {
		findings = []engine.Finding{}
	}
	findingsJSON, err := json.Marshal(findings)
	if err != nil {
		return "", fmt.Errorf("marshal findings: %w", err)
	}
	stats := rec.Stats
	if stats == nil {
		stats = map[string]any{}
	}
	statsJSON, err := json.Marshal(stats)
	if err != nil {
		return "", fmt.Errorf("marshal stats: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO reviews (id, repo_dir, mode, head_sha, created_at, findings_json, stats_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.RepoDir, rec.Mode, rec.HeadSHA,
		rec.CreatedAt.UTC().Format(time.RFC3339Nano), string(findingsJSON), string(statsJSON),
	)
	if err != nil {
		return "", fmt.Errorf("insert review: %w", err)
	}
	return rec.ID, nil
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
		`SELECT id, repo_dir, mode, head_sha, created_at, findings_json, stats_json
		 FROM reviews WHERE id = ?`, id)
	err := row.Scan(&rec.ID, &rec.RepoDir, &rec.Mode, &rec.HeadSHA, &createdAt, &findingsJSON, &statsJSON)
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
