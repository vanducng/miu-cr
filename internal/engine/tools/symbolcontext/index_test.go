package symbolcontext

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

// The index must snapshot the reviewed revision on first use and never rescan:
// a symbol staged AFTER the build stays invisible through the index while a
// fresh per-call scan (nil index) sees it.
func TestIndexBuildsOnceAndSnapshotsRevision(t *testing.T) {
	repo := initRepo(t)
	writeRepoFile(t, repo, "a.go", "package a\n\nfunc FirstSymbol() {}\n")
	runGit(t, repo, "add", "-A")

	ix := NewIndex(symbolConfig(), Context{RepoDir: repo, Runner: gitcmd.New()})
	tc := Context{RepoDir: repo, Runner: gitcmd.New(), Index: ix}

	out, isErr := Run(context.Background(), symbolConfig(), tc, 0, []byte(`{"relation":"definition","symbol":"FirstSymbol"}`))
	if isErr || !strings.Contains(out, "a.go:3") {
		t.Fatalf("indexed definition = %q isErr=%v", out, isErr)
	}

	writeRepoFile(t, repo, "b.go", "package a\n\nfunc SecondSymbol() {}\n")
	runGit(t, repo, "add", "-A")

	out, isErr = Run(context.Background(), symbolConfig(), tc, 0, []byte(`{"relation":"definition","symbol":"SecondSymbol"}`))
	if isErr || !strings.Contains(out, NoSymbolsFoundMarker) {
		t.Fatalf("index rescanned after build (SecondSymbol leaked in): %q isErr=%v", out, isErr)
	}

	fresh, isErr := Run(context.Background(), symbolConfig(), Context{RepoDir: repo, Runner: gitcmd.New()}, 0, []byte(`{"relation":"definition","symbol":"SecondSymbol"}`))
	if isErr || !strings.Contains(fresh, "b.go:3") {
		t.Fatalf("nil index must keep per-call scanning: %q isErr=%v", fresh, isErr)
	}
}

func indexEqualityRepo(t *testing.T) (string, string) {
	t.Helper()
	repo := initRepo(t)
	writeRepoFile(t, repo, "src/user.ts", "export function loadUser() {\n  return 1\n}\n")
	writeRepoFile(t, repo, "src/user2.ts", "export function loadUser() {\n  return 2\n}\n")
	writeRepoFile(t, repo, "app/service.py", "def build_service():\n    return 1\n\nclass ServiceKit:\n    pass\n")
	writeRepoFile(t, repo, "pkg/engine.go", "package pkg\n\ntype Engine struct {}\n\nfunc (e *Engine) Review() {\n\tloadUser()\n}\n")
	writeRepoFile(t, repo, "models/orders.sql", `select * from {{ ref("customers") }} join {{ source("stripe", "charges") }} on true`)
	writeRepoFile(t, repo, "models/schema_named.sql", "create table analytics.daily_orders as select 1\n")
	sha := commitRepo(t, repo, "corpus")
	return repo, sha
}

var indexEqualityCalls = []string{
	`{"relation":"definition","symbol":"loadUser"}`,
	`{"relation":"definition","symbol":"LOADUSER"}`,
	`{"relation":"definition","symbol":"loadUser","limit":1}`,
	`{"relation":"definition","symbol":"loadUser","file":"src/user.ts"}`,
	`{"relation":"definition","symbol":"daily_orders"}`,
	`{"relation":"definition","symbol":"analytics.daily_orders"}`,
	`{"relation":"definition","symbol":"Engine"}`,
	`{"relation":"implementations","symbol":"ServiceKit"}`,
	`{"relation":"document_symbols","file":"app/service.py"}`,
	`{"relation":"document_symbols","file":"models/orders.sql"}`,
	`{"relation":"outgoing_calls","symbol":"Review"}`,
	`{"relation":"dependencies"}`,
	`{"relation":"dependencies","symbol":"customers"}`,
	`{"relation":"dependencies","file":"models/orders.sql"}`,
}

// Every index-served relation must produce byte-identical output to the
// per-call scan it replaces.
func TestIndexMatchesPerCallScan(t *testing.T) {
	repo, sha := indexEqualityRepo(t)
	ix := NewIndex(symbolConfig(), Context{RepoDir: repo, Rev: sha, Runner: gitcmd.New()})
	withIndex := Context{RepoDir: repo, Rev: sha, Runner: gitcmd.New(), Index: ix}
	without := Context{RepoDir: repo, Rev: sha, Runner: gitcmd.New()}

	for _, call := range indexEqualityCalls {
		got, gotErr := Run(context.Background(), symbolConfig(), withIndex, 0, []byte(call))
		want, wantErr := Run(context.Background(), symbolConfig(), without, 0, []byte(call))
		if got != want || gotErr != wantErr {
			t.Errorf("index output diverged for %s:\nwith index: %q (err=%v)\nwithout:    %q (err=%v)", call, got, gotErr, want, wantErr)
		}
	}
}

// Parallel tool calls share one index; the lazy build must be safe and every
// call must see the same results as a per-call scan.
func TestIndexConcurrentToolCalls(t *testing.T) {
	repo, sha := indexEqualityRepo(t)
	ix := NewIndex(symbolConfig(), Context{RepoDir: repo, Rev: sha, Runner: gitcmd.New()})
	withIndex := Context{RepoDir: repo, Rev: sha, Runner: gitcmd.New(), Index: ix}
	without := Context{RepoDir: repo, Rev: sha, Runner: gitcmd.New()}

	want := make([]string, len(indexEqualityCalls))
	for i, call := range indexEqualityCalls {
		want[i], _ = Run(context.Background(), symbolConfig(), without, 0, []byte(call))
	}

	got := make([]string, len(indexEqualityCalls))
	var wg sync.WaitGroup
	for i, call := range indexEqualityCalls {
		wg.Add(1)
		go func(i int, call string) {
			defer wg.Done()
			got[i], _ = Run(context.Background(), symbolConfig(), withIndex, 0, []byte(call))
		}(i, call)
	}
	wg.Wait()
	for i := range indexEqualityCalls {
		if got[i] != want[i] {
			t.Errorf("concurrent indexed call %s diverged:\ngot:  %q\nwant: %q", indexEqualityCalls[i], got[i], want[i])
		}
	}
}

// A cancelled build must not poison the index with empty results; it reports
// not-ready and callers keep per-call scanning.
func TestIndexCancelledBuildStaysUnready(t *testing.T) {
	repo, sha := indexEqualityRepo(t)
	ix := NewIndex(symbolConfig(), Context{RepoDir: repo, Rev: sha, Runner: gitcmd.New()})
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if ix.ensure(cancelled) {
		t.Fatal("build under a cancelled context must not report ready")
	}
	if defs := ix.Lookup(context.Background(), "loadUser"); defs != nil {
		t.Fatalf("failed index must serve nothing, got %v", defs)
	}
	tc := Context{RepoDir: repo, Rev: sha, Runner: gitcmd.New(), Index: ix}
	out, isErr := Run(context.Background(), symbolConfig(), tc, 0, []byte(`{"relation":"definition","symbol":"loadUser"}`))
	if isErr || !strings.Contains(out, "src/user.ts:1") {
		t.Fatalf("failed index must fall back to per-call scan: %q isErr=%v", out, isErr)
	}
}

func TestNameKeysDotSuffixes(t *testing.T) {
	got := nameKeys("Analytics.Daily_Orders")
	want := []string{"analytics.daily_orders", "daily_orders"}
	if len(got) != len(want) {
		t.Fatalf("nameKeys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("nameKeys = %v, want %v", got, want)
		}
	}
}
