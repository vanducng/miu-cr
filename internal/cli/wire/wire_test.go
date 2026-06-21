package wire

import (
	stdctx "context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gh "github.com/google/go-github/v84/github"

	"github.com/vanducng/miu-cr/internal/cli"
	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
	mgithub "github.com/vanducng/miu-cr/internal/github"
)

// fakeGitHub persists posted review/issue comments across runs and records the
// call sequence so the publish flow's ordering (inline review before summary
// upsert) and cross-run dedupe can be asserted without live network.
type fakeGitHub struct {
	reviewComments []*gh.PullRequestComment
	issueComments  []*gh.IssueComment
	nextID         int64
	order          []string

	createReviewN int
	createIssueN  int
	editN         int
}

func (f *fakeGitHub) GetPR(stdctx.Context, string, string, int) (*gh.PullRequest, error) {
	return nil, nil
}
func (f *fakeGitHub) ListFiles(stdctx.Context, string, string, int, *gh.ListOptions) ([]*gh.CommitFile, *gh.Response, error) {
	return nil, &gh.Response{}, nil
}

func (f *fakeGitHub) CreateReview(_ stdctx.Context, _, _ string, _ int, r *gh.PullRequestReviewRequest) (*gh.PullRequestReview, error) {
	f.order = append(f.order, "create_review")
	f.createReviewN++
	for _, dc := range r.Comments {
		f.nextID++
		f.reviewComments = append(f.reviewComments, &gh.PullRequestComment{
			ID:   gh.Ptr(f.nextID),
			Body: gh.Ptr(dc.GetBody()),
		})
	}
	return &gh.PullRequestReview{}, nil
}

func (f *fakeGitHub) ListReviewComments(stdctx.Context, string, string, int, *gh.PullRequestListCommentsOptions) ([]*gh.PullRequestComment, *gh.Response, error) {
	f.order = append(f.order, "list_review")
	return f.reviewComments, &gh.Response{}, nil
}

func (f *fakeGitHub) ListIssueComments(stdctx.Context, string, string, int, *gh.IssueListCommentsOptions) ([]*gh.IssueComment, *gh.Response, error) {
	f.order = append(f.order, "list_issue")
	return f.issueComments, &gh.Response{}, nil
}

func (f *fakeGitHub) CreateIssueComment(_ stdctx.Context, _, _ string, _ int, com *gh.IssueComment) (*gh.IssueComment, error) {
	f.order = append(f.order, "create_issue")
	f.createIssueN++
	f.nextID++
	saved := &gh.IssueComment{ID: gh.Ptr(f.nextID), Body: gh.Ptr(com.GetBody())}
	f.issueComments = append(f.issueComments, saved)
	return saved, nil
}

func (f *fakeGitHub) EditIssueComment(_ stdctx.Context, _, _ string, id int64, com *gh.IssueComment) (*gh.IssueComment, error) {
	f.order = append(f.order, "edit_issue")
	f.editN++
	for _, ic := range f.issueComments {
		if ic.GetID() == id {
			ic.Body = gh.Ptr(com.GetBody())
		}
	}
	return com, nil
}

// setupRepo builds a real two-commit repo (base→head) the publish flow's
// DiffsForPR can diff via ModeRange, returning the dir and both SHAs.
func setupRepo(t *testing.T, runner *gitcmd.Runner) (string, string, string) {
	t.Helper()
	ctx := stdctx.Background()
	dir := t.TempDir()
	run := func(args ...string) {
		if _, err := runner.Output(ctx, dir, args...); err != nil {
			t.Fatalf("git %s: %v", strings.Join(args, " "), err)
		}
	}
	write := func(content string) {
		if err := os.WriteFile(filepath.Join(dir, "foo.go"), []byte(content), 0o644); err != nil {
			t.Fatalf("write foo.go: %v", err)
		}
	}

	run("init", "--quiet")
	run("config", "user.email", "t@t.test")
	run("config", "user.name", "tester")
	run("config", "commit.gpgsign", "false")

	write("package foo\n\nfunc A() {}\n")
	run("add", ".")
	run("commit", "--quiet", "-m", "base")
	base, err := runner.HeadSHA(ctx, dir)
	if err != nil {
		t.Fatalf("base HeadSHA: %v", err)
	}

	write("package foo\n\nfunc A() {}\nfunc B() {}\n")
	run("add", ".")
	run("commit", "--quiet", "-m", "head")
	head, err := runner.HeadSHA(ctx, dir)
	if err != nil {
		t.Fatalf("head HeadSHA: %v", err)
	}
	return dir, base, head
}

