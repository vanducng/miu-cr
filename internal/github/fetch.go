package github

import (
	stdctx "context"
	"fmt"
	"os"
	"strings"

	gh "github.com/google/go-github/v84/github"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

// PRInfo is the resolved PR: base/head SHAs, base branch, fork flag and the
// changed-file list. IsFork is true when head lives in a different repo (or the
// head repo was deleted), which means we always post to the BASE repo.
type PRInfo struct {
	Owner      string
	Repo       string
	Number     int
	HeadSHA    string
	BaseSHA    string
	BaseBranch string
	IsFork     bool
	// AuthorAssociation is the PR author's repo relationship (OWNER, MEMBER,
	// COLLABORATOR, CONTRIBUTOR, NONE, FIRST_TIME_CONTRIBUTOR, FIRST_TIMER); the
	// approve resolver treats the untrusted set as a hard block.
	AuthorAssociation string
	Files             []string
	// HTMLBase is the BASE repo's HTML URL (e.g. https://github.com/owner/repo),
	// used to build repo-relative blob permalinks. Never contains a token.
	HTMLBase string
}

// blobURL builds a repo-relative blob permalink at info.HeadSHA for path/line.
// When endLine>line it emits a #L{line}-L{endLine} range anchor. Returns "" when
// the HTML base or head SHA is unknown so callers can omit the link rather than
// emit a broken one. path is repo-relative; the URL never carries a token.
func blobURL(info *PRInfo, path string, line, endLine int) string {
	if info == nil || info.HTMLBase == "" || info.HeadSHA == "" || path == "" {
		return ""
	}
	u := fmt.Sprintf("%s/blob/%s/%s", strings.TrimRight(info.HTMLBase, "/"), info.HeadSHA, path)
	if line > 0 {
		u += fmt.Sprintf("#L%d", line)
		if endLine > line {
			u += fmt.Sprintf("-L%d", endLine)
		}
	}
	return u
}

// FetchPR resolves a PR's SHAs/fork status and its full changed-file list via a
// paginated ListFiles. A nil Head.Repo (deleted fork head) is treated as a fork.
func FetchPR(ctx stdctx.Context, client Client, ref PRRef) (*PRInfo, error) {
	pr, err := client.GetPR(ctx, ref.Owner, ref.Repo, ref.Number)
	if err != nil {
		return nil, ghAPIError("github.pr_fetch_failed", fmt.Sprintf("fetching PR %s/%s#%d", ref.Owner, ref.Repo, ref.Number), err)
	}
	if pr.Head == nil || pr.Base == nil {
		return nil, &clierr.CLIError{
			Code:    "github.pr_fetch_failed",
			Message: fmt.Sprintf("PR %s/%s#%d is missing head/base refs", ref.Owner, ref.Repo, ref.Number),
			Exit:    1,
		}
	}

	info := &PRInfo{
		Owner:             ref.Owner,
		Repo:              ref.Repo,
		Number:            ref.Number,
		HeadSHA:           pr.Head.GetSHA(),
		BaseSHA:           pr.Base.GetSHA(),
		BaseBranch:        pr.Base.GetRef(),
		IsFork:            isFork(ref, pr),
		AuthorAssociation: pr.GetAuthorAssociation(),
		HTMLBase:          pr.GetBase().GetRepo().GetHTMLURL(),
	}

	opts := &gh.ListOptions{PerPage: 100}
	for {
		files, resp, lerr := client.ListFiles(ctx, ref.Owner, ref.Repo, ref.Number, opts)
		if lerr != nil {
			return nil, ghAPIError("github.pr_fetch_failed", "listing PR files", lerr)
		}
		for _, f := range files {
			if name := f.GetFilename(); name != "" {
				info.Files = append(info.Files, name)
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return info, nil
}

// isFork reports whether the head lives outside the base repo. A deleted head
// repo (Head.Repo == nil) is treated as a fork: we never assume same-repo.
func isFork(ref PRRef, pr *gh.PullRequest) bool {
	if pr.Head.Repo == nil {
		return true
	}
	owner := ""
	if pr.Head.Repo.Owner != nil {
		owner = pr.Head.Repo.Owner.GetLogin()
	}
	// GitHub owner/repo names are case-insensitive; EqualFold avoids misflagging a
	// same-repo PR as a fork when the user-typed ref differs in casing from canonical.
	return !strings.EqualFold(owner, ref.Owner) || !strings.EqualFold(pr.Head.Repo.GetName(), ref.Repo)
}

// gitFetcher is the git subset FetchIntoTempClone needs; *gitcmd.Runner satisfies
// it, and tests inject a recorder to assert the fetch is non-shallow.
type gitFetcher interface {
	Output(ctx stdctx.Context, repoDir string, args ...string) ([]byte, error)
}

// FetchIntoTempClone creates a temp dir, inits a repo pointed at the PR's base
// repo, and NON-SHALLOW fetches the base branch + pull/N/head so ModeRange's
// merge-base has shared history. token!="" embeds an x-access-token credential in
// the remote URL for private repos; empty uses anonymous HTTPS (public). The
// returned cleanup removes the temp dir.
func FetchIntoTempClone(ctx stdctx.Context, runner gitFetcher, info *PRInfo, token string) (string, func(), error) {
	if runner == nil {
		runner = gitcmd.New()
	}
	dir, err := os.MkdirTemp("", "miucr-pr-")
	if err != nil {
		return "", func() {}, &clierr.CLIError{
			Code:    "github.fetch_failed",
			Message: config.RedactString(fmt.Sprintf("creating temp clone dir: %v", err)),
			Exit:    1,
		}
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	if _, err := runner.Output(ctx, dir, "init", "--quiet"); err != nil {
		cleanup()
		return "", func() {}, fetchError("git init", err)
	}

	remote := remoteURL(info.Owner, info.Repo, token)
	headRef := fmt.Sprintf("pull/%d/head", info.Number)
	// NON-SHALLOW: no --depth. ModeRange runs `git merge-base base head`, which
	// needs the shared history a shallow fetch would truncate.
	args := []string{"fetch", "--no-tags", "--quiet", remote, info.BaseBranch, headRef}
	if _, err := runner.Output(ctx, dir, args...); err != nil {
		cleanup()
		return "", func() {}, fetchError("git fetch base + pull/N/head", err)
	}
	// git init leaves an unborn HEAD, which the engine's repo guard
	// (git rev-parse HEAD) rejects. Detach HEAD onto the fetched head commit;
	// ModeRange diffs merge-base(base,head)..head, so head is sufficient.
	if _, err := runner.Output(ctx, dir, "checkout", "--quiet", info.HeadSHA); err != nil {
		cleanup()
		return "", func() {}, fetchError("git checkout head", err)
	}
	return dir, cleanup, nil
}

func remoteURL(owner, repo, token string) string {
	if token != "" {
		return fmt.Sprintf("https://x-access-token:%s@github.com/%s/%s", token, owner, repo)
	}
	return fmt.Sprintf("https://github.com/%s/%s", owner, repo)
}

func fetchError(stage string, err error) error {
	return &clierr.CLIError{
		Code:    "github.fetch_failed",
		Message: config.RedactString(fmt.Sprintf("%s failed: %v", stage, err)),
		Hint:    "the fetch must be non-shallow (no --depth) so merge-base has shared history",
		Exit:    1,
	}
}

func ghAPIError(code, stage string, err error) error {
	return &clierr.CLIError{
		Code:    code,
		Message: config.RedactString(fmt.Sprintf("%s: %v", stage, err)),
		Exit:    1,
	}
}
