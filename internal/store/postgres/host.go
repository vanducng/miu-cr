package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vanducng/miu-cr/internal/store"
)

var _ store.HostStore = (*Store)(nil)

func (s *Store) ReconcileHostRepo(ctx context.Context, in store.HostRepoInput) (store.HostRepo, error) {
	if in.Slug == "" && in.Owner != "" && in.Repo != "" {
		in.Slug = in.Owner + "/" + in.Repo
	}
	if in.DefaultBranch == "" {
		in.DefaultBranch = "main"
	}
	row := s.db.QueryRowContext(ctx, `
INSERT INTO host_repos (name, owner, repo, slug, git_url, default_branch, github_account, enabled, poll, config_hash, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,now())
ON CONFLICT(owner, repo) DO UPDATE SET
	name=excluded.name,
	slug=excluded.slug,
	git_url=excluded.git_url,
	default_branch=excluded.default_branch,
	github_account=excluded.github_account,
	enabled=excluded.enabled,
	poll=excluded.poll,
	config_hash=excluded.config_hash,
	updated_at=now()
RETURNING id, name, owner, repo, slug, git_url, default_branch, github_account, enabled, poll, config_hash, created_at, updated_at`,
		in.Name, in.Owner, in.Repo, in.Slug, in.GitURL, in.DefaultBranch, in.GithubAccount, in.Enabled, in.Poll, in.ConfigHash)
	return scanHostRepo(row)
}

func (s *Store) UpsertHostPRSession(ctx context.Context, in store.HostPRSessionInput) (store.HostPRSession, error) {
	if in.State == "" {
		in.State = "open"
	}
	row := s.db.QueryRowContext(ctx, `
INSERT INTO host_pr_sessions (repo_id, number, state, head_sha, base_sha, branch, title, review_id, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now())
ON CONFLICT(repo_id, number) DO UPDATE SET
	state=excluded.state,
	head_sha=excluded.head_sha,
	base_sha=excluded.base_sha,
	branch=excluded.branch,
	title=excluded.title,
	review_id=COALESCE(NULLIF(excluded.review_id, ''), host_pr_sessions.review_id),
	updated_at=now()
RETURNING id, repo_id, number, state, head_sha, base_sha, branch, title, review_id, created_at, updated_at`,
		in.RepoID, in.Number, in.State, in.HeadSHA, in.BaseSHA, in.Branch, in.Title, in.ReviewID)
	return scanHostPRSession(row)
}

func (s *Store) EnqueueHostJob(ctx context.Context, in store.HostJobInput) (store.HostJob, bool, error) {
	if in.AvailableAt.IsZero() {
		in.AvailableAt = time.Now().UTC()
	}
	in.DedupeKey = store.HostJobDedupeKey(in)
	sessionID := nullInt64(in.SessionID)
	row := s.db.QueryRowContext(ctx, `
INSERT INTO host_jobs (repo_id, session_id, number, head_sha, base_sha, policy_hash, prompt_hash, rules_hash, dedupe_key, priority, available_at, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,now())
ON CONFLICT(dedupe_key) DO UPDATE SET
	status='queued',
	lease_owner='',
	lease_until=NULL,
	error='',
	available_at=excluded.available_at,
	updated_at=now(),
	completed_at=NULL
WHERE host_jobs.status = 'failed'
RETURNING id, repo_id, session_id, number, head_sha, base_sha, policy_hash, prompt_hash, rules_hash, dedupe_key, priority, available_at, status, attempts, lease_owner, lease_until, review_id, error, created_at, updated_at, completed_at`,
		in.RepoID, sessionID, in.Number, in.HeadSHA, in.BaseSHA, in.PolicyHash, in.PromptHash, in.RulesHash, in.DedupeKey, in.Priority, in.AvailableAt)
	job, err := scanHostJob(row)
	if err == nil {
		return job, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return store.HostJob{}, false, err
	}
	job, err = s.hostJobByDedupeKey(ctx, in.DedupeKey)
	return job, false, err
}

