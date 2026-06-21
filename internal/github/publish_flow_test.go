package github

import (
	stdctx "context"
	"strings"
	"testing"

	gh "github.com/google/go-github/v84/github"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/diff"
)

// statefulClient persists posted inline review comments and issue comments so a
// second run sees the first run's state — exercising cross-run dedupe (inline
// fingerprint skip) and sentinel summary upsert (create then edit).
type statefulClient struct {
	reviewComments []*gh.PullRequestComment
	issueComments  []*gh.IssueComment
	nextID         int64

	createReviewN int
	createIssueN  int
	editN         int
}

func (c *statefulClient) GetPR(stdctx.Context, string, string, int) (*gh.PullRequest, error) {
	return nil, nil
}
func (c *statefulClient) ListFiles(stdctx.Context, string, string, int, *gh.ListOptions) ([]*gh.CommitFile, *gh.Response, error) {
	return nil, &gh.Response{}, nil
}

func (c *statefulClient) CreateReview(_ stdctx.Context, _, _ string, _ int, r *gh.PullRequestReviewRequest) (*gh.PullRequestReview, error) {
	c.createReviewN++
	for _, dc := range r.Comments {
		c.nextID++
		c.reviewComments = append(c.reviewComments, &gh.PullRequestComment{
			ID:   gh.Ptr(c.nextID),
			Body: gh.Ptr(dc.GetBody()),
		})
	}
	return &gh.PullRequestReview{}, nil
}

func (c *statefulClient) ListReviewComments(_ stdctx.Context, _, _ string, _ int, _ *gh.PullRequestListCommentsOptions) ([]*gh.PullRequestComment, *gh.Response, error) {
	return c.reviewComments, &gh.Response{}, nil
}

func (c *statefulClient) ListIssueComments(_ stdctx.Context, _, _ string, _ int, _ *gh.IssueListCommentsOptions) ([]*gh.IssueComment, *gh.Response, error) {
	return c.issueComments, &gh.Response{}, nil
}

func (c *statefulClient) CreateIssueComment(_ stdctx.Context, _, _ string, _ int, com *gh.IssueComment) (*gh.IssueComment, error) {
	c.createIssueN++
	c.nextID++
	saved := &gh.IssueComment{ID: gh.Ptr(c.nextID), Body: gh.Ptr(com.GetBody())}
	c.issueComments = append(c.issueComments, saved)
	return saved, nil
}

func (c *statefulClient) EditIssueComment(_ stdctx.Context, _, _ string, id int64, com *gh.IssueComment) (*gh.IssueComment, error) {
	c.editN++
	for _, ic := range c.issueComments {
		if ic.GetID() == id {
			ic.Body = gh.Ptr(com.GetBody())
		}
	}
	return com, nil
}

// runPublishWithDiffs mirrors wire.publishReview's order (existing fps → inline
// → summary last) without the engine, so the flow's cross-run behavior is
// exercised here.
func runPublishWithDiffs(t *testing.T, c Client, info *PRInfo, findings []engine.Finding, diffs []diff.Diff) (int, string) {
	t.Helper()
	ctx := stdctx.Background()
	existing, err := ExistingFingerprints(ctx, c, info)
	if err != nil {
		t.Fatalf("ExistingFingerprints: %v", err)
	}
	posted, err := PostReview(ctx, c, info, findings, diffs, "", existing)
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	action, err := UpsertSummaryComment(ctx, c, info, RenderSummary(info, findings, nil))
	if err != nil {
		t.Fatalf("UpsertSummaryComment: %v", err)
	}
	return posted, action
}

func TestPublishFlowPostThenRerun(t *testing.T) {
	c := &statefulClient{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 7, HeadSHA: "headsha"}
	findings := []engine.Finding{
		{File: "p.go", Line: 2, Severity: "high", Category: "bug", Rationale: "boom"},
		{File: "p.go", Line: 4, Severity: "low", Category: "style", Rationale: "nit"},
		{File: "p.go", Line: 99, Rationale: "out of hunk"}, // dropped by filter
	}
	diffs := sampleDiffs()

	// First run: 2 in-hunk inline + a created summary.
	posted, action := runPublishWithDiffs(t, c, info, findings, diffs)
	if posted != 2 {
		t.Fatalf("first run: want 2 inline posted, got %d", posted)
	}
	if action != "created" {
		t.Fatalf("first run: want summary created, got %q", action)
	}
	if c.createReviewN != 1 || c.createIssueN != 1 || c.editN != 0 {
		t.Fatalf("first run calls: review=%d create=%d edit=%d", c.createReviewN, c.createIssueN, c.editN)
	}

	// Second run: same findings → 0 new inline (fp skip), summary edited.
	posted, action = runPublishWithDiffs(t, c, info, findings, diffs)
	if posted != 0 {
		t.Fatalf("re-run: want 0 new inline (dedupe), got %d", posted)
	}
	if action != "edited" {
		t.Fatalf("re-run: want summary edited, got %q", action)
	}
	if c.createIssueN != 1 {
		t.Errorf("re-run must not create a second summary, create=%d", c.createIssueN)
	}
	if c.editN != 1 {
		t.Errorf("re-run must edit the summary once, edit=%d", c.editN)
	}
	if len(c.reviewComments) != 2 {
		t.Errorf("re-run must not duplicate inline comments, have %d", len(c.reviewComments))
	}
}

func TestRenderSummaryShape(t *testing.T) {
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "deadbeef", IsFork: true}
	findings := []engine.Finding{
		{Severity: "high"}, {Severity: "high"}, {Severity: "low"}, {Severity: ""},
	}
	stats := map[string]any{"truncation_level": "hunks", "files_reviewed": float64(3)}
	out := RenderSummary(info, findings, stats)

	if !strings.HasPrefix(out, SummarySentinel) {
		t.Fatalf("summary must start with the sentinel: %q", out[:min(40, len(out))])
	}
	for _, want := range []string{"high: 2", "low: 1", "info: 1", "deadbeef", "hunks", "Files reviewed: 3", "fork"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
}

func TestRenderSummaryNoFindings(t *testing.T) {
	info := &PRInfo{HeadSHA: "abc"}
	out := RenderSummary(info, nil, nil)
	if !strings.HasPrefix(out, SummarySentinel) {
		t.Fatal("must start with sentinel")
	}
	if !strings.Contains(out, "No findings") {
		t.Errorf("want No findings:\n%s", out)
	}
	if !strings.Contains(out, "full") {
		t.Errorf("want default truncation full:\n%s", out)
	}
}
