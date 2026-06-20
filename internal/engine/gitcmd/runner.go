// Package gitcmd runs git subprocesses for the engine.
package gitcmd

import (
	"context"
	"os/exec"
	"strings"
)

// Runner wraps git subprocess invocation. The zero value is usable.
type Runner struct{}

// New returns a Runner.
func New() *Runner { return &Runner{} }

// Output runs `git <args...>` in repoDir and returns stdout only.
func (r *Runner) Output(ctx context.Context, repoDir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoDir
	return cmd.Output()
}

// HeadSHA returns the resolved commit SHA at HEAD.
func (r *Runner) HeadSHA(ctx context.Context, repoDir string) (string, error) {
	out, err := r.Output(ctx, repoDir, "rev-parse", "--verify", "--end-of-options", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ShowBlob reads a file blob via `git show <rev>:<path>`. An empty rev reads the
// staged blob from the index (`git show :<path>`).
func (r *Runner) ShowBlob(ctx context.Context, repoDir, rev, path string) ([]byte, error) {
	spec := rev + ":" + path
	return r.Output(ctx, repoDir, "-c", "core.quotepath=false", "show", "--end-of-options", spec)
}
