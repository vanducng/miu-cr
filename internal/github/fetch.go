package github

import (
	stdctx "context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"unicode/utf8"

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
	// URL-encode each path segment (spaces/special chars) while keeping the slashes.
	enc := make([]string, 0)
	for _, seg := range strings.Split(path, "/") {
		enc = append(enc, url.PathEscape(seg))
	}
	u := fmt.Sprintf("%s/blob/%s/%s", strings.TrimRight(info.HTMLBase, "/"), info.HeadSHA, strings.Join(enc, "/"))
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

// maxConversationBytes caps the rendered conversation block. Mirrors the rules
// token budget (defaultRulesTokenBudget=4096) so injected untrusted participant
// text can't starve the diff; over-cap content is truncated with an ellipsis marker.
const maxConversationBytes = 4096
const maxConvPages = 10 // bound conversation pagination (~1000 comments) so a huge PR can't fan out unboundedly

const conversationTruncated = "\n…(conversation truncated)"

// FetchConversation paginates the PR's prior miucr review summaries, inline
// finding threads, and developer issue replies into one labeled, byte-capped
// advisory string for the USER turn. It is best-effort: any list error is logged
// (redacted) and returns "" — a conversation fetch never fails the review (mirrors
// skipUnchanged's degrade-to-default). The caller drops it on fork PRs; this
// helper does no trust decision. Returns "" when there is nothing to inject.
func FetchConversation(ctx stdctx.Context, client Client, info *PRInfo) string {
	var b strings.Builder

	if summaries := fetchPriorSummaries(ctx, client, info); summaries != "" {
		b.WriteString("Prior miucr review summaries:\n")
		b.WriteString(summaries)
		b.WriteString("\n")
	}
	if threads := fetchInlineThreads(ctx, client, info); threads != "" {
		b.WriteString("Inline finding threads:\n")
		b.WriteString(threads)
		b.WriteString("\n")
	}
	if replies := fetchDeveloperReplies(ctx, client, info); replies != "" {
		b.WriteString("Developer replies:\n")
		b.WriteString(replies)
		b.WriteString("\n")
	}

	out := strings.TrimSpace(b.String())
	if out == "" {
		return ""
	}
	return capConversation(out)
}

// capConversation truncates s to maxConversationBytes (rune-safe), appending an
// ellipsis marker when it cuts. Empty stays empty.
func capConversation(s string) string {
	if len(s) <= maxConversationBytes {
		return s
	}
	// Budget is in BYTES; back up to a UTF-8 rune boundary so a multi-byte rune is
	// never split (the prior []rune[:keep] used a byte count as a rune index, which
	// could overshoot the byte budget up to ~4x).
	budget := maxConversationBytes - len(conversationTruncated)
	if budget < 0 {
		budget = 0
	}
	for budget > 0 && !utf8.RuneStart(s[budget]) {
		budget--
	}
	return s[:budget] + conversationTruncated
}

// fetchPriorSummaries returns miucr's own prior review summary bodies (those
// carrying ReviewMarker), newest pages last. "" on any list error.
func fetchPriorSummaries(ctx stdctx.Context, client Client, info *PRInfo) string {
	var b strings.Builder
	opts := &gh.ListOptions{PerPage: 100}
	for page := 0; page < maxConvPages; page++ {
		reviews, resp, err := client.ListReviews(ctx, info.Owner, info.Repo, info.Number, opts)
		if err != nil {
			os.Stderr.WriteString(config.RedactString("miucr: conversation fetch (reviews) skipped: "+err.Error()) + "\n")
			return ""
		}
		for _, r := range reviews {
			body := strings.TrimSpace(r.GetBody())
			if body != "" && strings.Contains(body, ReviewMarker) {
				b.WriteString("- ")
				b.WriteString(body)
				b.WriteString("\n")
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return strings.TrimSpace(b.String())
}

// fetchInlineThreads returns the bodies of inline review comments (finding
// threads). "" on any list error.
func fetchInlineThreads(ctx stdctx.Context, client Client, info *PRInfo) string {
	var b strings.Builder
	opts := &gh.PullRequestListCommentsOptions{ListOptions: gh.ListOptions{PerPage: 100}}
	for page := 0; page < maxConvPages; page++ {
		comments, resp, err := client.ListReviewComments(ctx, info.Owner, info.Repo, info.Number, opts)
		if err != nil {
			os.Stderr.WriteString(config.RedactString("miucr: conversation fetch (review comments) skipped: "+err.Error()) + "\n")
			return ""
		}
		for _, c := range comments {
			body := strings.TrimSpace(c.GetBody())
			if body == "" {
				continue
			}
			// Skip miucr's own prior inline findings (they carry the fp marker), so the
			// agent never re-reads its own output as "conversation" (feedback loop).
			if strings.Contains(body, fpPrefix) {
				continue
			}
			if path := c.GetPath(); path != "" {
				fmt.Fprintf(&b, "- [%s] %s\n", path, body)
			} else {
				fmt.Fprintf(&b, "- %s\n", body)
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return strings.TrimSpace(b.String())
}

// fetchDeveloperReplies returns top-level issue comments that are NOT miucr's own
// posts (a miucr summary carries ReviewMarker), so developer pushback is surfaced
// while the bot's own chatter is skipped (loop-guard). "" on any list error.
func fetchDeveloperReplies(ctx stdctx.Context, client Client, info *PRInfo) string {
	var b strings.Builder
	opts := &gh.IssueListCommentsOptions{ListOptions: gh.ListOptions{PerPage: 100}}
	for page := 0; page < maxConvPages; page++ {
		comments, resp, err := client.ListIssueComments(ctx, info.Owner, info.Repo, info.Number, opts)
		if err != nil {
			os.Stderr.WriteString(config.RedactString("miucr: conversation fetch (issue comments) skipped: "+err.Error()) + "\n")
			return ""
		}
		for _, c := range comments {
			body := strings.TrimSpace(c.GetBody())
			if body == "" || strings.Contains(body, ReviewMarker) {
				continue
			}
			b.WriteString("- ")
			b.WriteString(body)
			b.WriteString("\n")
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return strings.TrimSpace(b.String())
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

// ghAPIError classifies a GitHub API failure into a typed CLIError by a PROVEN
// signal: the go-github *ErrorResponse status (401/403/404/5xx) or a net error
// (DNS/refused/timeout). Anything unrecognized keeps the caller's fallback code
// (github.pr_fetch_failed) so a real bug is never mislabeled retryable. The
// message is redacted so a 401 body can't leak a token fragment.
func ghAPIError(fallback, stage string, err error) error {
	msg := config.RedactString(fmt.Sprintf("%s: %v", stage, err))

	// Rate-limit errors arrive as dedicated types that do NOT embed *gh.ErrorResponse,
	// so errors.As below would miss them; match them first. Reads are idempotent →
	// SafeRetry (mirrors mapWriteError in publish.go).
	var rle *gh.RateLimitError
	if errors.As(err, &rle) {
		return &clierr.CLIError{
			Code:      "github.rate_limited",
			Message:   "GitHub rate limit exceeded",
			Hint:      "wait for the rate limit to reset, then re-run",
			Exit:      1,
			Retry:     true,
			SafeRetry: true,
		}
	}
	var arle *gh.AbuseRateLimitError
	if errors.As(err, &arle) {
		return &clierr.CLIError{
			Code:      "github.rate_limited",
			Message:   "GitHub secondary (abuse) rate limit exceeded",
			Hint:      "wait before retrying",
			Exit:      1,
			Retry:     true,
			SafeRetry: true,
		}
	}

	var er *gh.ErrorResponse
	if errors.As(err, &er) && er.Response != nil {
		switch status := er.Response.StatusCode; {
		case status == 401 || status == 403:
			return &clierr.CLIError{
				Code:    "github.auth",
				Message: msg,
				Hint:    "check GITHUB_TOKEN / its repo scope",
				Exit:    1,
			}
		case status == 404:
			return &clierr.CLIError{
				Code:    "github.pr_not_found",
				Message: msg,
				Hint:    "check the PR exists and the token has access",
				Exit:    1,
			}
		case status == 429:
			return &clierr.CLIError{
				Code:      "github.rate_limited",
				Message:   msg,
				Hint:      "GitHub rate limit — wait for the reset and retry",
				Exit:      1,
				Retry:     true,
				SafeRetry: true,
			}
		case status >= 500 && status <= 599:
			return &clierr.CLIError{
				Code:      "github.unavailable",
				Message:   msg,
				Hint:      "GitHub is unavailable — retry shortly",
				Exit:      1,
				Retry:     true,
				SafeRetry: true,
			}
		}
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return &clierr.CLIError{
			Code:      "github.unavailable",
			Message:   msg,
			Hint:      "cannot reach GitHub — check your network and retry",
			Exit:      1,
			Retry:     true,
			SafeRetry: true,
		}
	}

	return &clierr.CLIError{
		Code:    fallback,
		Message: msg,
		Exit:    1,
	}
}
