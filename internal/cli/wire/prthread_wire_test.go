package wire

import (
	stdctx "context"
	"path/filepath"
	"testing"

	"github.com/vanducng/miu-cr/internal/cli"
	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
	mgithub "github.com/vanducng/miu-cr/internal/github"
	"github.com/vanducng/miu-cr/internal/store"
	"github.com/vanducng/miu-cr/internal/store/sqlite"
)

// tempStore opens a fresh on-disk sqlite PR-thread store under t.TempDir.
func tempStore(t *testing.T) *sqlite.Store {
	t.Helper()
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func findingB() engine.Finding {
	// Anchored to the added line (new-side line 4 in setupRepo's head commit).
	return engine.Finding{File: "foo.go", Line: 4, Severity: "high", Category: "bug", Rationale: "boom", QuotedCode: "func B() {}"}
}

func wireFake(t *testing.T) (*fakeGitHub, mgithub.Client) {
	t.Helper()
	fake := &fakeGitHub{}
	restore := newGitHubClient
	newGitHubClient = func(string) mgithub.Client { return fake }
	t.Cleanup(func() { newGitHubClient = restore })
	return fake, newGitHubClient("")
}

// TestStoreCrossPush: a finding posted in run A is recorded posted; a re-anchored
// run B (same fingerprint) is NOT re-posted — dedupe holds through the store.
func TestStoreCrossPush(t *testing.T) {
	runner := gitcmd.New()
	dir, base, head := setupRepo(t, runner)
	fake, client := wireFake(t)
	st := tempStore(t)
	info := &mgithub.PRInfo{Owner: "o", Repo: "r", Number: 7, HeadSHA: head, BaseSHA: base, BaseBranch: "main"}
	res := engine.ReviewResult{Findings: []engine.Finding{findingB()}, Stats: map[string]any{"truncation_level": "full", "files_reviewed": float64(1)}}

	prA := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, res, prA, cli.PRReviewRequest{Gate: "high"}, st.PRThread()); err != nil {
		t.Fatalf("run A: %v", err)
	}
	if prA.PostedInline != 1 {
		t.Fatalf("run A: want 1 inline, got %d", prA.PostedInline)
	}
	got, _ := st.PRThread().ListFindings(stdctx.Background(), store.PRKey{Owner: "o", Repo: "r", Number: 7})
	if len(got) != 1 || got[0].Status != "posted" {
		t.Fatalf("run A must record one posted finding, got %+v", got)
	}

	prB := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, res, prB, cli.PRReviewRequest{Gate: "high"}, st.PRThread()); err != nil {
		t.Fatalf("run B: %v", err)
	}
	if prB.PostedInline != 0 {
		t.Fatalf("run B: re-anchored finding must not re-post, got %d", prB.PostedInline)
	}
	if fake.createReviewN != 1 {
		t.Fatalf("run B must not create a second review, createReviewN=%d", fake.createReviewN)
	}
}

// TestStoreResolution: a finding posted in run A then absent in run B (file still
// in diff) is marked resolved; on the immediate next run C it is NOT re-raised
// (because the run-C findings still omit it).
func TestStoreResolution(t *testing.T) {
	runner := gitcmd.New()
	dir, base, head := setupRepo(t, runner)
	_, client := wireFake(t)
	st := tempStore(t)
	key := store.PRKey{Owner: "o", Repo: "r", Number: 7}
	info := &mgithub.PRInfo{Owner: "o", Repo: "r", Number: 7, HeadSHA: head, BaseSHA: base, BaseBranch: "main"}
	withF := engine.ReviewResult{Findings: []engine.Finding{findingB()}, Stats: map[string]any{"truncation_level": "full", "files_reviewed": float64(1)}}
	noF := engine.ReviewResult{Findings: nil, Stats: map[string]any{"truncation_level": "full", "files_reviewed": float64(1)}}

	prA := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, withF, prA, cli.PRReviewRequest{Gate: "high"}, st.PRThread()); err != nil {
		t.Fatalf("run A: %v", err)
	}

	prB := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, noF, prB, cli.PRReviewRequest{Gate: "high"}, st.PRThread()); err != nil {
		t.Fatalf("run B: %v", err)
	}
	got, _ := st.PRThread().ListFindings(stdctx.Background(), key)
	if len(got) != 1 || got[0].Status != "resolved" {
		t.Fatalf("run B must resolve the absent finding (file in diff), got %+v", got)
	}

	prC := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, noF, prC, cli.PRReviewRequest{Gate: "high"}, st.PRThread()); err != nil {
		t.Fatalf("run C: %v", err)
	}
	if prC.PostedInline != 0 {
		t.Fatalf("run C must not re-raise a still-fixed finding, got %d", prC.PostedInline)
	}
	got, _ = st.PRThread().ListFindings(stdctx.Background(), key)
	if got[0].Status != "resolved" {
		t.Fatalf("still-fixed finding must stay resolved, got %q", got[0].Status)
	}
}

