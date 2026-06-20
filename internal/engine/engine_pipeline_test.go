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
	findings []engine.Finding
	gotRev   string
}

func (f *fakeAgent) Review(_ stdctx.Context, rc engine.AgentContext) ([]engine.Finding, error) {
	f.gotRev = rc.Rev
	out := make([]engine.Finding, len(f.findings))
	copy(out, f.findings)
	return out, nil
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
