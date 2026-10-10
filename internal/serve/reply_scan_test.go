package serve

import (
	stdctx "context"
	"io"
	"log/slog"
	"testing"
	"time"

	gh "github.com/google/go-github/v84/github"

	mgithub "github.com/vanducng/miu-cr/internal/github"
)

func TestScanThreadRepliesQueuesAuthorizedReply(t *testing.T) {
	disp := &fakeDispatcher{accept: true}
	body := "finding\n<!-- miucr:fp=aaaaaaaaaaaaaaaa -->\n<!-- miu-cr-bot -->"
	client := &replyScanClient{
		login: "miucr",
		review: []*gh.PullRequestComment{
			{ID: gh.Ptr(int64(10)), Body: gh.Ptr(body), User: &gh.User{Login: gh.Ptr("miucr")}},
			{
				ID: gh.Ptr(int64(11)), InReplyTo: gh.Ptr(int64(10)),
				Body: gh.Ptr("Deferred: tracked in #42 because this helper is unused and safe to ship later"),
				User: &gh.User{Login: gh.Ptr("dev")}, AuthorAssociation: gh.Ptr("NONE"),
			},
		},
	}
	r := &HostRunner{disp: disp, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	info := &mgithub.PRInfo{Owner: "acme", Repo: "app", Number: 7, HeadSHA: "abc", AuthorLogin: "dev"}
	r.scanThreadReplies(stdctx.Background(), client, HostRepoConfig{Slug: "acme/app"}, info, "token")
	jobs := disp.submitted()
	if len(jobs) != 1 || jobs[0].Kind != JobKindThreadReply || jobs[0].Reply == nil || jobs[0].Reply.CommentID != 11 {
		t.Fatalf("jobs = %+v", jobs)
	}
	if jobs[0].Ref != "acme/app#7" || jobs[0].Key.CommentID != 11 {
		t.Fatalf("key = %+v ref=%s", jobs[0].Key, jobs[0].Ref)
	}
}

func TestScanThreadRepliesSkipsWithoutTokenOrDuringCooldown(t *testing.T) {
	disp := &fakeDispatcher{accept: true}
	client := &replyScanClient{login: "miucr"}
	r := &HostRunner{disp: disp, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	info := &mgithub.PRInfo{Owner: "acme", Repo: "app", Number: 7, AuthorLogin: "dev"}
	r.scanThreadReplies(stdctx.Background(), client, HostRepoConfig{Slug: "acme/app"}, info, "")
	if len(disp.submitted()) != 0 {
		t.Fatal("empty token queued a reply")
	}
	DeferReplyRetry(ReplyRetryKey("acme/app#7", 11), time.Now().Add(time.Hour))
	client.review = []*gh.PullRequestComment{
		{ID: gh.Ptr(int64(10)), Body: gh.Ptr("<!-- miucr:fp=aaaaaaaaaaaaaaaa -->"), User: &gh.User{Login: gh.Ptr("miucr")}},
		{
			ID: gh.Ptr(int64(11)), InReplyTo: gh.Ptr(int64(10)),
			Body: gh.Ptr("Deferred: tracked in #42 because this helper is unused and safe to ship later"),
			User: &gh.User{Login: gh.Ptr("dev")},
		},
	}
	r.scanThreadReplies(stdctx.Background(), client, HostRepoConfig{Slug: "acme/app"}, info, "token")
	if len(disp.submitted()) != 0 {
		t.Fatal("cooled-down reply was queued")
	}
}

type replyScanClient struct {
	login  string
	review []*gh.PullRequestComment
}

func (c *replyScanClient) CurrentLogin(stdctx.Context) (string, error) { return c.login, nil }
func (c *replyScanClient) ListReviewComments(stdctx.Context, string, string, int, *gh.PullRequestListCommentsOptions) ([]*gh.PullRequestComment, *gh.Response, error) {
	return c.review, &gh.Response{}, nil
}
func (c *replyScanClient) ListIssueComments(stdctx.Context, string, string, int, *gh.IssueListCommentsOptions) ([]*gh.IssueComment, *gh.Response, error) {
	return nil, &gh.Response{}, nil
}
func (c *replyScanClient) GetPR(stdctx.Context, string, string, int) (*gh.PullRequest, error) {
	return nil, nil
}
func (c *replyScanClient) ListFiles(stdctx.Context, string, string, int, *gh.ListOptions) ([]*gh.CommitFile, *gh.Response, error) {
	return nil, nil, nil
}
func (c *replyScanClient) GetCommit(stdctx.Context, string, string, string) (*gh.Commit, error) {
	return nil, nil
}
func (c *replyScanClient) CreateReview(stdctx.Context, string, string, int, *gh.PullRequestReviewRequest) (*gh.PullRequestReview, error) {
	return nil, nil
}
func (c *replyScanClient) ListReviews(stdctx.Context, string, string, int, *gh.ListOptions) ([]*gh.PullRequestReview, *gh.Response, error) {
	return nil, nil, nil
}
func (c *replyScanClient) CreateIssueComment(stdctx.Context, string, string, int, *gh.IssueComment) (*gh.IssueComment, error) {
	return nil, nil
}
func (c *replyScanClient) EditIssueComment(stdctx.Context, string, string, int64, *gh.IssueComment) (*gh.IssueComment, error) {
	return nil, nil
}
func (c *replyScanClient) CreateIssueReaction(stdctx.Context, string, string, int, string) (*gh.Reaction, error) {
	return nil, nil
}
func (c *replyScanClient) CreateCheckRun(stdctx.Context, string, string, gh.CreateCheckRunOptions) (*gh.CheckRun, error) {
	return nil, nil
}
func (c *replyScanClient) UpdateCheckRun(stdctx.Context, string, string, int64, gh.UpdateCheckRunOptions) (*gh.CheckRun, error) {
	return nil, nil
}
func (c *replyScanClient) ListCheckRunsForRef(stdctx.Context, string, string, string, *gh.ListCheckRunsOptions) (*gh.ListCheckRunsResults, *gh.Response, error) {
	return nil, nil, nil
}
func (c *replyScanClient) GetCombinedStatus(stdctx.Context, string, string, string, *gh.ListOptions) (*gh.CombinedStatus, *gh.Response, error) {
	return nil, nil, nil
}
