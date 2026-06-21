package sqlite

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/vanducng/miu-cr/internal/store"
)

func samplePRKey() store.PRKey {
	return store.PRKey{Owner: "acme", Repo: "widgets", Number: 7}
}

func TestPRUpsertListRoundTrip(t *testing.T) {
	s := tempStore(t)
	ctx := context.Background()
	key := samplePRKey()

	in := []store.PRFinding{
		{Fingerprint: "fp1", Path: "a.go", Status: "posted"},
		{Fingerprint: "fp2", Path: "b.go", Status: "posted"},
	}
	if err := s.UpsertPosted(ctx, key, in); err != nil {
		t.Fatalf("UpsertPosted: %v", err)
	}

	got, err := s.ListFindings(ctx, key)
	if err != nil {
		t.Fatalf("ListFindings: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 findings, got %d", len(got))
	}
	sort.Slice(got, func(i, j int) bool { return got[i].Fingerprint < got[j].Fingerprint })
	if got[0].Fingerprint != "fp1" || got[0].Path != "a.go" || got[0].Status != "posted" {
		t.Fatalf("fp1 mismatch: %+v", got[0])
	}
	if got[1].Fingerprint != "fp2" || got[1].Status != "posted" {
		t.Fatalf("fp2 mismatch: %+v", got[1])
	}
}

