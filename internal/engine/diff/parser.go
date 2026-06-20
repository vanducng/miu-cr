package diff

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

var (
	diffHeaderRe = regexp.MustCompile(`^diff --git a/(.+?) b/(.+)$`)
	// quoted form git emits for paths with non-ASCII/control bytes:
	// `diff --git "a/caf\303\251.go" "b/caf\303\251.go"`.
	diffHeaderQuotedRe = regexp.MustCompile(`^diff --git ("a/.*") ("b/.*")$`)
)

// parseDiffHeader matches a `diff --git` header line and returns the unquoted
// old/new paths. It handles both the bare form (`a/x b/x`) and git's C-quoted
// form for paths with non-ASCII or control bytes (`"a/x" "b/x"`), which no
// core.quotepath setting unquotes. ok is false when line is not a header.
func parseDiffHeader(line string) (oldPath, newPath string, ok bool) {
	if m := diffHeaderRe.FindStringSubmatch(line); m != nil {
		return m[1], m[2], true
	}
	if m := diffHeaderQuotedRe.FindStringSubmatch(line); m != nil {
		return strings.TrimPrefix(unquoteGitPath(m[1]), "a/"),
			strings.TrimPrefix(unquoteGitPath(m[2]), "b/"), true
	}
	return "", "", false
}

// unquoteGitPath decodes a git C-quoted path (octal byte escapes, standard C
// escapes). On any decode failure it returns the inner text with surrounding
// quotes stripped, so a malformed quote degrades to a best-effort path rather
// than dropping the file.
func unquoteGitPath(quoted string) string {
	if unquoted, err := strconv.Unquote(quoted); err == nil {
		return unquoted
	}
	return strings.Trim(quoted, `"`)
}

// isBinaryMarker reports whether line is git's unprefixed binary-diff notice
// ("Binary files a/x and b/x differ"), never a +/-/space hunk content line whose
// text merely contains the substring "Binary files ".
func isBinaryMarker(line string) bool {
	if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") || strings.HasPrefix(line, " ") {
		return false
	}
	return strings.HasPrefix(line, "Binary files ") && strings.HasSuffix(line, " differ")
}

// ParseDiffText splits unified diff text into per-file Diffs. ref selects the
// revision NewFileContent is read from: empty ref reads the staged index blob
// (`git show :<path>`), otherwise `git show <ref>:<path>`.
func ParseDiffText(ctx context.Context, diffText, repoDir, ref string, runner *gitcmd.Runner) ([]Diff, error) {
	if runner == nil {
		runner = gitcmd.New()
	}
	lines := strings.Split(diffText, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "diff --cc ") || strings.HasPrefix(line, "diff --combined ") {
			return nil, &clierr.CLIError{
				Code:    "git.combined_diff_unsupported",
				Message: "combined (merge) diff is not reviewable",
				Hint:    "review a parent range, or rely on --first-parent for merge commits",
				Exit:    1,
			}
		}
	}
	var diffs []Diff
	var current *Diff
	var buf strings.Builder

	flush := func() {
		if current == nil {
			return
		}
		current.Diff = strings.TrimSuffix(buf.String(), "\n")
		finalizeDiff(ctx, current, repoDir, ref, runner)
		diffs = append(diffs, *current)
		buf.Reset()
	}

	for _, line := range lines {
		if oldPath, newPath, ok := parseDiffHeader(line); ok {
			flush()
			current = &Diff{OldPath: oldPath, NewPath: newPath}
		} else if strings.HasPrefix(line, "diff --git ") {
			fmt.Fprintln(os.Stderr, config.RedactString(
				fmt.Sprintf("[miucr] WARNING: unparseable diff header, file dropped from review: %s", line)))
		}
		if current == nil {
			continue
		}

		switch {
		case isBinaryMarker(line):
			current.IsBinary = true
		case strings.HasPrefix(line, "new file mode "):
			current.IsNew = true
		case strings.HasPrefix(line, "deleted file mode "):
			current.IsDeleted = true
		case strings.HasPrefix(line, "rename from "):
			current.OldPath = strings.TrimPrefix(line, "rename from ")
			current.IsRenamed = true
		case strings.HasPrefix(line, "rename to "):
			current.NewPath = strings.TrimPrefix(line, "rename to ")
			current.IsRenamed = true
		case line == "--- /dev/null":
			current.IsNew = true
		case line == "+++ /dev/null":
			current.IsDeleted = true
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			current.Insertions++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			current.Deletions++
		}
		buf.WriteString(line)
		buf.WriteString("\n")
	}
	flush()

	return diffs, nil
}

// finalizeDiff records the revision and populates NewFileContent from it.
func finalizeDiff(ctx context.Context, d *Diff, repoDir, ref string, runner *gitcmd.Runner) {
	d.Ref = ref
	if d.IsDeleted || d.NewPath == "/dev/null" {
		d.NewPath = "/dev/null"
		return
	}
	if d.IsBinary {
		return
	}
	out, err := runner.ShowBlob(ctx, repoDir, ref, d.NewPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, config.RedactString(
			fmt.Sprintf("[miucr] WARNING: cannot read %s at ref %q: %v", d.NewPath, ref, err)))
		return
	}
	d.NewFileContent = string(out)
}
