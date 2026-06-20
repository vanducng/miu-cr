package diff

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

var (
	diffHeaderRe = regexp.MustCompile(`^diff --git a/(.+?) b/(.+)$`)
	binaryRe     = regexp.MustCompile(`Binary files `)
)

// ParseDiffText splits unified diff text into per-file Diffs. ref selects the
// revision NewFileContent is read from: empty ref reads the staged index blob
// (`git show :<path>`), otherwise `git show <ref>:<path>`.
func ParseDiffText(ctx context.Context, diffText, repoDir, ref string, runner *gitcmd.Runner) ([]Diff, error) {
	if runner == nil {
		runner = gitcmd.New()
	}
	lines := strings.Split(diffText, "\n")
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
		if m := diffHeaderRe.FindStringSubmatch(line); m != nil {
			flush()
			current = &Diff{OldPath: m[1], NewPath: m[2]}
		}
		if current == nil {
			continue
		}

		switch {
		case binaryRe.MatchString(line):
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
		fmt.Fprintf(os.Stderr, "[miucr] WARNING: cannot read %s at ref %q: %v\n", d.NewPath, ref, err)
		return
	}
	d.NewFileContent = string(out)
}
