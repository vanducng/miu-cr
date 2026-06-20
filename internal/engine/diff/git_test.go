package diff

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/cli"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

func runGitTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGitTest(t, repo, "init", "-q")
	runGitTest(t, repo, "config", "user.email", "test@example.com")
	runGitTest(t, repo, "config", "user.name", "Test User")
	runGitTest(t, repo, "config", "commit.gpgsign", "false")
	return repo
}

func TestGetDiff_Staged(t *testing.T) {
	repo := initRepo(t)
	file := filepath.Join(repo, "sample.txt")
	if err := os.WriteFile(file, []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGitTest(t, repo, "add", "sample.txt")
	runGitTest(t, repo, "commit", "-q", "-m", "initial")

	if err := os.WriteFile(file, []byte("line1\nSTAGED\nline3\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGitTest(t, repo, "add", "sample.txt")

	diffs, err := GetDiff(context.Background(), ModeStaged, repo, "", "", "", gitcmd.New())
	if err != nil {
		t.Fatalf("GetDiff: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d", len(diffs))
	}
	if diffs[0].NewPath != "sample.txt" {
		t.Errorf("NewPath = %q", diffs[0].NewPath)
	}
	if !strings.Contains(diffs[0].NewFileContent, "STAGED") {
		t.Errorf("NewFileContent missing staged content: %q", diffs[0].NewFileContent)
	}
	if diffs[0].Ref != "" {
		t.Errorf("Ref = %q, want empty (index)", diffs[0].Ref)
	}
}

// Index-vs-worktree: stage an edit, then edit the worktree AGAIN. NewFileContent
// for --staged must equal the STAGED (index) content, not the later worktree
// edit and not HEAD. (red-team CRITICAL)
func TestGetDiff_StagedReadsIndexNotWorktree(t *testing.T) {
	repo := initRepo(t)
	file := filepath.Join(repo, "f.txt")
	if err := os.WriteFile(file, []byte("orig\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGitTest(t, repo, "add", "f.txt")
	runGitTest(t, repo, "commit", "-q", "-m", "initial")

	// Stage version A.
	if err := os.WriteFile(file, []byte("STAGED_VERSION\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGitTest(t, repo, "add", "f.txt")

	// Edit the worktree again (version B) WITHOUT staging.
	if err := os.WriteFile(file, []byte("WORKTREE_VERSION\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	diffs, err := GetDiff(context.Background(), ModeStaged, repo, "", "", "", gitcmd.New())
	if err != nil {
		t.Fatalf("GetDiff: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d", len(diffs))
	}
	got := diffs[0].NewFileContent
	if !strings.Contains(got, "STAGED_VERSION") {
		t.Errorf("NewFileContent = %q, want staged (index) content", got)
	}
	if strings.Contains(got, "WORKTREE_VERSION") {
		t.Errorf("NewFileContent leaked later worktree edit: %q", got)
	}
	if strings.Contains(got, "orig") {
		t.Errorf("NewFileContent read HEAD instead of index: %q", got)
	}
}

func TestGetDiff_Commit(t *testing.T) {
	repo := initRepo(t)
	file := filepath.Join(repo, "sample.txt")
	if err := os.WriteFile(file, []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGitTest(t, repo, "add", "sample.txt")
	runGitTest(t, repo, "commit", "-q", "-m", "initial")

	if err := os.WriteFile(file, []byte("line1\nCOMMITTED\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGitTest(t, repo, "add", "sample.txt")
	runGitTest(t, repo, "commit", "-q", "-m", "second")

	diffs, err := GetDiff(context.Background(), ModeCommit, repo, "", "", "HEAD", gitcmd.New())
	if err != nil {
		t.Fatalf("GetDiff: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d", len(diffs))
	}
	if !strings.Contains(diffs[0].NewFileContent, "COMMITTED") {
		t.Errorf("NewFileContent = %q, want commit content", diffs[0].NewFileContent)
	}
	if diffs[0].Ref != "HEAD" {
		t.Errorf("Ref = %q, want HEAD", diffs[0].Ref)
	}
}

func TestGetDiff_Range(t *testing.T) {
	repo := initRepo(t)
	file := filepath.Join(repo, "sample.txt")
	var content strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&content, "line%d\n", i)
	}
	if err := os.WriteFile(file, []byte(content.String()), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGitTest(t, repo, "add", "sample.txt")
	runGitTest(t, repo, "commit", "-q", "-m", "initial")

	runGitTest(t, repo, "checkout", "-q", "-b", "feature")
	edited := strings.Replace(content.String(), "line5\n", "RANGE_EDIT\n", 1)
	if err := os.WriteFile(file, []byte(edited), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGitTest(t, repo, "add", "sample.txt")
	runGitTest(t, repo, "commit", "-q", "-m", "feature edit")

	diffs, err := GetDiff(context.Background(), ModeRange, repo, "HEAD~1", "feature", "", gitcmd.New())
	if err != nil {
		t.Fatalf("GetDiff: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d", len(diffs))
	}
	if !strings.Contains(diffs[0].NewFileContent, "RANGE_EDIT") {
		t.Errorf("NewFileContent = %q, want range <to> content", diffs[0].NewFileContent)
	}
	if diffs[0].Ref != "feature" {
		t.Errorf("Ref = %q, want feature (the <to> revision)", diffs[0].Ref)
	}
}

func TestGetDiff_EmptyStaged(t *testing.T) {
	repo := initRepo(t)
	file := filepath.Join(repo, "sample.txt")
	if err := os.WriteFile(file, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGitTest(t, repo, "add", "sample.txt")
	runGitTest(t, repo, "commit", "-q", "-m", "initial")

	diffs, err := GetDiff(context.Background(), ModeStaged, repo, "", "", "", gitcmd.New())
	if err != nil {
		t.Fatalf("GetDiff: %v", err)
	}
	if len(diffs) != 0 {
		t.Fatalf("expected 0 diffs for empty staged set, got %d", len(diffs))
	}
}

func TestGetDiff_NonGitDir(t *testing.T) {
	dir := t.TempDir()
	_, err := GetDiff(context.Background(), ModeStaged, dir, "", "", "", gitcmd.New())
	if err == nil {
		t.Fatal("expected typed error for non-git dir")
	}
	var cliErr *cli.CLIError
	if !errors.As(err, &cliErr) {
		t.Fatalf("expected *cli.CLIError, got %T", err)
	}
	if cliErr.Code != "git.not_a_repo" {
		t.Errorf("Code = %q, want git.not_a_repo", cliErr.Code)
	}
}

func TestGetDiff_RangeUnrelatedHistories(t *testing.T) {
	repo := initRepo(t)
	f := filepath.Join(repo, "a.txt")
	if err := os.WriteFile(f, []byte("a\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGitTest(t, repo, "add", "a.txt")
	runGitTest(t, repo, "commit", "-q", "-m", "main commit")

	// Orphan branch -> unrelated history -> merge-base fails.
	runGitTest(t, repo, "checkout", "-q", "--orphan", "other")
	if err := os.WriteFile(filepath.Join(repo, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGitTest(t, repo, "add", "b.txt")
	runGitTest(t, repo, "commit", "-q", "-m", "orphan commit")

	_, err := GetDiff(context.Background(), ModeRange, repo, "master", "other", "", gitcmd.New())
	if err == nil {
		// Some git defaults name the first branch "main"; retry with main.
		_, err = GetDiff(context.Background(), ModeRange, repo, "main", "other", "", gitcmd.New())
	}
	if err == nil {
		t.Fatal("expected merge-base failure for unrelated histories")
	}
	var cliErr *cli.CLIError
	if !errors.As(err, &cliErr) {
		t.Fatalf("expected *cli.CLIError, got %T", err)
	}
	if cliErr.Code != "git.merge_base_failed" {
		t.Errorf("Code = %q, want git.merge_base_failed", cliErr.Code)
	}
}