func (s *Store) ClaimHostJob(ctx context.Context, in store.HostJobClaimInput) (store.HostJobClaim, bool, error) {
	if in.WorkerID == "" {
		return store.HostJobClaim{}, false, errors.New("host job worker id is required")
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if in.LeaseDuration <= 0 {
		in.LeaseDuration = time.Minute
	}
	leaseUntil := now.Add(in.LeaseDuration)
	row := s.db.QueryRowContext(ctx, `
WITH picked AS (
	SELECT id
	FROM host_jobs
	WHERE (status = 'queued' AND available_at <= $1)
	   OR (status = 'running' AND lease_until <= $1)
	ORDER BY priority DESC, created_at ASC
	FOR UPDATE SKIP LOCKED
	LIMIT 1
), updated AS (
	UPDATE host_jobs j
	SET status='running',
	    attempts=j.attempts + 1,
	    lease_owner=$2,
	    lease_until=$3,
	    updated_at=$1
	FROM picked
	WHERE j.id = picked.id
	RETURNING j.id, j.repo_id, j.session_id, j.number, j.head_sha, j.base_sha, j.policy_hash, j.prompt_hash, j.rules_hash, j.dedupe_key, j.priority, j.available_at, j.status, j.attempts, j.lease_owner, j.lease_until, j.review_id, j.error, j.created_at, j.updated_at, j.completed_at
), attempt AS (
	INSERT INTO host_job_attempts (job_id, attempt, worker_id, started_at, status)
	SELECT id, attempts, $2, $1, 'running'
	FROM updated
	RETURNING id
)
SELECT updated.id, updated.repo_id, updated.session_id, updated.number, updated.head_sha, updated.base_sha, updated.policy_hash, updated.prompt_hash, updated.rules_hash, updated.dedupe_key, updated.priority, updated.available_at, updated.status, updated.attempts, updated.lease_owner, updated.lease_until, updated.review_id, updated.error, updated.created_at, updated.updated_at, updated.completed_at, attempt.id
FROM updated, attempt`,
		now, in.WorkerID, leaseUntil)
	var claim store.HostJobClaim
	job, err := scanHostJobWithAttempt(row, &claim.AttemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return store.HostJobClaim{}, false, nil
	}
	if err != nil {
		return store.HostJobClaim{}, false, err
	}
	claim.Job = job
	return claim, true, nil
}

func (s *Store) CompleteHostJob(ctx context.Context, in store.HostJobCompleteInput) error {
	if in.Status != "done" && in.Status != "failed" && in.Status != "canceled" {
		return fmt.Errorf("host job status %q is invalid", in.Status)
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
UPDATE host_jobs
SET status=$2,
    lease_owner='',
    lease_until=NULL,
    review_id=$3,
    error=$4,
    updated_at=$5,
    completed_at=$5
WHERE id=$1
  AND ($6::bigint = 0 OR attempts = (SELECT attempt FROM host_job_attempts WHERE id=$6 AND job_id=$1))`,
		in.JobID, in.Status, in.ReviewID, in.Error, now, in.AttemptID)
	if err != nil {
		return err
	}
	if affected, err := res.RowsAffected(); err == nil && affected == 0 {
		if in.AttemptID != 0 {
			if _, err := tx.ExecContext(ctx, `
UPDATE host_job_attempts
SET status='canceled',
    error='stale completion ignored',
    finished_at=$2
WHERE id=$1`, in.AttemptID, now); err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	if in.AttemptID != 0 {
		if _, err := tx.ExecContext(ctx, `
UPDATE host_job_attempts
SET status=$2,
    error=$3,
    finished_at=$4
WHERE id=$1`, in.AttemptID, in.Status, in.Error, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) UpsertHostWorkspace(ctx context.Context, in store.HostWorkspaceInput) (store.HostWorkspace, error) {
	if in.State == "" {
		in.State = "active"
	}
	if in.LastUsedAt.IsZero() {
		in.LastUsedAt = time.Now().UTC()
	}
	row := s.db.QueryRowContext(ctx, `
INSERT INTO host_workspaces (repo_id, session_id, number, path, state, head_sha, size_bytes, last_used_at, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now())
ON CONFLICT(path) DO UPDATE SET
	repo_id=excluded.repo_id,
	session_id=excluded.session_id,
	number=excluded.number,
	state=excluded.state,
	head_sha=excluded.head_sha,
	size_bytes=excluded.size_bytes,
	last_used_at=excluded.last_used_at,
	updated_at=now()
RETURNING id, repo_id, session_id, number, path, state, head_sha, size_bytes, last_used_at, created_at, updated_at`,
		in.RepoID, nullInt64(in.SessionID), in.Number, in.Path, in.State, in.HeadSHA, in.SizeBytes, in.LastUsedAt)
	return scanHostWorkspace(row)
}

func (s *Store) UpsertHostPollCursor(ctx context.Context, in store.HostPollCursorInput) error {
	var polled any
	if !in.LastPolledAt.IsZero() {
		polled = in.LastPolledAt
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO host_poll_cursors (repo_id, source, cursor_value, last_polled_at, updated_at)
VALUES ($1,$2,$3,$4,now())
ON CONFLICT(repo_id, source) DO UPDATE SET
	cursor_value=excluded.cursor_value,
	last_polled_at=excluded.last_polled_at,
	updated_at=now()`,
		in.RepoID, in.Source, in.Cursor, polled)
	return err
}

func (s *Store) GetHostPollCursor(ctx context.Context, repoID int64, source string) (store.HostPollCursor, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT repo_id, source, cursor_value, last_polled_at, updated_at
FROM host_poll_cursors
WHERE repo_id=$1 AND source=$2`, repoID, source)
	cur, err := scanHostPollCursor(row)
	if errors.Is(err, sql.ErrNoRows) {
		return store.HostPollCursor{}, false, nil
	}
	if err != nil {
		return store.HostPollCursor{}, false, err
	}
	return cur, true, nil
}

func (s *Store) PruneHost(ctx context.Context, p store.HostPrunePolicy) (store.HostPruneResult, error) {
	var out store.HostPruneResult
	if !p.FinishedAttemptsBefore.IsZero() {
		n, err := execRows(ctx, s.db, `DELETE FROM host_job_attempts WHERE finished_at IS NOT NULL AND finished_at < $1`, p.FinishedAttemptsBefore)
		if err != nil {
			return out, err
		}
		out.Attempts = n
	}
	if !p.CompletedJobsBefore.IsZero() {
		n, err := execRows(ctx, s.db, `DELETE FROM host_jobs WHERE status IN ('done','failed','canceled') AND completed_at IS NOT NULL AND completed_at < $1`, p.CompletedJobsBefore)
		if err != nil {
			return out, err
		}
		out.Jobs = n
	}
	if !p.ClosedSessionsBefore.IsZero() {
		n, err := execRows(ctx, s.db, `DELETE FROM host_pr_sessions WHERE state IN ('closed','merged') AND updated_at < $1`, p.ClosedSessionsBefore)
		if err != nil {
			return out, err
		}
		out.Sessions = n
	}
	if !p.InactiveWorkspacesBefore.IsZero() {
		n, err := execRows(ctx, s.db, `DELETE FROM host_workspaces WHERE state <> 'active' AND last_used_at < $1`, p.InactiveWorkspacesBefore)
		if err != nil {
			return out, err
		}
		out.Workspaces = n
	}
	if !p.PollCursorsBefore.IsZero() {
		n, err := execRows(ctx, s.db, `DELETE FROM host_poll_cursors WHERE updated_at < $1`, p.PollCursorsBefore)
		if err != nil {
			return out, err
		}
		out.PollCursors = n
	}
	return out, nil
}

func (s *Store) hostJobByDedupeKey(ctx context.Context, key string) (store.HostJob, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, repo_id, session_id, number, head_sha, base_sha, policy_hash, prompt_hash, rules_hash, dedupe_key, priority, available_at, status, attempts, lease_owner, lease_until, review_id, error, created_at, updated_at, completed_at
FROM host_jobs
WHERE dedupe_key=$1`, key)
	return scanHostJob(row)
}

