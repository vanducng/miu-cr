package agent

import (
	stdctx "context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

func runToolRepo(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "T")
	run("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\nfunc Foo() {}\nfunc Bar() {}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "init")
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	return repo, strings.TrimSpace(string(out))
}

func TestRunTool_Validation(t *testing.T) {
	ctx := stdctx.Background()
	rc := Context{Runner: gitcmd.New()}

	if out, isErr := runTool(ctx, rc, 0, "file_read", json.RawMessage(`{}`)); !isErr || !strings.Contains(out, "non-empty") {
		t.Errorf("empty file_read: got %q isErr=%v", out, isErr)
	}
	if out, isErr := runTool(ctx, rc, 0, "grep", json.RawMessage(`{}`)); !isErr || !strings.Contains(out, "non-empty") {
		t.Errorf("empty grep: got %q isErr=%v", out, isErr)
	}
	if out, isErr := runTool(ctx, rc, 0, "bogus", json.RawMessage(`{}`)); !isErr || !strings.Contains(out, "unknown tool") {
		t.Errorf("unknown tool: got %q isErr=%v", out, isErr)
	}
}

func TestRunTool_FileReadAndGrep(t *testing.T) {
	repo, sha := runToolRepo(t)
	ctx := stdctx.Background()
	rc := Context{RepoDir: repo, Rev: sha, Runner: gitcmd.New()}

	out, isErr := runTool(ctx, rc, 0, "file_read", json.RawMessage(`{"file":"main.go"}`))
	if isErr || !strings.Contains(out, "func Foo()") {
		t.Errorf("file_read: got %q isErr=%v", out, isErr)
	}

	out, isErr = runTool(ctx, rc, 0, "grep", json.RawMessage(`{"pattern":"func Bar"}`))
	if isErr || !strings.Contains(out, "Bar") {
		t.Errorf("grep match: got %q isErr=%v", out, isErr)
	}

	out, isErr = runTool(ctx, rc, 0, "grep", json.RawMessage(`{"pattern":"zzz_no_such_symbol"}`))
	if isErr || out != "(no matches)" {
		t.Errorf("grep no-match: got %q isErr=%v", out, isErr)
	}

	out, isErr = runTool(ctx, rc, 0, "file_read", json.RawMessage(`{"file":"main.go","start":99,"end":100}`))
	if isErr || out != "(no lines in range)" {
		t.Errorf("file_read empty range: got %q isErr=%v", out, isErr)
	}
}
