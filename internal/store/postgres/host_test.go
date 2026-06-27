package postgres

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vanducng/miu-cr/internal/store"
)

func TestHostSchemaSQLSeparateFromBaseSchema(t *testing.T) {
	if strings.Contains(SchemaSQL, "host_jobs") || strings.Contains(SchemaSQL, "host_repos") {
		t.Fatal("host tables must stay out of the shared schema parity SQL")
	}
	for _, table := range []string{"host_repos", "host_pr_sessions", "host_jobs", "host_job_attempts", "host_workspaces", "host_poll_cursors"} {
		if !strings.Contains(HostSchemaSQL, table) {
			t.Fatalf("HostSchemaSQL missing %s", table)
		}
	}
}

func TestHostJobDedupeKey(t *testing.T) {
	in := store.HostJobInput{RepoID: 1, Number: 2, HeadSHA: "head", PolicyHash: "policy", PromptHash: "prompt", RulesHash: "rules"}
	if got, want := len(store.HostJobDedupeKey(in)), 64; got != want {
		t.Fatalf("dedupe key len = %d, want %d", got, want)
	}
	if store.HostJobDedupeKey(in) != store.HostJobDedupeKey(in) {
		t.Fatal("dedupe key must be stable")
	}
	in.HeadSHA = "other"
	if store.HostJobDedupeKey(in) == store.HostJobDedupeKey(store.HostJobInput{RepoID: 1, Number: 2, HeadSHA: "head", PolicyHash: "policy", PromptHash: "prompt", RulesHash: "rules"}) {
		t.Fatal("dedupe key must include head sha")
	}
	a := store.HostJobInput{RepoID: 1, Number: 2, HeadSHA: "a:b", PolicyHash: "c", PromptHash: "d", RulesHash: "e"}
	b := store.HostJobInput{RepoID: 1, Number: 2, HeadSHA: "a", PolicyHash: "b:c", PromptHash: "d", RulesHash: "e"}
	if store.HostJobDedupeKey(a) == store.HostJobDedupeKey(b) {
		t.Fatal("dedupe key must encode field boundaries")
	}
}