// TestStoreReopen proves the set-DIFFERENCE: run A posts F (marker persists),
// run B omits F → resolved, run C re-emits F → re-RAISED even though the run-A
// marker is STILL in ExistingFingerprints (a union could never re-raise it).
func TestStoreReopen(t *testing.T) {
	runner := gitcmd.New()
	dir, base, head := setupRepo(t, runner)
	fake, client := wireFake(t)
	st := tempStore(t)
	key := store.PRKey{Owner: "o", Repo: "r", Number: 7}
	info := &mgithub.PRInfo{Owner: "o", Repo: "r", Number: 7, HeadSHA: head, BaseSHA: base, BaseBranch: "main"}
	withF := engine.ReviewResult{Findings: []engine.Finding{findingB()}, Stats: map[string]any{"truncation_level": "full", "files_reviewed": float64(1)}}
	noF := engine.ReviewResult{Findings: nil, Stats: map[string]any{"truncation_level": "full", "files_reviewed": float64(1)}}

	// A: post F.
	prA := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, withF, prA, cli.PRReviewRequest{Gate: "high"}, st.PRThread()); err != nil {
		t.Fatalf("run A: %v", err)
	}
	if prA.PostedInline != 1 {
		t.Fatalf("run A must post F, got %d", prA.PostedInline)
	}
	fpF := mgithub.Fingerprint(findingB())
	// The run-A marker must still be live so the union can't help us.
	existing, _ := mgithub.ExistingFingerprints(stdctx.Background(), client, info)
	if !existing[fpF] {
		t.Fatalf("run-A marker for F must persist in ExistingFingerprints")
	}

	// B: omit F → resolved.
	prB := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, noF, prB, cli.PRReviewRequest{Gate: "high"}, st.PRThread()); err != nil {
		t.Fatalf("run B: %v", err)
	}
	got, _ := st.PRThread().ListFindings(stdctx.Background(), key)
	if len(got) != 1 || got[0].Status != "resolved" {
		t.Fatalf("run B must resolve F, got %+v", got)
	}

	// C: re-emit F → must RE-RAISE (set-difference removes the lingering marker).
	postedBefore := fake.createReviewN
	prC := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, withF, prC, cli.PRReviewRequest{Gate: "high"}, st.PRThread()); err != nil {
		t.Fatalf("run C: %v", err)
	}
	if prC.PostedInline != 1 {
		t.Fatalf("REOPEN: run C must re-raise F despite the lingering marker, got %d", prC.PostedInline)
	}
	if fake.createReviewN != postedBefore+1 {
		t.Fatalf("REOPEN: run C must create a fresh review, createReviewN=%d", fake.createReviewN)
	}
	got, _ = st.PRThread().ListFindings(stdctx.Background(), key)
	if got[0].Status != "posted" {
		t.Fatalf("REOPEN: F must flip back to posted, got %q", got[0].Status)
	}
}

// TestPostedFindingsOnlySubmitted: the empty-guard path (no inline + empty summary
// + COMMENT) submits nothing, so nothing is recorded as posted.
func TestPostedFindingsOnlySubmitted(t *testing.T) {
	runner := gitcmd.New()
	dir, base, head := setupRepo(t, runner)
	_, client := wireFake(t)
	st := tempStore(t)
	info := &mgithub.PRInfo{Owner: "o", Repo: "r", Number: 7, HeadSHA: head, BaseSHA: base, BaseBranch: "main"}

	// No findings → RenderSummary still produces a body, so to truly hit the
	// empty-guard we assert directly on PostReview with empty summary + no findings.
	diffs, err := mgithub.DiffsForPR(stdctx.Background(), runner, dir, base, head)
	if err != nil {
		t.Fatalf("diffs: %v", err)
	}
	pr, err := mgithub.PostReview(stdctx.Background(), client, info, nil, diffs, "", nil, mgithub.PostReviewOptions{Gate: "high"})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if len(pr.PostedFindings) != 0 {
		t.Fatalf("empty-guard must record no posted findings, got %+v", pr.PostedFindings)
	}
	// And a store-backed upsert of an empty set must leave the table empty.
	if err := st.PRThread().UpsertPosted(stdctx.Background(), store.PRKey{Owner: "o", Repo: "r", Number: 7}, nil); err != nil {
		t.Fatalf("upsert empty: %v", err)
	}
	got, _ := st.PRThread().ListFindings(stdctx.Background(), store.PRKey{Owner: "o", Repo: "r", Number: 7})
	if len(got) != 0 {
		t.Fatalf("empty upsert must record nothing, got %+v", got)
	}
}

// TestPublishNoStoreUnchanged asserts the nil-store path is byte-for-byte the M2
// baseline: same fake-gh call order and the same create/edit counts as
// TestPublishReviewWireFlow's first+second run.
func TestPublishNoStoreUnchanged(t *testing.T) {
	runner := gitcmd.New()
	dir, base, head := setupRepo(t, runner)
	fake, client := wireFake(t)
	info := &mgithub.PRInfo{Owner: "o", Repo: "r", Number: 7, HeadSHA: head, BaseSHA: base, BaseBranch: "main"}
	res := engine.ReviewResult{Findings: []engine.Finding{findingB()}, Stats: map[string]any{"truncation_level": "full", "files_reviewed": float64(1)}}

	pr := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, res, pr, cli.PRReviewRequest{Gate: "high"}, nil); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	wantOrder := []string{"list_review", "create_review", "list_issue", "create_issue"}
	if !equalStr(fake.order, wantOrder) {
		t.Fatalf("run 1 call order = %v, want %v", fake.order, wantOrder)
	}

	fake.order = nil
	pr2 := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, res, pr2, cli.PRReviewRequest{Gate: "high"}, nil); err != nil {
		t.Fatalf("run 2: %v", err)
	}
	// Re-run: dedupe skips inline, summary edited; NO extra review.
	wantOrder2 := []string{"list_review", "list_issue", "edit_issue"}
	if !equalStr(fake.order, wantOrder2) {
		t.Fatalf("run 2 call order = %v, want %v", fake.order, wantOrder2)
	}
	if fake.createReviewN != 1 || fake.createIssueN != 1 || fake.editN != 1 {
		t.Fatalf("no-store counts: createReview=%d createIssue=%d edit=%d", fake.createReviewN, fake.createIssueN, fake.editN)
	}
}

func equalStr(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