type scanner interface {
	Scan(...any) error
}

func scanHostRepo(row scanner) (store.HostRepo, error) {
	var r store.HostRepo
	err := row.Scan(&r.ID, &r.Name, &r.Owner, &r.Repo, &r.Slug, &r.GitURL, &r.DefaultBranch, &r.GithubAccount, &r.Enabled, &r.Poll, &r.ConfigHash, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func scanHostPRSession(row scanner) (store.HostPRSession, error) {
	var s store.HostPRSession
	err := row.Scan(&s.ID, &s.RepoID, &s.Number, &s.State, &s.HeadSHA, &s.BaseSHA, &s.Branch, &s.Title, &s.ReviewID, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

func scanHostJob(row scanner) (store.HostJob, error) {
	return scanHostJobWithAttempt(row, nil)
}

func scanHostJobWithAttempt(row scanner, attemptID *int64) (store.HostJob, error) {
	var (
		j           store.HostJob
		sessionID   sql.NullInt64
		leaseUntil  sql.NullTime
		completedAt sql.NullTime
	)
	args := []any{&j.ID, &j.RepoID, &sessionID, &j.Number, &j.HeadSHA, &j.BaseSHA, &j.PolicyHash, &j.PromptHash, &j.RulesHash, &j.DedupeKey, &j.Priority, &j.AvailableAt, &j.Status, &j.Attempts, &j.LeaseOwner, &leaseUntil, &j.ReviewID, &j.Error, &j.CreatedAt, &j.UpdatedAt, &completedAt}
	if attemptID != nil {
		args = append(args, attemptID)
	}
	if err := row.Scan(args...); err != nil {
		return store.HostJob{}, err
	}
	if sessionID.Valid {
		j.SessionID = sessionID.Int64
	}
	if leaseUntil.Valid {
		t := leaseUntil.Time
		j.LeaseUntil = &t
	}
	if completedAt.Valid {
		t := completedAt.Time
		j.CompletedAt = &t
	}
	return j, nil
}

func scanHostWorkspace(row scanner) (store.HostWorkspace, error) {
	var (
		w         store.HostWorkspace
		sessionID sql.NullInt64
	)
	err := row.Scan(&w.ID, &w.RepoID, &sessionID, &w.Number, &w.Path, &w.State, &w.HeadSHA, &w.SizeBytes, &w.LastUsedAt, &w.CreatedAt, &w.UpdatedAt)
	if sessionID.Valid {
		w.SessionID = sessionID.Int64
	}
	return w, err
}

func scanHostPollCursor(row scanner) (store.HostPollCursor, error) {
	var (
		c      store.HostPollCursor
		polled sql.NullTime
	)
	err := row.Scan(&c.RepoID, &c.Source, &c.Cursor, &polled, &c.UpdatedAt)
	if polled.Valid {
		c.LastPolledAt = polled.Time
	}
	return c, err
}

func nullInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func execRows(ctx context.Context, db *sql.DB, query string, args ...any) (int, error) {
	res, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}
