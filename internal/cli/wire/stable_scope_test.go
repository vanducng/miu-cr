package wire

import (
	stdctx "context"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
	mgithub "github.com/vanducng/miu-cr/internal/github"
	"github.com/vanducng/miu-cr/internal/store"
)

func TestApplyStableScope(t *testing.T) {
	runner := gitcmd.New()
	dir, base, head := setupRepo(t, runner)
	ctx := stdctx.Background()
	prior := []mgithub.LedgerEntry{{FP: "aa", Path: "other.go", Status: "open"}}
	context := engine.Finding{File: "foo.go", QuotedCode: "func A() {}", Title: "context", Severity: "low", Category: "bug"}
	added := engine.Finding{File: "foo.go", QuotedCode: "func B() {}", Title: "added", Severity: "high", Category: "bug"}

	untrusted := &mgithub.PRInfo{
		Owner: "acme", Repo: "app", Number: 1, HeadSHA: head,
		PriorPublishedHeadSHA: base, PriorLedger: prior,
	}
	stats := map[string]any{}
	got, stable := applyStableScope(ctx, runner, dir, untrusted, []engine.Finding{context, added}, &stats)
	if stable != nil || len(got) != 2 {
		t.Fatalf("untrusted ledger dropped findings: kept %d stable %#v", len(got), stable)
	}
	if _, ok := stats["stable_scope_dropped"]; ok {
		t.Fatal("untrusted ledger recorded a drop")
	}

	same := &mgithub.PRInfo{
		Owner: "acme", Repo: "app", Number: 1, HeadSHA: head, LedgerTrusted: true,
		PriorPublishedHeadSHA: head, PriorLedger: prior,
	}
	got, stable = applyStableScope(ctx, runner, dir, same, []engine.Finding{context}, &stats)
	if stable != nil || len(got) != 1 {
		t.Fatalf("same head filtered findings: kept %d", len(got))
	}

	follow := &mgithub.PRInfo{
		Owner: "acme", Repo: "app", Number: 1, HeadSHA: head, LedgerTrusted: true,
		PriorPublishedHeadSHA: base, PriorLedger: prior,
	}
	stats = map[string]any{}
	got, stable = applyStableScope(ctx, runner, dir, follow, []engine.Finding{context, added}, &stats)
	if stable == nil || stats["stable_scope_dropped"] != 1 || len(got) != 1 || got[0].Title != "added" {
		t.Fatalf("follow-up kept %+v dropped %v stable %#v", got, stats["stable_scope_dropped"], stable)
	}

	shortSHA := &mgithub.PRInfo{
		Owner: "acme", Repo: "app", Number: 1, HeadSHA: head, LedgerTrusted: true,
		PriorSummaryHeadSHA: head[:7], PriorLedger: prior,
	}
	stats = map[string]any{}
	got, stable = applyStableScope(ctx, runner, dir, shortSHA, []engine.Finding{context, added}, &stats)
	if stable != nil || len(got) != 2 {
		t.Fatalf("short summary SHA must match head, kept %d stable %#v", len(got), stable)
	}

	if _, err := runner.Output(ctx, dir, "commit", "--quiet", "--allow-empty", "-m", "empty"); err != nil {
		t.Fatal(err)
	}
	empty, err := runner.HeadSHA(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	clean := &mgithub.PRInfo{
		Owner: "acme", Repo: "app", Number: 1, HeadSHA: empty, LedgerTrusted: true,
		PriorPublishedHeadSHA: head, PriorLedger: prior,
	}
	stats = map[string]any{}
	got, stable = applyStableScope(ctx, runner, dir, clean, []engine.Finding{context, added}, &stats)
	if stable == nil || len(got) != 0 || stats["stable_scope_dropped"] != 2 {
		t.Fatalf("empty delta kept %+v dropped %v stable %#v", got, stats["stable_scope_dropped"], stable)
	}

	broken := &mgithub.PRInfo{
		Owner: "acme", Repo: "app", Number: 1, HeadSHA: "deadbeef", LedgerTrusted: true,
		PriorPublishedHeadSHA: base, PriorLedger: prior,
	}
	stats = map[string]any{}
	got, stable = applyStableScope(ctx, runner, t.TempDir(), broken, []engine.Finding{context}, &stats)
	if stable != nil || len(got) != 1 {
		t.Fatalf("delta error must fail open, kept %d stable %#v", len(got), stable)
	}
	if _, ok := stats["stable_scope_dropped"]; ok {
		t.Fatal("delta error recorded a drop")
	}
}

func TestLedgerDiffNeeded(t *testing.T) {
	info := &mgithub.PRInfo{LedgerTrusted: true, PriorLedger: []mgithub.LedgerEntry{{FP: "aa"}}}
	if !ledgerDiffNeeded(info, false) {
		t.Fatal("a trusted ledger on a dry run needs the pull request diff")
	}
	if ledgerDiffNeeded(info, true) || ledgerDiffNeeded(&mgithub.PRInfo{PriorLedger: info.PriorLedger}, false) {
		t.Fatal("captured diffs and an untrusted ledger do not need another fetch")
	}
}

func TestSyncFilteredReviewReplacesFindings(t *testing.T) {
	ctx := stdctx.Background()
	st := tempHistoryStore(t)
	id, err := st.SaveReview(ctx, store.ReviewRecord{
		Findings: []engine.Finding{{File: "a.go", Title: "drop me"}},
		Stats:    map[string]any{"files_changed": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	kept := []engine.Finding{{File: "b.go", Title: "kept"}}
	stats := map[string]any{"stable_scope_dropped": 1}
	syncFilteredReview(ctx, st, id, kept, stats)
	rec, err := st.GetReview(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Findings) != 1 || rec.Findings[0].Title != "kept" {
		t.Fatalf("findings = %+v", rec.Findings)
	}
	n, ok := rec.Stats["stable_scope_dropped"].(float64)
	if !ok || n != 1 {
		t.Fatalf("stats = %#v", rec.Stats)
	}
	syncFilteredReview(ctx, nil, id, nil, nil)
	syncFilteredReview(ctx, st, "", nil, nil)
}