func TestPublishReviewWireFlow(t *testing.T) {
	runner := gitcmd.New()
	dir, base, head := setupRepo(t, runner)

	fake := &fakeGitHub{}
	// Exercise the production seam: ReviewPR resolves the client through this var.
	restore := newGitHubClient
	newGitHubClient = func(string) mgithub.Client { return fake }
	t.Cleanup(func() { newGitHubClient = restore })
	client := newGitHubClient("")

	info := &mgithub.PRInfo{Owner: "o", Repo: "r", Number: 7, HeadSHA: head, BaseSHA: base, BaseBranch: "main"}
	res := engine.ReviewResult{
		Findings: []engine.Finding{
			// Anchored to the added line (new-side line 4) so it lands in a diff hunk.
			{File: "foo.go", Line: 4, Severity: "high", Category: "bug", Rationale: "boom", QuotedCode: "func B() {}"},
		},
		Stats: map[string]any{"truncation_level": "full", "files_reviewed": float64(1)},
	}

	// First run: one inline posted + summary created.
	pr := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, res, pr); err != nil {
		t.Fatalf("publishReview: %v", err)
	}
	if pr.PostedInline != 1 {
		t.Fatalf("first run: want 1 inline posted, got %d", pr.PostedInline)
	}
	if pr.SummaryAction != "created" {
		t.Fatalf("first run: want summary created, got %q", pr.SummaryAction)
	}
	ci, ri := indexOf(fake.order, "create_issue"), indexOf(fake.order, "create_review")
	if ri < 0 || ci < 0 || ri > ci {
		t.Fatalf("inline review must post BEFORE the summary; order=%v", fake.order)
	}
	if le := indexOf(fake.order, "list_review"); le < 0 || le > ri {
		t.Fatalf("ExistingFingerprints (list_review) must run before the review; order=%v", fake.order)
	}

	// Re-run: 0 new inline (fingerprint skip), summary edited (not duplicated).
	fake.order = nil
	pr2 := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, res, pr2); err != nil {
		t.Fatalf("publishReview re-run: %v", err)
	}
	if pr2.PostedInline != 0 {
		t.Fatalf("re-run: want 0 new inline, got %d", pr2.PostedInline)
	}
	if pr2.SummaryAction != "edited" {
		t.Fatalf("re-run: want summary edited, got %q", pr2.SummaryAction)
	}
	if fake.createReviewN != 1 {
		t.Errorf("re-run must not create a second review, createReviewN=%d", fake.createReviewN)
	}
	if fake.createIssueN != 1 || fake.editN != 1 {
		t.Errorf("re-run must edit (not re-create) the summary: create=%d edit=%d", fake.createIssueN, fake.editN)
	}
	if len(fake.reviewComments) != 1 {
		t.Errorf("re-run must not duplicate inline comments, have %d", len(fake.reviewComments))
	}
	if got := strings.Count(fake.issueComments[0].GetBody(), mgithub.SummarySentinel); got != 1 {
		t.Errorf("final summary must carry exactly one sentinel, got %d", got)
	}
}

func indexOf(s []string, want string) int {
	for i, v := range s {
		if v == want {
			return i
		}
	}
	return -1
}
