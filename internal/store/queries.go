package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/vanducng/miu-cr/internal/engine"
)

// ph renders the nth (1-based) bind placeholder for the dialect: "?" for sqlite,
// "$n" for postgres.
type ph func(n int) string

// SqlitePlaceholder and PostgresPlaceholder are the two dialect placeholder
// renderers shared by the list/prune query builders.
func SqlitePlaceholder(int) string     { return "?" }
func PostgresPlaceholder(n int) string { return fmt.Sprintf("$%d", n) }

// ListReviewsQuery builds the summary SELECT for f. It returns the SQL plus the
// ordered args; the WHERE matches Repo against repo OR repo_dir so a local
// review (repo_dir set, repo empty) and a PR review both filter by one flag.
func ListReviewsQuery(f ReviewFilter, p ph) (string, []any) {
	var (
		where []string
		args  []any
		n     int
	)
	next := func(v any) string { n++; args = append(args, v); return p(n) }
	if f.Repo != "" {
		where = append(where, "(repo = "+next(f.Repo)+" OR repo_dir = "+next(f.Repo)+")")
	}
	if f.Owner != "" {
		where = append(where, "owner = "+next(f.Owner))
	}
	if f.Number != 0 {
		where = append(where, "number = "+next(f.Number))
	}
	if !f.Since.IsZero() {
		where = append(where, "created_at >= "+next(f.Since.UTC().Format(time.RFC3339Nano)))
	}
	q := `SELECT id, created_at, repo_dir, owner, repo, number, mode, status, findings_json FROM reviews`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY created_at DESC"
	if f.Limit > 0 {
		q += " LIMIT " + next(f.Limit)
	}
	return q, args
}

// ScanSummaries reads ListReviews rows, projecting findings_count + max_severity
// from the decoded findings JSON.
func ScanSummaries(rows *sql.Rows) ([]ReviewSummary, error) {
	defer rows.Close()
	var out []ReviewSummary
	for rows.Next() {
		var (
			s            ReviewSummary
			createdAt    string
			findingsJSON string
		)
		if err := rows.Scan(&s.ID, &createdAt, &s.RepoDir, &s.Owner, &s.Repo, &s.Number, &s.Mode, &s.Status, &findingsJSON); err != nil {
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
			s.CreatedAt = t
		}
		var findings []engine.Finding
		if err := json.Unmarshal([]byte(findingsJSON), &findings); err != nil {
			return nil, fmt.Errorf("unmarshal findings: %w", err)
		}
		s.FindingsCount = len(findings)
		s.MaxSeverity = engine.MaxSeverity(findings)
		out = append(out, s)
	}
	return out, rows.Err()
}

// PruneReviewsQuery builds a single DELETE matching records to drop per p: rows
// older than OlderThan, OR rows outside the newest Keep (by created_at). Returns
// "" when the policy is empty (nothing to do).
func PruneReviewsQuery(pol PrunePolicy, p ph) (string, []any) {
	var (
		clauses []string
		args    []any
		n       int
	)
	next := func(v any) string { n++; args = append(args, v); return p(n) }
	if !pol.OlderThan.IsZero() {
		clauses = append(clauses, "created_at < "+next(pol.OlderThan.UTC().Format(time.RFC3339Nano)))
	}
	if pol.Keep > 0 {
		clauses = append(clauses,
			"id NOT IN (SELECT id FROM reviews ORDER BY created_at DESC LIMIT "+next(pol.Keep)+")")
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return "DELETE FROM reviews WHERE " + strings.Join(clauses, " OR "), args
}

// execPrune runs a prune DELETE built by PruneReviewsQuery and returns the
// deleted-row count; an empty policy is a no-op returning 0.
func execPrune(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, pol PrunePolicy, p ph) (int, error) {
	q, args := PruneReviewsQuery(pol, p)
	if q == "" {
		return 0, nil
	}
	res, err := db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, fmt.Errorf("prune reviews: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}

// ExecPruneSqlite/ExecPrunePostgres are thin exported entry points the backends
// call so the prune logic lives once here.
func ExecPruneSqlite(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, pol PrunePolicy) (int, error) {
	return execPrune(ctx, db, pol, SqlitePlaceholder)
}

func ExecPrunePostgres(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, pol PrunePolicy) (int, error) {
	return execPrune(ctx, db, pol, PostgresPlaceholder)
}