func TestPRUpsertConflictKeepsFirstSeenAdvancesLastSeen(t *testing.T) {
	s := tempStore(t)
	ctx := context.Background()
	key := samplePRKey()

	if err := s.UpsertPosted(ctx, key, []store.PRFinding{{Fingerprint: "fp1", Path: "a.go"}}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	var first1, last1 string
	row := s.db.QueryRowContext(ctx,
		`SELECT first_seen, last_seen FROM pr_findings WHERE owner=? AND repo=? AND number=? AND fingerprint=?`,
		key.Owner, key.Repo, key.Number, "fp1")
	if err := row.Scan(&first1, &last1); err != nil {
		t.Fatalf("scan after first: %v", err)
	}

	if err := s.UpsertPosted(ctx, key, []store.PRFinding{{Fingerprint: "fp1", Path: "a.go"}}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	var first2, last2 string
	row = s.db.QueryRowContext(ctx,
		`SELECT first_seen, last_seen FROM pr_findings WHERE owner=? AND repo=? AND number=? AND fingerprint=?`,
		key.Owner, key.Repo, key.Number, "fp1")
	if err := row.Scan(&first2, &last2); err != nil {
		t.Fatalf("scan after second: %v", err)
	}

	if first1 != first2 {
		t.Errorf("first_seen changed on conflict: %q -> %q", first1, first2)
	}
	if last2 < last1 {
		t.Errorf("last_seen regressed: %q -> %q", last1, last2)
	}
}

func TestPRMarkResolvedFlipsStatus(t *testing.T) {
	s := tempStore(t)
	ctx := context.Background()
	key := samplePRKey()

	if err := s.UpsertPosted(ctx, key, []store.PRFinding{
		{Fingerprint: "fp1", Path: "a.go"},
		{Fingerprint: "fp2", Path: "b.go"},
	}); err != nil {
		t.Fatalf("UpsertPosted: %v", err)
	}
	if err := s.MarkResolved(ctx, key, []string{"fp1"}); err != nil {
		t.Fatalf("MarkResolved: %v", err)
	}

	got, err := s.ListFindings(ctx, key)
	if err != nil {
		t.Fatalf("ListFindings: %v", err)
	}
	byFP := map[string]string{}
	for _, f := range got {
		byFP[f.Fingerprint] = f.Status
	}
	if byFP["fp1"] != "resolved" {
		t.Errorf("fp1 status = %q, want resolved", byFP["fp1"])
	}
	if byFP["fp2"] != "posted" {
		t.Errorf("fp2 status = %q, want posted", byFP["fp2"])
	}
}

// Reopen: an upsert of a previously-resolved fingerprint flips it back to posted.
func TestPRUpsertReopensResolved(t *testing.T) {
	s := tempStore(t)
	ctx := context.Background()
	key := samplePRKey()

	if err := s.UpsertPosted(ctx, key, []store.PRFinding{{Fingerprint: "fp1", Path: "a.go"}}); err != nil {
		t.Fatalf("UpsertPosted: %v", err)
	}
	if err := s.MarkResolved(ctx, key, []string{"fp1"}); err != nil {
		t.Fatalf("MarkResolved: %v", err)
	}
	if err := s.UpsertPosted(ctx, key, []store.PRFinding{{Fingerprint: "fp1", Path: "a.go"}}); err != nil {
		t.Fatalf("reopen upsert: %v", err)
	}
	got, err := s.ListFindings(ctx, key)
	if err != nil {
		t.Fatalf("ListFindings: %v", err)
	}
	if len(got) != 1 || got[0].Status != "posted" {
		t.Fatalf("want single posted finding after reopen, got %+v", got)
	}
}

func TestPRListFindingsStrictKeyFilter(t *testing.T) {
	s := tempStore(t)
	ctx := context.Background()
	a := store.PRKey{Owner: "acme", Repo: "widgets", Number: 7}
	b := store.PRKey{Owner: "acme", Repo: "widgets", Number: 8}
	c := store.PRKey{Owner: "other", Repo: "widgets", Number: 7}

	if err := s.UpsertPosted(ctx, a, []store.PRFinding{{Fingerprint: "fpA", Path: "a.go"}}); err != nil {
		t.Fatalf("upsert a: %v", err)
	}
	if err := s.UpsertPosted(ctx, b, []store.PRFinding{{Fingerprint: "fpB", Path: "b.go"}}); err != nil {
		t.Fatalf("upsert b: %v", err)
	}
	if err := s.UpsertPosted(ctx, c, []store.PRFinding{{Fingerprint: "fpC", Path: "c.go"}}); err != nil {
		t.Fatalf("upsert c: %v", err)
	}

	got, err := s.ListFindings(ctx, a)
	if err != nil {
		t.Fatalf("ListFindings: %v", err)
	}
	if len(got) != 1 || got[0].Fingerprint != "fpA" {
		t.Fatalf("strict filter failed: %+v", got)
	}
}

func TestPRConcurrentUpserts(t *testing.T) {
	s := tempStore(t)
	ctx := context.Background()
	key := samplePRKey()

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			fp := "fp" + string(rune('a'+n))
			if err := s.UpsertPosted(ctx, key, []store.PRFinding{{Fingerprint: fp, Path: "x.go"}}); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent upsert: %v", err)
	}

	got, err := s.ListFindings(ctx, key)
	if err != nil {
		t.Fatalf("ListFindings: %v", err)
	}
	if len(got) != 16 {
		t.Fatalf("want 16 findings, got %d", len(got))
	}
}

func TestPREmptyArgsNoop(t *testing.T) {
	s := tempStore(t)
	ctx := context.Background()
	key := samplePRKey()
	if err := s.UpsertPosted(ctx, key, nil); err != nil {
		t.Fatalf("empty UpsertPosted: %v", err)
	}
	if err := s.MarkResolved(ctx, key, nil); err != nil {
		t.Fatalf("empty MarkResolved: %v", err)
	}
}

// The status CHECK constraint rejects an out-of-band status.
func TestPRStatusCheckConstraint(t *testing.T) {
	s := tempStore(t)
	ctx := context.Background()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO pr_findings (owner, repo, number, fingerprint, path, status, first_seen, last_seen)
		 VALUES ('a','b',1,'fp','p.go','bogus','t','t')`)
	if err == nil {
		t.Fatal("expected CHECK constraint to reject status='bogus'")
	}
}

// *sqlite.Store satisfies store.PRThreadStore (and PRThread() returns it).
func TestPRThreadInterface(t *testing.T) {
	s := tempStore(t)
	var _ store.PRThreadStore = s
	if s.PRThread() == nil {
		t.Fatal("PRThread() returned nil")
	}
}

func TestNoCredentialColumnsPRFindings(t *testing.T) {
	s := tempStore(t)
	ctx := context.Background()

	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info(pr_findings)")
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		lower := strings.ToLower(name)
		for _, banned := range []string{"key", "token", "secret", "credential", "password", "auth"} {
			if strings.Contains(lower, banned) {
				t.Fatalf("credential-like column present: %s", name)
			}
		}
	}
}
