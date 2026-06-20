package gitcmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func repoWith(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	git(t, dir, "config", "user.email", "t@example.com")
	git(t, dir, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func TestHeadSHA(t *testing.T) {
	dir := repoWith(t, "a.go", "package a\n")
	r := New()
	sha, err := r.HeadSHA(context.Background(), dir)
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	if len(sha) != 40 {
		t.Errorf("HeadSHA = %q, want a 40-char SHA", sha)
	}
}

func TestShowBlobCommitted(t *testing.T) {
	dir := repoWith(t, "a.go", "package a\n\nvar X = 1\n")
	r := New()
	out, err := r.ShowBlob(context.Background(), dir, "HEAD", "a.go")
	if err != nil {
		t.Fatalf("ShowBlob: %v", err)
	}
	if string(out) != "package a\n\nvar X = 1\n" {
		t.Errorf("ShowBlob = %q", out)
	}
}

func TestShowBlobStagedIndex(t *testing.T) {
	dir := repoWith(t, "a.go", "package a\n")
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nvar Staged = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "a.go")
	r := New()
	// empty rev reads the index blob, not HEAD
	out, err := r.ShowBlob(context.Background(), dir, "", "a.go")
	if err != nil {
		t.Fatalf("ShowBlob (index): %v", err)
	}
	if !strings.Contains(string(out), "var Staged = 2") {
		t.Errorf("staged ShowBlob did not read the index blob: %q", out)
	}
}

// A failing git command surfaces git's stderr in the error, not a bare exit code.
func TestOutputSurfacesStderr(t *testing.T) {
	dir := repoWith(t, "a.go", "package a\n")
	r := New()
	_, err := r.Output(context.Background(), dir, "rev-parse", "--verify", "--end-of-options", "no-such-ref")
	if err == nil {
		t.Fatal("expected error for bad ref")
	}
	if !strings.Contains(err.Error(), "no-such-ref") && !strings.Contains(err.Error(), "fatal") {
		t.Errorf("error must include git stderr, got %q", err.Error())
	}
}

func TestOutputNonGitDir(t *testing.T) {
	r := New()
	_, err := r.HeadSHA(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("expected error outside a git repo")
	}
}
