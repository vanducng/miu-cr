package github

import (
	stdctx "context"
	"strings"
	"testing"

	gh "github.com/google/go-github/v84/github"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/diff"
)

// statefulClient persists posted inline review comments and reviews so a second
// run sees the first run's state — exercising cross-run inline-fingerprint dedupe
// and the Codex per-commit review body.
type statefulClient struct {
	reviewComments []*gh.PullRequestComment
	reviews        []*gh.PullRequestReview
	nextID         int64

	createReviewN int
}

func (c *statefulClient) GetPR(stdctx.Context, string, string, int) (*gh.PullRequest, error) {
	return &gh.PullRequest{Head: &gh.PullRequestBranch{SHA: gh.Ptr("headsha")}}, nil
}

func (c *statefulClient) ListReviews(stdctx.Context, string, string, int, *gh.ListOptions) ([]*gh.PullRequestReview, *gh.Response, error) {
	return c.reviews, &gh.Response{}, nil
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
	c.reviews = append(c.reviews, &gh.PullRequestReview{
		CommitID: r.CommitID,
		Body:     r.Body,
	})
	return &gh.PullRequestReview{}, nil
}

func (c *statefulClient) ListReviewComments(_ stdctx.Context, _, _ string, _ int, _ *gh.PullRequestListCommentsOptions) ([]*gh.PullRequestComment, *gh.Response, error) {
	return c.reviewComments, &gh.Response{}, nil
}

func (c *statefulClient) ListIssueComments(_ stdctx.Context, _, _ string, _ int, _ *gh.IssueListCommentsOptions) ([]*gh.IssueComment, *gh.Response, error) {
	return nil, &gh.Response{}, nil
}

func (c *statefulClient) CreateIssueComment(stdctx.Context, string, string, int, *gh.IssueComment) (*gh.IssueComment, error) {
	return nil, nil
}

func (c *statefulClient) EditIssueComment(stdctx.Context, string, string, int64, *gh.IssueComment) (*gh.IssueComment, error) {
	return nil, nil
}

func (c *statefulClient) CreateCheckRun(stdctx.Context, string, string, gh.CreateCheckRunOptions) (*gh.CheckRun, error) {
	return &gh.CheckRun{ID: gh.Ptr(int64(1))}, nil
}
func (c *statefulClient) UpdateCheckRun(stdctx.Context, string, string, int64, gh.UpdateCheckRunOptions) (*gh.CheckRun, error) {
	return &gh.CheckRun{ID: gh.Ptr(int64(1))}, nil
}
func (c *statefulClient) ListCheckRunsForRef(stdctx.Context, string, string, string, *gh.ListCheckRunsOptions) (*gh.ListCheckRunsResults, *gh.Response, error) {
	return &gh.ListCheckRunsResults{}, &gh.Response{}, nil
}

// runPublishWithDiffs mirrors wire.publishReview's Codex flow (existing fps →
// inline + summary as the review BODY) without the engine, so the cross-run
// behavior is exercised here. It returns inline-posted count + whether a body
// was set on the submitted review.
func runPublishWithDiffs(t *testing.T, c Client, info *PRInfo, findings []engine.Finding, diffs []diff.Diff) (int, bool) {
	t.Helper()
	ctx := stdctx.Background()
	existing, err := ExistingFingerprints(ctx, c, info)
	if err != nil {
		t.Fatalf("ExistingFingerprints: %v", err)
	}
	summaryFn := func(omitted int, _ []engine.Finding) string {
		return RenderSummary(info, findings, nil, omitted)
	}
	res, err := PostReview(ctx, c, info, findings, diffs, summaryFn, existing, PostReviewOptions{})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	body := ""
	if sc, ok := c.(*statefulClient); ok && len(sc.reviews) > 0 {
		body = sc.reviews[len(sc.reviews)-1].GetBody()
	}
	return res.Posted, body != ""
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

	// First run: 2 in-hunk inline nested under a review carrying the summary body.
	posted, hasBody := runPublishWithDiffs(t, c, info, findings, diffs)
	if posted != 2 {
		t.Fatalf("first run: want 2 inline posted, got %d", posted)
	}
	if !hasBody {
		t.Fatalf("first run: review must carry the summary body")
	}
	if c.createReviewN != 1 {
		t.Fatalf("first run: want 1 CreateReview, got %d", c.createReviewN)
	}

	// Second run (same SHA, no wire-layer skip here): inline dedupe still drops the
	// 2 already-posted comments; the review body carries the summary again. The
	// duplicate-review guard lives at the wire layer (AlreadyPostedAtSHA), not here.
	posted, hasBody = runPublishWithDiffs(t, c, info, findings, diffs)
	if posted != 0 {
		t.Fatalf("re-run: want 0 new inline (dedupe), got %d", posted)
	}
	if !hasBody {
		t.Fatalf("re-run: review still carries the summary body")
	}
	if len(c.reviewComments) != 2 {
		t.Errorf("re-run must not duplicate inline comments, have %d", len(c.reviewComments))
	}
	// The submitted review body carries exactly one marker.
	last := c.reviews[len(c.reviews)-1].GetBody()
	if got := strings.Count(last, ReviewMarker); got != 1 {
		t.Errorf("review body must carry exactly one marker, got %d:\n%s", got, last)
	}
}

func TestRenderSummaryShape(t *testing.T) {
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "deadbeef", IsFork: true}
	findings := []engine.Finding{
		{Severity: "high"}, {Severity: "high"}, {Severity: "low"}, {Severity: ""},
	}
	stats := map[string]any{"truncation_level": "hunks", "files_reviewed": float64(3)}
	out := RenderSummary(info, findings, stats, 0)

	if !strings.HasPrefix(out, ReviewMarker) {
		t.Fatalf("summary body must lead with the review marker: %q", out[:min(40, len(out))])
	}
	if !strings.Contains(out, "## Code Review") {
		t.Fatalf("summary must contain the heading:\n%s", out)
	}
	// Header chips high-first (empty severity folds to ⚪), finding count, the
	// reviewed-commit lead-in, quote-line context + files-reviewed fallback (no
	// diffs), fork note, footer SHA.
	for _, want := range []string{shieldsCount("P1", 2, "orange"), shieldsCount("P3", 1, "blue"), "4 findings", "Reviewed commit: `deadbeef`", "context hunks", "3 files", "fork"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
}

func TestRenderSummaryNoFindings(t *testing.T) {
	info := &PRInfo{HeadSHA: "abc"}
	out := RenderSummary(info, nil, nil, 0)
	if !strings.HasPrefix(out, ReviewMarker) {
		t.Fatal("body must lead with the review marker")
	}
	if !strings.Contains(out, "✅ no findings") {
		t.Errorf("want the no-findings header:\n%s", out)
	}
	if !strings.Contains(out, "context full") {
		t.Errorf("want default truncation full:\n%s", out)
	}
}

func TestRenderSummaryOmittedInlineNote(t *testing.T) {
	info := &PRInfo{HeadSHA: "abc"}
	out := RenderSummary(info, []engine.Finding{{Severity: "high"}}, nil, 5)
	if !strings.Contains(out, "Omitted inline: 5") {
		t.Errorf("summary must note omitted inline count:\n%s", out)
	}
	if zero := RenderSummary(info, nil, nil, 0); strings.Contains(zero, "Omitted inline") {
		t.Errorf("no omitted note when omittedInline==0:\n%s", zero)
	}
}
