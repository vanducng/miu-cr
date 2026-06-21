package engine_test

import (
	stdctx "context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/anchor"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

func init() { engine.SetAnchorer(anchor.ResolveLineNumbers) }

// fakeAgent returns canned findings, ignoring the assembled context. No network,
// no API key. It records the rev it was invoked with for revision-source asserts.
type fakeAgent struct {
	findings    []engine.Finding
	gotRev      string
	gotRules    string
	gotSemantic string
}

func (f *fakeAgent) Review(_ stdctx.Context, rc engine.AgentContext) ([]engine.Finding, error) {
	f.gotRev = rc.Rev
	f.gotRules = rc.Rules
	f.gotSemantic = rc.SemanticContext
	out := make([]engine.Finding, len(f.findings))
	copy(out, f.findings)
	return out, nil
}

// fakeRetriever drives the engine's semantic seam without embed/DB/network.
type fakeRetriever struct {
	advisory string
	err      error
	gotCode  []string
	called   bool
}

func (r *fakeRetriever) Related(_ stdctx.Context, changedCode []string) (string, error) {
	r.called = true
	r.gotCode = changedCode
	return r.advisory, r.err
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	git(t, dir, "config", "user.email", "t@example.com")
	git(t, dir, "config", "user.name", "t")
	return dir
}

func TestReviewPipelineEndToEnd(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "app.go", "package app\n\nfunc Existing() int { return 1 }\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")
	writeFile(t, dir, "app.go", "package app\n\nfunc Existing() int { return 1 }\n\nfunc Risky() {\n\tpassword := \"hunter2\"\n\t_ = password\n}\n")
	git(t, dir, "add", "app.go")

	fa := &fakeAgent{findings: []engine.Finding{
		{File: "app.go", Severity: "high", Category: "security", Rationale: "hardcoded credential", QuotedCode: "password := \"hunter2\""},
		{File: "app.go", Severity: "critical", Category: "hallucination", Rationale: "not in diff", QuotedCode: "this_is_never_in_the_file_at_all()"},
	}}

	eng := engine.New(fa, gitcmd.New())
	res, err := eng.Review(stdctx.Background(), engine.Request{Mode: 0, RepoDir: dir, Gate: "high", Extensions: []string{"go"}})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if fa.gotRev != "" {
		t.Errorf("staged review must read the index (rev==\"\"), got %q", fa.gotRev)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("drift-reject failed: want 1 anchored finding, got %d: %+v", len(res.Findings), res.Findings)
	}
	got := res.Findings[0]
	if got.Line == 0 {
		t.Errorf("anchored finding must have a non-zero line: %+v", got)
	}
	if got.Category != "security" {
		t.Errorf("wrong finding survived drift-reject: %+v", got)
	}
	if res.Stats["max_severity"] != "high" {
		t.Errorf("max_severity: want high, got %v", res.Stats["max_severity"])
	}
	if d, _ := res.Stats["findings_dropped"].(float64); d != 1 {
		t.Errorf("findings_dropped: want 1, got %v", res.Stats["findings_dropped"])
	}
	if engine.GateFailed(res.Findings, "high") != true {
		t.Error("high finding must trip high gate")
	}
}

type failStore struct{ called bool }

func (s *failStore) SaveReview(stdctx.Context, engine.PersistRecord) (string, error) {
	s.called = true
	return "", errors.New("disk full")
}
func (s *failStore) GetReview(stdctx.Context, string) (engine.PersistRecord, error) {
	return engine.PersistRecord{}, errors.New("nope")
}

// Persistence is an optional side-effect: a save failure must not nullify the
// computed review.
func TestReviewSurvivesPersistFailure(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "app.go", "package app\n\nfunc Existing() int { return 1 }\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")
	writeFile(t, dir, "app.go", "package app\n\nfunc Existing() int { return 1 }\n\nfunc Risky() {\n\tpassword := \"hunter2\"\n\t_ = password\n}\n")
	git(t, dir, "add", "app.go")

	fa := &fakeAgent{findings: []engine.Finding{
		{File: "app.go", Severity: "high", Category: "security", Rationale: "hardcoded credential", QuotedCode: "password := \"hunter2\""},
	}}
	eng := engine.New(fa, gitcmd.New())
	fs := &failStore{}
	eng.Store = fs

	res, err := eng.Review(stdctx.Background(), engine.Request{Mode: 0, RepoDir: dir, Gate: "high", Extensions: []string{"go"}})
	if err != nil {
		t.Fatalf("Review must not error on persist failure: %v", err)
	}
	if !fs.called {
		t.Fatal("expected SaveReview to be attempted")
	}
	if len(res.Findings) != 1 {
		t.Fatalf("persist failure discarded findings: got %d", len(res.Findings))
	}
	if res.Stats["persist_error"] != "disk full" {
		t.Errorf("persist_error: want %q, got %v", "disk full", res.Stats["persist_error"])
	}
}

