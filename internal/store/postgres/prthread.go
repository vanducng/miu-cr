package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/vanducng/miu-cr/internal/store"
)

// PRThread returns the store.PRThreadStore view of this Store. The same
// *postgres.Store satisfies both store.Store and store.PRThreadStore.
func (s *Store) PRThread() store.PRThreadStore { return s }

// UpsertPosted records each finding as status='posted' for the PR. On conflict
// it advances last_seen and flips status back to posted (the reopen path),
// keeping the original first_seen. No per-process lock: Postgres serializes the
// upsert; the multi-row write stays atomic via BeginTx/Commit.
func (s *Store) UpsertPosted(ctx context.Context, key store.PRKey, findings []store.PRFinding) error {
	if len(findings) == 0 {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin upsert: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, f := range findings {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO pr_findings (owner, repo, number, fingerprint, path, status, first_seen, last_seen)
			 VALUES ($1, $2, $3, $4, $5, 'posted', $6, $7)
			 ON CONFLICT (owner, repo, number, fingerprint)
			 DO UPDATE SET status='posted', path=EXCLUDED.path, last_seen=EXCLUDED.last_seen`,
			key.Owner, key.Repo, key.Number, f.Fingerprint, f.Path, now, now,
		)
		if err != nil {
			return fmt.Errorf("upsert pr_finding: %w", err)
		}
	}
	return tx.Commit()
}

// MarkResolved flips the given fingerprints to status='resolved' for the PR.
func (s *Store) MarkResolved(ctx context.Context, key store.PRKey, fps []string) error {
	if len(fps) == 0 {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin resolve: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, fp := range fps {
		_, err := tx.ExecContext(ctx,
			`UPDATE pr_findings SET status='resolved', last_seen=$1
			 WHERE owner=$2 AND repo=$3 AND number=$4 AND fingerprint=$5 AND status='posted'`,
			now, key.Owner, key.Repo, key.Number, fp,
		)
		if err != nil {
			return fmt.Errorf("mark resolved: %w", err)
		}
	}
	return tx.Commit()
}

// ListFindings returns all tracked findings for the PR, filtered strictly by key.
func (s *Store) ListFindings(ctx context.Context, key store.PRKey) ([]store.PRFinding, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT fingerprint, path, status FROM pr_findings
		 WHERE owner=$1 AND repo=$2 AND number=$3`,
		key.Owner, key.Repo, key.Number,
	)
	if err != nil {
		return nil, fmt.Errorf("list pr_findings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []store.PRFinding
	for rows.Next() {
		var f store.PRFinding
		if err := rows.Scan(&f.Fingerprint, &f.Path, &f.Status); err != nil {
			return nil, fmt.Errorf("scan pr_finding: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
