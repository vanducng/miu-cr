// Package github fetches a GitHub PR (metadata + changed files) and materializes
// it into a local non-shallow temp clone the M1 engine can review via ModeRange.
// It wraps go-github/v84 (WithAuthToken, anonymous when no token) behind a Client
// interface whose full read+write method set is defined up front so the M2 publish
// path's fake implements it once.
package github

import (
	stdctx "context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	gh "github.com/google/go-github/v84/github"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
)

// Client is the full GitHub surface M2 needs: read ops for the dry-run path and
// write ops for the publish path (defined now so the P2 fake implements it once).
// Methods take the raw go-github types so FetchPR/publish drive pagination and
// field mapping without leaking those concerns into the interface.
type Client interface {
	GetPR(ctx stdctx.Context, owner, repo string, number int) (*gh.PullRequest, error)
	ListFiles(ctx stdctx.Context, owner, repo string, number int, opts *gh.ListOptions) ([]*gh.CommitFile, *gh.Response, error)

	CreateReview(ctx stdctx.Context, owner, repo string, number int, review *gh.PullRequestReviewRequest) (*gh.PullRequestReview, error)
	ListReviewComments(ctx stdctx.Context, owner, repo string, number int, opts *gh.PullRequestListCommentsOptions) ([]*gh.PullRequestComment, *gh.Response, error)
	ListIssueComments(ctx stdctx.Context, owner, repo string, number int, opts *gh.IssueListCommentsOptions) ([]*gh.IssueComment, *gh.Response, error)
	CreateIssueComment(ctx stdctx.Context, owner, repo string, number int, comment *gh.IssueComment) (*gh.IssueComment, error)
	EditIssueComment(ctx stdctx.Context, owner, repo string, commentID int64, comment *gh.IssueComment) (*gh.IssueComment, error)
}

// ghClient wraps *github.Client. An empty token yields an anonymous client (fine
// for public-repo reads); a non-empty token authenticates via WithAuthToken so we
// avoid pulling in an external auth-transport dependency.
type ghClient struct{ c *gh.Client }

// NewClient returns a Client. token=="" → anonymous; else WithAuthToken (PAT). The
// underlying http.Client carries a 30s timeout so a stalled connection (DNS/TLS) can
// never hang indefinitely even if a caller forgets to bound the context.
func NewClient(token string) Client {
	c := gh.NewClient(&http.Client{Timeout: 30 * time.Second})
	if token != "" {
		c = c.WithAuthToken(token)
	}
	return ghClient{c: c}
}

func (g ghClient) GetPR(ctx stdctx.Context, owner, repo string, number int) (*gh.PullRequest, error) {
	pr, _, err := g.c.PullRequests.Get(ctx, owner, repo, number)
	return pr, err
}

func (g ghClient) ListFiles(ctx stdctx.Context, owner, repo string, number int, opts *gh.ListOptions) ([]*gh.CommitFile, *gh.Response, error) {
	return g.c.PullRequests.ListFiles(ctx, owner, repo, number, opts)
}

func (g ghClient) CreateReview(ctx stdctx.Context, owner, repo string, number int, review *gh.PullRequestReviewRequest) (*gh.PullRequestReview, error) {
	r, _, err := g.c.PullRequests.CreateReview(ctx, owner, repo, number, review)
	return r, err
}

func (g ghClient) ListReviewComments(ctx stdctx.Context, owner, repo string, number int, opts *gh.PullRequestListCommentsOptions) ([]*gh.PullRequestComment, *gh.Response, error) {
	return g.c.PullRequests.ListComments(ctx, owner, repo, number, opts)
}

func (g ghClient) ListIssueComments(ctx stdctx.Context, owner, repo string, number int, opts *gh.IssueListCommentsOptions) ([]*gh.IssueComment, *gh.Response, error) {
	return g.c.Issues.ListComments(ctx, owner, repo, number, opts)
}

func (g ghClient) CreateIssueComment(ctx stdctx.Context, owner, repo string, number int, comment *gh.IssueComment) (*gh.IssueComment, error) {
	c, _, err := g.c.Issues.CreateComment(ctx, owner, repo, number, comment)
	return c, err
}

func (g ghClient) EditIssueComment(ctx stdctx.Context, owner, repo string, commentID int64, comment *gh.IssueComment) (*gh.IssueComment, error) {
	c, _, err := g.c.Issues.EditComment(ctx, owner, repo, commentID, comment)
	return c, err
}

// PRRef identifies a pull request: owner/repo and its number.
type PRRef struct {
	Owner  string
	Repo   string
	Number int
}

var (
	urlRefRe   = regexp.MustCompile(`^https?://github\.com/([^/]+)/([^/]+)/pull/(\d+)/?$`)
	shortRefRe = regexp.MustCompile(`^([^/]+)/([^/#]+)#(\d+)$`)
)

// ParseRef accepts "https://github.com/owner/repo/pull/N" or "owner/repo#N",
// tolerating surrounding whitespace from pasted refs. Anything else is a typed
// github.bad_pr_ref error.
func ParseRef(s string) (PRRef, error) {
	s = strings.TrimSpace(s)
	if m := urlRefRe.FindStringSubmatch(s); m != nil {
		return prRefFrom(m[1], m[2], m[3])
	}
	if m := shortRefRe.FindStringSubmatch(s); m != nil {
		return prRefFrom(m[1], m[2], m[3])
	}
	return PRRef{}, &clierr.CLIError{
		Code:    "github.bad_pr_ref",
		Message: fmt.Sprintf("cannot parse PR ref %q", s),
		Hint:    "use https://github.com/owner/repo/pull/N or owner/repo#N",
		Exit:    2,
	}
}

func prRefFrom(owner, repo, num string) (PRRef, error) {
	n, err := strconv.Atoi(num)
	if err != nil || n <= 0 {
		return PRRef{}, &clierr.CLIError{
			Code:    "github.bad_pr_ref",
			Message: fmt.Sprintf("invalid PR number %q", num),
			Hint:    "the PR number must be a positive integer",
			Exit:    2,
		}
	}
	return PRRef{Owner: owner, Repo: repo, Number: n}, nil
}