func TestHostSchemaMigrationIdempotent(t *testing.T) {
	s := openHost(t)
	ctx := context.Background()
	var ok bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name='host_jobs')`).Scan(&ok); err != nil {
		t.Fatalf("query host_jobs: %v", err)
	}
	if !ok {
		t.Fatal("host_jobs table missing")
	}
	dsn := os.Getenv("MIUCR_TEST_PG_DSN")
	again, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	t.Cleanup(func() { _ = again.Close() })
}

func TestHostRepoReconcileUpsert(t *testing.T) {
	s := openHost(t)
	ctx := context.Background()
	in := hostRepoInput(t)
	first, err := s.ReconcileHostRepo(ctx, in)
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	in.GitURL = "https://github.com/" + in.Slug + "-renamed.git"
	in.Poll = false
	second, err := s.ReconcileHostRepo(ctx, in)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("repo id changed: %d -> %d", first.ID, second.ID)
	}
	if second.GitURL != in.GitURL || second.Poll {
		t.Fatalf("repo not updated: %+v", second)
	}
}

func TestHostSessionUpsertPreservesReviewID(t *testing.T) {
	s := openHost(t)
	ctx := context.Background()
	repo := mustHostRepo(t, s)
	first, err := s.UpsertHostPRSession(ctx, store.HostPRSessionInput{RepoID: repo.ID, Number: 9, State: "open", HeadSHA: "h1", ReviewID: "review-1"})
	if err != nil {
		t.Fatalf("first session: %v", err)
	}
	second, err := s.UpsertHostPRSession(ctx, store.HostPRSessionInput{RepoID: repo.ID, Number: 9, State: "open", HeadSHA: "h2"})
	if err != nil {
		t.Fatalf("second session: %v", err)
	}
	if first.ID != second.ID || second.ReviewID != "review-1" || second.HeadSHA != "h2" {
		t.Fatalf("session not preserved/updated: first=%+v second=%+v", first, second)
	}
}

func TestHostConcurrentEnqueueDedupe(t *testing.T) {
	s := openHost(t)
	ctx := context.Background()
	repo := mustHostRepo(t, s)
	session := mustHostSession(t, s, repo.ID, 11, "open")
	in := hostJobInput(repo.ID, session.ID, 11, uniqueName(t, "head"))
	const workers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	created := 0
	ids := map[int64]struct{}{}
	errs := []error{}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job, ok, err := s.EnqueueHostJob(ctx, in)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			if ok {
				created++
			}
			ids[job.ID] = struct{}{}
		}()
	}
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("enqueue errors: %v", errs)
	}
	if created != 1 {
		t.Fatalf("created = %d, want 1", created)
	}
	if len(ids) != 1 {
		t.Fatalf("job ids = %v, want one", ids)
	}
}

func TestHostConcurrentClaimOneOwnerPerJob(t *testing.T) {
	s := openHost(t)
	ctx := context.Background()
	repo := mustHostRepo(t, s)
	session := mustHostSession(t, s, repo.ID, 12, "open")
	if _, _, err := s.EnqueueHostJob(ctx, hostJobInput(repo.ID, session.ID, 12, uniqueName(t, "head"))); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	const workers = 8
	now := time.Now().UTC()
	var wg sync.WaitGroup
	var mu sync.Mutex
	claims := []store.HostJobClaim{}
	errs := []error{}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			claim, ok, err := s.ClaimHostJob(ctx, store.HostJobClaimInput{WorkerID: fmt.Sprintf("worker-%d", i), Now: now, LeaseDuration: time.Hour})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			if ok {
				claims = append(claims, claim)
			}
		}(i)
	}
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("claim errors: %v", errs)
	}
	if len(claims) != 1 {
		t.Fatalf("claims = %d, want 1", len(claims))
	}
	if claims[0].AttemptID == 0 || claims[0].Job.LeaseOwner == "" {
		t.Fatalf("claim missing lease/attempt: %+v", claims[0])
	}
}

func TestHostStaleAttemptCompletionDoesNotOverwriteCurrentClaim(t *testing.T) {
	s := openHost(t)
	ctx := context.Background()
	repo := mustHostRepo(t, s)
	session := mustHostSession(t, s, repo.ID, 13, "open")
	job, _, err := s.EnqueueHostJob(ctx, hostJobInput(repo.ID, session.ID, 13, uniqueName(t, "head")))
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	now := time.Now().UTC()
	first, ok, err := s.ClaimHostJob(ctx, store.HostJobClaimInput{WorkerID: "worker-1", Now: now, LeaseDuration: time.Second})
	if err != nil || !ok {
		t.Fatalf("first claim ok=%v err=%v", ok, err)
	}
	second, ok, err := s.ClaimHostJob(ctx, store.HostJobClaimInput{WorkerID: "worker-2", Now: now.Add(2 * time.Second), LeaseDuration: time.Hour})
	if err != nil || !ok {
		t.Fatalf("second claim ok=%v err=%v", ok, err)
	}
	if err := s.CompleteHostJob(ctx, store.HostJobCompleteInput{JobID: job.ID, AttemptID: first.AttemptID, Status: "done", Now: now.Add(3 * time.Second)}); err != nil {
		t.Fatalf("stale complete: %v", err)
	}
	var status, owner, firstAttemptStatus string
	var attempts int
	if err := s.db.QueryRowContext(ctx, `SELECT status, lease_owner, attempts FROM host_jobs WHERE id=$1`, job.ID).Scan(&status, &owner, &attempts); err != nil {
		t.Fatalf("query job: %v", err)
	}
	if status != "running" || owner != "worker-2" || attempts != 2 {
		t.Fatalf("stale completion overwrote current claim: status=%s owner=%s attempts=%d", status, owner, attempts)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM host_job_attempts WHERE id=$1`, first.AttemptID).Scan(&firstAttemptStatus); err != nil {
		t.Fatalf("query first attempt: %v", err)
	}
	if firstAttemptStatus != "canceled" {
		t.Fatalf("first attempt status = %s, want canceled", firstAttemptStatus)
	}
	if err := s.CompleteHostJob(ctx, store.HostJobCompleteInput{JobID: job.ID, AttemptID: second.AttemptID, Status: "done", Now: now.Add(4 * time.Second)}); err != nil {
		t.Fatalf("current complete: %v", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM host_jobs WHERE id=$1`, job.ID).Scan(&status); err != nil {
		t.Fatalf("query completed job: %v", err)
	}
	if status != "done" {
		t.Fatalf("current completion did not finish job: %s", status)
	}
}

func TestHostPollCursorRoundTrip(t *testing.T) {
	s := openHost(t)
	ctx := context.Background()
	repo := mustHostRepo(t, s)
	now := time.Now().UTC()
	if err := s.UpsertHostPollCursor(ctx, store.HostPollCursorInput{RepoID: repo.ID, Source: "pulls", Cursor: "abc", LastPolledAt: now}); err != nil {
		t.Fatalf("upsert cursor: %v", err)
	}
	cur, ok, err := s.GetHostPollCursor(ctx, repo.ID, "pulls")
	if err != nil {
		t.Fatalf("get cursor: %v", err)
	}
	if !ok || cur.Cursor != "abc" || cur.LastPolledAt.IsZero() {
		t.Fatalf("cursor = %+v ok=%v", cur, ok)
	}
}

func TestHostPruneKeepsActiveSessions(t *testing.T) {
	s := openHost(t)
	ctx := context.Background()
	repo := mustHostRepo(t, s)
	open := mustHostSession(t, s, repo.ID, 21, "open")
	closed := mustHostSession(t, s, repo.ID, 22, "closed")
	past := time.Now().Add(-48 * time.Hour).UTC()
	if _, err := s.db.ExecContext(ctx, `UPDATE host_pr_sessions SET updated_at=$1 WHERE id IN ($2,$3)`, past, open.ID, closed.ID); err != nil {
		t.Fatalf("backdate sessions: %v", err)
	}
	res, err := s.PruneHost(ctx, store.HostPrunePolicy{ClosedSessionsBefore: time.Now().UTC()})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if res.Sessions != 1 {
		t.Fatalf("pruned sessions = %d, want 1", res.Sessions)
	}
	var openCount, closedCount int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM host_pr_sessions WHERE id=$1`, open.ID).Scan(&openCount); err != nil {
		t.Fatalf("count open: %v", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM host_pr_sessions WHERE id=$1`, closed.ID).Scan(&closedCount); err != nil {
		t.Fatalf("count closed: %v", err)
	}
	if openCount != 1 || closedCount != 0 {
		t.Fatalf("open=%d closed=%d, want open kept and closed pruned", openCount, closedCount)
	}
}

func openHost(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("MIUCR_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("MIUCR_TEST_PG_DSN not set")
	}
	s, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.db.ExecContext(context.Background(), `TRUNCATE host_job_attempts, host_jobs, host_workspaces, host_poll_cursors, host_pr_sessions, host_repos RESTART IDENTITY CASCADE`); err != nil {
		_ = s.Close()
		t.Fatalf("truncate host tables: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustHostRepo(t *testing.T, s *Store) store.HostRepo {
	t.Helper()
	repo, err := s.ReconcileHostRepo(context.Background(), hostRepoInput(t))
	if err != nil {
		t.Fatalf("reconcile repo: %v", err)
	}
	return repo
}

func mustHostSession(t *testing.T, s *Store, repoID, number int64, state string) store.HostPRSession {
	t.Helper()
	session, err := s.UpsertHostPRSession(context.Background(), store.HostPRSessionInput{
		RepoID:  repoID,
		Number:  number,
		State:   state,
		HeadSHA: uniqueName(t, "head"),
		BaseSHA: uniqueName(t, "base"),
		Branch:  uniqueName(t, "branch"),
		Title:   "Synthetic PR",
	})
	if err != nil {
		t.Fatalf("upsert session: %v", err)
	}
	return session
}

func hostRepoInput(t *testing.T) store.HostRepoInput {
	t.Helper()
	name := uniqueName(t, "repo")
	return store.HostRepoInput{
		Name:          name,
		Owner:         "host-test",
		Repo:          name,
		Slug:          "host-test/" + name,
		GitURL:        "https://github.com/host-test/" + name + ".git",
		DefaultBranch: "main",
		GithubAccount: "test",
		Enabled:       true,
		Poll:          true,
		ConfigHash:    "hash",
	}
}

func hostJobInput(repoID, sessionID, number int64, head string) store.HostJobInput {
	return store.HostJobInput{
		RepoID:     repoID,
		SessionID:  sessionID,
		Number:     number,
		HeadSHA:    head,
		BaseSHA:    "base",
		PolicyHash: "policy",
		PromptHash: "prompt",
		RulesHash:  "rules",
	}
}

func uniqueName(t *testing.T, prefix string) string {
	t.Helper()
	repl := strings.NewReplacer("/", "-", " ", "-", "_", "-", ".", "-")
	return prefix + "-" + repl.Replace(strings.ToLower(t.Name())) + "-" + time.Now().UTC().Format("20060102150405.000000000")
}
