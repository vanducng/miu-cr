package context

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "t@example.com")
	runGit(t, repo, "config", "user.name", "T")
	runGit(t, repo, "config", "commit.gpgsign", "false")
	return repo
}

func writeFile(t *testing.T, repo, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func commit(t *testing.T, repo, msg string) string {
	t.Helper()
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", msg)
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func TestReadRange_Cap500(t *testing.T) {
	repo := initRepo(t)
	var b strings.Builder
	for i := 1; i <= 800; i++ {
		b.WriteString("line")
		b.WriteString("\n")
	}
	writeFile(t, repo, "big.txt", b.String())
	sha := commit(t, repo, "big")

	out, err := ReadRange(context.Background(), repo, sha, "big.txt", 1, 0, gitcmd.New())
	if err != nil {
		t.Fatalf("ReadRange: %v", err)
	}
	got := strings.Count(out, "\n")
	if got != readRangeMaxCap {
		t.Fatalf("expected %d lines, got %d", readRangeMaxCap, got)
	}
	if !strings.HasPrefix(out, "1|line\n") {
		t.Fatalf("bad first line: %q", out[:20])
	}
	if !strings.Contains(out, "500|line\n") {
		t.Fatalf("expected line 500 present")
	}
	if strings.Contains(out, "501|") {
		t.Fatalf("line 501 must be capped out")
	}
}

func TestReadRange_SameRevision_CommitMode(t *testing.T) {
	repo := initRepo(t)
	writeFile(t, repo, "f.go", "package main\nfunc Old() {}\n")
	sha := commit(t, repo, "v1")

	// Edit the worktree AFTER the commit; it must NOT leak into the reviewed read.
	writeFile(t, repo, "f.go", "package main\nfunc Leaked() {}\n")

	out, err := ReadRange(context.Background(), repo, sha, "f.go", 1, 10, gitcmd.New())
	if err != nil {
		t.Fatalf("ReadRange: %v", err)
	}
	if !strings.Contains(out, "Old()") {
		t.Fatalf("expected reviewed-revision content, got %q", out)
	}
	if strings.Contains(out, "Leaked") {
		t.Fatalf("worktree edit leaked into reviewed read: %q", out)
	}
}

func TestGrep_SameRevision_CommitMode(t *testing.T) {
	repo := initRepo(t)
	writeFile(t, repo, "f.go", "package main\nvar Token = \"committed\"\n")
	sha := commit(t, repo, "v1")

	writeFile(t, repo, "f.go", "package main\nvar Token = \"worktree\"\n")

	out, err := Grep(context.Background(), repo, sha, "Token", gitcmd.New())
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if !strings.Contains(out, "committed") {
		t.Fatalf("expected committed content, got %q", out)
	}
	if strings.Contains(out, "worktree") {
		t.Fatalf("worktree edit leaked into grep: %q", out)
	}
	if !strings.Contains(out, "File: f.go") {
		t.Fatalf("expected file-grouped output, got %q", out)
	}
}

func TestGrep_FileScoped(t *testing.T) {
	repo := initRepo(t)
	writeFile(t, repo, "f.go", "package main\nvar Token = \"wanted\"\n")
	writeFile(t, repo, "other.go", "package main\nvar Token = \"other\"\n")
	sha := commit(t, repo, "v1")

	out, err := Grep(context.Background(), repo, sha, "Token", gitcmd.New(), "f.go")
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if !strings.Contains(out, "wanted") {
		t.Fatalf("expected scoped match, got %q", out)
	}
	if strings.Contains(out, "other") || strings.Contains(out, "File: other.go") {
		t.Fatalf("file-scoped grep leaked sibling match: %q", out)
	}
}

func TestGrep_StagedMode_ReadsIndexNotWorktree(t *testing.T) {
	repo := initRepo(t)
	writeFile(t, repo, "f.go", "package main\nvar Marker = \"staged\"\n")
	runGit(t, repo, "add", "-A") // stage into index (rev == "" path)

	// Edit the worktree AFTER staging; with rg on PATH this used to leak.
	writeFile(t, repo, "f.go", "package main\nvar Marker = \"worktree\"\n")

	if _, err := exec.LookPath("rg"); err != nil {
		t.Logf("rg not on PATH; staged grep must still read the index")
	}

	out, err := Grep(context.Background(), repo, "", "Marker", gitcmd.New())
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if !strings.Contains(out, "staged") {
		t.Fatalf("expected staged index content, got %q", out)
	}
	if strings.Contains(out, "worktree") {
		t.Fatalf("worktree edit leaked into staged grep: %q", out)
	}
	if !strings.Contains(out, "File: f.go") {
		t.Fatalf("expected file-grouped output, got %q", out)
	}
}
