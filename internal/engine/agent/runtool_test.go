package agent

import (
	stdctx "context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
	enginetools "github.com/vanducng/miu-cr/internal/engine/tools"
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
	if err := os.WriteFile(filepath.Join(repo, "other.go"), []byte("package main\nfunc Bar() {}\n"), 0o644); err != nil {
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
	if out, isErr := runTool(ctx, rc, 0, "file_read", json.RawMessage(`{"file":"main.go","start":"bad"}`)); !isErr || !strings.Contains(out, "invalid arguments") {
		t.Errorf("invalid file_read args: got %q isErr=%v", out, isErr)
	}
	if out, isErr := runTool(ctx, rc, 0, "bogus", json.RawMessage(`{}`)); !isErr || !strings.Contains(out, "unknown tool") {
		t.Errorf("unknown tool: got %q isErr=%v", out, isErr)
	}
}

func TestRunToolRetriesTransientErrors(t *testing.T) {
	prev := executeTool
	calls := 0
	executeTool = func(stdctx.Context, config.SymbolContext, enginetools.Context, int, string, json.RawMessage) (string, bool) {
		calls++
		if calls < 3 {
			return "grep failed: resource temporarily unavailable", true
		}
		return "ok", false
	}
	t.Cleanup(func() { executeTool = prev })

	maxRetries := 2
	out, isErr := runTool(stdctx.Background(), Context{Tools: config.ReviewTools{MaxRetries: &maxRetries, RetryBackoff: "0s"}}, 0, "grep", json.RawMessage(`{"pattern":"x"}`))
	if isErr || out != "ok" {
		t.Fatalf("runTool = %q isErr=%v, want ok", out, isErr)
	}
	if calls != 3 {
		t.Fatalf("tool calls = %d, want 3", calls)
	}
}

func TestRunToolDoesNotRetryTerminalErrors(t *testing.T) {
	prev := executeTool
	calls := 0
	executeTool = func(stdctx.Context, config.SymbolContext, enginetools.Context, int, string, json.RawMessage) (string, bool) {
		calls++
		return "file_read: invalid arguments: bad", true
	}
	t.Cleanup(func() { executeTool = prev })

	maxRetries := 5
	out, isErr := runTool(stdctx.Background(), Context{Tools: config.ReviewTools{MaxRetries: &maxRetries, RetryBackoff: "0s"}}, 0, "file_read", json.RawMessage(`{}`))
	if !isErr || !strings.Contains(out, "invalid arguments") {
		t.Fatalf("runTool = %q isErr=%v, want terminal error", out, isErr)
	}
	if calls != 1 {
		t.Fatalf("tool calls = %d, want 1", calls)
	}
}

func TestRunToolZeroRetries(t *testing.T) {
	prev := executeTool
	calls := 0
	executeTool = func(stdctx.Context, config.SymbolContext, enginetools.Context, int, string, json.RawMessage) (string, bool) {
		calls++
		return "grep failed: temporary failure", true
	}
	t.Cleanup(func() { executeTool = prev })

	maxRetries := 0
	out, isErr := runTool(stdctx.Background(), Context{Tools: config.ReviewTools{MaxRetries: &maxRetries, RetryBackoff: "0s"}}, 0, "grep", json.RawMessage(`{"pattern":"x"}`))
	if !isErr || !strings.Contains(out, "temporary failure") {
		t.Fatalf("runTool = %q isErr=%v, want original transient error", out, isErr)
	}
	if calls != 1 {
		t.Fatalf("tool calls = %d, want 1", calls)
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

	out, isErr = runTool(ctx, rc, 0, "grep", json.RawMessage(`{"pattern":"func Bar","file":"other.go"}`))
	if isErr || !strings.Contains(out, "File: other.go") || strings.Contains(out, "File: main.go") {
		t.Errorf("grep file scope: got %q isErr=%v", out, isErr)
	}

	out, isErr = runTool(ctx, rc, 0, "grep", json.RawMessage(`{"pattern":"zzz_no_such_symbol"}`))
	if isErr || out != "(no matches)" {
		t.Errorf("grep no-match: got %q isErr=%v", out, isErr)
	}

	out, isErr = runTool(ctx, rc, 0, "symbol_context", json.RawMessage(`{"relation":"document_symbols","file":"main.go"}`))
	if isErr || !strings.Contains(out, "Document symbols for main.go") || !strings.Contains(out, "Foo") {
		t.Errorf("symbol_context dispatch: got %q isErr=%v", out, isErr)
	}

	out, isErr = runTool(ctx, rc, 0, "file_read", json.RawMessage(`{"file":"main.go","start":99,"end":100}`))
	if isErr || out != "(no lines in range)" {
		t.Errorf("file_read empty range: got %q isErr=%v", out, isErr)
	}
}