// semanticDir builds a staged change with one finding-worthy line, returning the
// repo dir + the canned finding set.
func semanticDir(t *testing.T) (string, []engine.Finding) {
	t.Helper()
	dir := initRepo(t)
	writeFile(t, dir, "app.go", "package app\n\nfunc Existing() int { return 1 }\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")
	writeFile(t, dir, "app.go", "package app\n\nfunc Existing() int { return 1 }\n\nfunc Risky() {\n\tpassword := \"hunter2\"\n\t_ = password\n}\n")
	git(t, dir, "add", "app.go")
	return dir, []engine.Finding{
		{File: "app.go", Severity: "high", Category: "security", Rationale: "hardcoded credential", QuotedCode: "password := \"hunter2\""},
	}
}

// Retriever nil => SemanticContext empty (byte-for-byte M6) and no stat.
func TestReviewRetrieverNilIsM6(t *testing.T) {
	dir, findings := semanticDir(t)
	fa := &fakeAgent{findings: findings}
	eng := engine.New(fa, gitcmd.New())
	res, err := eng.Review(stdctx.Background(), engine.Request{Mode: 0, RepoDir: dir, Gate: "high", Extensions: []string{"go"}})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if fa.gotSemantic != "" {
		t.Errorf("nil Retriever must leave SemanticContext empty, got %q", fa.gotSemantic)
	}
	if _, ok := res.Stats["semantic_recall"]; ok {
		t.Errorf("nil Retriever must not set semantic_recall stat: %v", res.Stats["semantic_recall"])
	}
}

// Retriever returning zero matches (empty advisory) => SemanticContext empty
// (still M6 prompt) but a no_matches stat for cost/outcome visibility.
func TestReviewRetrieverZeroMatchesIsM6(t *testing.T) {
	dir, findings := semanticDir(t)
	fa := &fakeAgent{findings: findings}
	r := &fakeRetriever{advisory: ""}
	eng := engine.New(fa, gitcmd.New())
	res, err := eng.Review(stdctx.Background(), engine.Request{Mode: 0, RepoDir: dir, Gate: "high", Extensions: []string{"go"}, Retriever: r})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if !r.called || len(r.gotCode) == 0 {
		t.Fatal("Retriever must be called with the change's code anchors")
	}
	if fa.gotSemantic != "" {
		t.Errorf("zero matches must leave SemanticContext empty, got %q", fa.gotSemantic)
	}
	if res.Stats["semantic_recall"] != "no_matches" {
		t.Errorf("semantic_recall: want no_matches, got %v", res.Stats["semantic_recall"])
	}
}

// Retriever with hits => advisory injected into SemanticContext; findings count
// and content unchanged (additive only).
func TestReviewRetrieverHitsInject(t *testing.T) {
	dir, findings := semanticDir(t)
	fa := &fakeAgent{findings: findings}
	r := &fakeRetriever{advisory: "- [bug] prior off-by-one"}
	eng := engine.New(fa, gitcmd.New())
	res, err := eng.Review(stdctx.Background(), engine.Request{Mode: 0, RepoDir: dir, Gate: "high", Extensions: []string{"go"}, Retriever: r})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if fa.gotSemantic != "- [bug] prior off-by-one" {
		t.Errorf("advisory not threaded into SemanticContext, got %q", fa.gotSemantic)
	}
	if res.Stats["semantic_recall"] != "injected" {
		t.Errorf("semantic_recall: want injected, got %v", res.Stats["semantic_recall"])
	}
	if len(res.Findings) != 1 || res.Findings[0].Category != "security" {
		t.Errorf("semantic injection mutated findings: %+v", res.Findings)
	}
}

// Retriever error => SemanticContext empty (degrade to M6) + an error stat; the
// review never fails.
func TestReviewRetrieverErrorDegrades(t *testing.T) {
	dir, findings := semanticDir(t)
	fa := &fakeAgent{findings: findings}
	r := &fakeRetriever{err: errors.New("embedder timeout")}
	eng := engine.New(fa, gitcmd.New())
	res, err := eng.Review(stdctx.Background(), engine.Request{Mode: 0, RepoDir: dir, Gate: "high", Extensions: []string{"go"}, Retriever: r})
	if err != nil {
		t.Fatalf("Retriever error must not fail the review: %v", err)
	}
	if fa.gotSemantic != "" {
		t.Errorf("Retriever error must leave SemanticContext empty, got %q", fa.gotSemantic)
	}
	if res.Stats["semantic_recall"] != "error" {
		t.Errorf("semantic_recall: want error, got %v", res.Stats["semantic_recall"])
	}
	if len(res.Findings) != 1 {
		t.Errorf("Retriever error dropped findings: %+v", res.Findings)
	}
}

func TestReviewEmptyStaged(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "app.go", "package app\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")

	eng := engine.New(&fakeAgent{}, gitcmd.New())
	res, err := eng.Review(stdctx.Background(), engine.Request{Mode: 0, RepoDir: dir, Gate: "high", Extensions: []string{"go"}})
	if err != nil {
		t.Fatalf("Review empty staged: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Errorf("empty staged: want 0 findings, got %d", len(res.Findings))
	}
	if engine.GateFailed(res.Findings, "high") {
		t.Error("empty staged must not fail the gate")
	}
}
