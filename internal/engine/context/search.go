// Package context builds the deterministic review context the agent sees:
// diff hunks plus grep expansion and read-surrounding-code, all read from the
// reviewed revision (never the live working tree).
package context

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

const (
	grepMaxCount    = 100
	readRangeMaxCap = 500
)

// Grep searches the reviewed revision for a fixed-string pattern via `git grep`.
// An empty rev means the staged index (git grep --cached). Output is grouped by
// file with "N|line" rows; results are bounded by grepMaxCount per file.
func Grep(ctx context.Context, repoDir, rev, pattern string, runner *gitcmd.Runner) (string, error) {
	if strings.TrimSpace(pattern) == "" {
		return "", nil
	}
	if runner == nil {
		runner = gitcmd.New()
	}
	out, err := gitGrep(ctx, repoDir, rev, pattern, runner)
	if err != nil {
		return "", err
	}
	return formatGrep(out), nil
}

// gitGrep runs `git grep -n -F --max-count <N>` against the reviewed revision
// (or the staged index via --cached when rev is empty). All M1 modes are
// revision-pinned, so the live working tree is never searched.
func gitGrep(ctx context.Context, repoDir, rev, pattern string, runner *gitcmd.Runner) (string, error) {
	if rev == "" {
		out, err := runner.Output(ctx, repoDir, "--no-pager", "grep", "-n", "-F",
			"--no-color", "--max-count", strconv.Itoa(grepMaxCount), "--cached", "-e", pattern)
		return string(out), grepErr(err, string(out))
	}
	out, err := runner.Output(ctx, repoDir, "--no-pager", "grep", "-n", "-F",
		"--no-color", "--max-count", strconv.Itoa(grepMaxCount), "-e", pattern,
		"--end-of-options", rev)
	return string(out), grepErr(err, string(out))
}

// grepErr treats grep's exit-1 (no matches) as success.
func grepErr(err error, out string) error {
	if err == nil {
		return nil
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		return nil
	}
	if out != "" {
		return nil
	}
	return err
}

// formatGrep regroups "rev:file:line:content" or "file:line:content" rows into
// per-file blocks of "N|line".
func formatGrep(raw string) string {
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	type match struct {
		num     int
		content string
	}
	byFile := map[string][]match{}
	var order []string
	for _, ln := range lines {
		if ln == "" {
			continue
		}
		file, num, content, ok := splitGrepLine(ln)
		if !ok {
			continue
		}
		if _, seen := byFile[file]; !seen {
			order = append(order, file)
		}
		byFile[file] = append(byFile[file], match{num, content})
	}
	var sb strings.Builder
	for _, file := range order {
		ms := byFile[file]
		sb.WriteString(fmt.Sprintf("File: %s\nMatch lines: %d\n", file, len(ms)))
		for _, m := range ms {
			sb.WriteString(fmt.Sprintf("%d|%s\n", m.num, m.content))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// splitGrepLine parses "file:line:content"; a leading "rev:file:line:content"
// (git grep at a revision) is detected when the second-to-last numeric field is
// the line number.
func splitGrepLine(ln string) (file string, num int, content string, ok bool) {
	for splitN := 3; splitN <= 4; splitN++ {
		parts := strings.SplitN(ln, ":", splitN)
		if len(parts) < splitN {
			continue
		}
		n, err := strconv.Atoi(parts[splitN-2])
		if err != nil {
			continue
		}
		file = parts[splitN-3]
		num = n
		content = parts[splitN-1]
		return file, num, content, true
	}
	return "", 0, "", false
}

// ReadRange reads lines [start,end] of path at the reviewed revision and returns
// them formatted "N|line". An empty rev reads the staged index blob. The window
// is capped at readRangeMaxCap lines.
func ReadRange(ctx context.Context, repoDir, rev, path string, start, end int, runner *gitcmd.Runner) (string, error) {
	if runner == nil {
		runner = gitcmd.New()
	}
	if start <= 0 {
		start = 1
	}
	if end <= 0 || end < start {
		end = start + readRangeMaxCap - 1
	}
	if end-start+1 > readRangeMaxCap {
		end = start + readRangeMaxCap - 1
	}
	blob, err := runner.ShowBlob(ctx, repoDir, rev, path)
	if err != nil {
		return "", fmt.Errorf("read %s at ref %q: %w", path, rev, err)
	}
	all := strings.Split(string(blob), "\n")
	if len(all) > 0 && all[len(all)-1] == "" {
		all = all[:len(all)-1]
	}
	var sb strings.Builder
	for i := start; i <= end && i <= len(all); i++ {
		sb.WriteString(fmt.Sprintf("%d|%s\n", i, all[i-1]))
	}
	return sb.String(), nil
}
