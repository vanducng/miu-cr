package engine_test

import (
	stdctx "context"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

// progressDir stages one finding-worthy change and returns the repo dir + the
// canned finding set, mirroring semanticDir for the progress assertions.
func progressDir(t *testing.T) (string, []engine.Finding) {
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

// A non-nil Progress sink must receive the engine's file-selection milestone and
// be threaded into the AgentContext (so the agent's per-turn/tool emits reach it).
func TestReviewProgressEmitsMilestones(t *testing.T) {
	dir, findings := progressDir(t)
	fa := &fakeAgent{findings: findings}
	var msgs []string
	eng := engine.New(fa, gitcmd.New())
	res, err := eng.Review(stdctx.Background(), engine.Request{
		Mode: 0, RepoDir: dir, Gate: "high", Extensions: []string{"go"},
		Progress: func(m string) { msgs = append(msgs, m) },
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("want 1 finding, got %d", len(res.Findings))
	}
	if !fa.gotProgress {
		t.Error("Progress sink not threaded into AgentContext")
	}
	var sawReviewing, sawAgent bool
	for _, m := range msgs {
		if strings.HasPrefix(m, "reviewing ") {
			sawReviewing = true
		}
		if m == "agent ran" {
			sawAgent = true
		}
	}
	if !sawReviewing {
		t.Errorf("expected a \"reviewing N files…\" milestone, got %v", msgs)
	}
	if !sawAgent {
		t.Errorf("expected the agent-level milestone to reach the sink, got %v", msgs)
	}
}

// A nil Progress sink (the default) must not panic and must leave the review
// outcome identical, progress is a pure side-channel.
func TestReviewNilProgressIsSilentNoOp(t *testing.T) {
	dir, findings := progressDir(t)
	fa := &fakeAgent{findings: findings}
	eng := engine.New(fa, gitcmd.New())
	res, err := eng.Review(stdctx.Background(), engine.Request{
		Mode: 0, RepoDir: dir, Gate: "high", Extensions: []string{"go"},
	})
	if err != nil {
		t.Fatalf("nil Progress must not error: %v", err)
	}
	if fa.gotProgress {
		t.Error("nil Progress must thread through as nil, not a non-nil sink")
	}
	if len(res.Findings) != 1 {
		t.Fatalf("nil Progress changed the outcome: got %d findings", len(res.Findings))
	}
}
