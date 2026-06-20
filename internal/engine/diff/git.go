package diff

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

const contextLines = 3

// diffFlags are shared by every diff/show invocation so the parser sees
// canonical unified-diff text regardless of the user's git config.
var diffFlags = []string{
	"--no-ext-diff", "--no-textconv", "--find-renames",
	"--src-prefix=a/", "--dst-prefix=b/", "--no-color",
	"-U" + strconv.Itoa(contextLines), "--end-of-options",
}

// GetDiff acquires the diff for the given mode and parses it into per-file
// Diffs whose NewFileContent is read from the mode-correct revision.
func GetDiff(ctx context.Context, mode Mode, repoDir, from, to, commit string, runner *gitcmd.Runner) ([]Diff, error) {
	if runner == nil {
		runner = gitcmd.New()
	}
	if _, err := runner.HeadSHA(ctx, repoDir); err != nil {
		return nil, &clierr.CLIError{
			Code:    "git.not_a_repo",
			Message: fmt.Sprintf("%s is not a git repository with commits", repoDir),
			Hint:    "run from inside a git repo that has at least one commit",
			Exit:    1,
		}
	}

	var (
		diffText string
		ref      string
	)
	switch mode {
	case ModeStaged:
		args := append([]string{"diff", "--cached"}, diffFlags...)
		args = append(args, "--")
		out, err := runner.Output(ctx, repoDir, args...)
		if err != nil {
			return nil, gitError("git.diff_failed", "git diff --cached failed", err)
		}
		diffText = string(out)
		ref = "" // index: read via `git show :<path>`

	case ModeCommit:
		// -m --first-parent makes `git show` emit a normal two-way `diff --git`
		// even for merge commits (vs the default `diff --cc` combined diff the
		// parser cannot read, which would otherwise yield a false "clean").
		args := append([]string{"show", "-m", "--first-parent"}, diffFlags...)
		args = append(args, commit)
		out, err := runner.Output(ctx, repoDir, args...)
		if err != nil {
			return nil, gitError("git.show_failed", fmt.Sprintf("git show %s failed", commit), err)
		}
		diffText = string(out)
		ref = commit

	case ModeRange:
		baseOut, err := runner.Output(ctx, repoDir, "merge-base", "--end-of-options", from, to)
		if err != nil {
			return nil, &clierr.CLIError{
				Code:    "git.merge_base_failed",
				Message: fmt.Sprintf("cannot find merge-base between %s and %s", from, to),
				Hint:    "ensure both refs exist and share history (not a shallow or unrelated clone)",
				Exit:    1,
			}
		}
		base := strings.TrimSpace(string(baseOut))
		if base == "" {
			return nil, &clierr.CLIError{
				Code:    "git.merge_base_failed",
				Message: fmt.Sprintf("cannot find merge-base between %s and %s", from, to),
				Hint:    "ensure both refs exist and share history (not a shallow or unrelated clone)",
				Exit:    1,
			}
		}
		args := append([]string{"diff"}, diffFlags...)
		args = append(args, base, to, "--")
		out, err := runner.Output(ctx, repoDir, args...)
		if err != nil {
			return nil, gitError("git.diff_failed", "git diff range failed", err)
		}
		diffText = string(out)
		ref = to

	default:
		return nil, &clierr.CLIError{
			Code:    "git.bad_mode",
			Message: fmt.Sprintf("unknown diff mode %d", int(mode)),
			Exit:    1,
		}
	}

	return ParseDiffText(ctx, diffText, repoDir, ref, runner)
}

func gitError(code, msg string, err error) error {
	return &clierr.CLIError{
		Code:    code,
		Message: config.RedactString(fmt.Sprintf("%s: %v", msg, err)),
		Exit:    1,
	}
}
