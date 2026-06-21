package github

import (
	stdctx "context"
	"strings"
	"testing"

	gh "github.com/google/go-github/v84/github"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/diff"
)

// recordClient records write calls and serves canned list pages so the publish
// primitives can be exercised without live network.
type recordClient struct {
	reviewComments [][]*gh.PullRequestComment
	issueComments  [][]*gh.IssueComment
	listRevErr     error
	listIssueErr   error

	createReviewErr error
	createIssueErr  error
	editErr         error

	gotReview     *gh.PullRequestReviewRequest
	createdIssue  *gh.IssueComment
	editedID      int64
	editedBody    string
	createReviewN int
	createIssueN  int
	editN         int
}

func (c *recordClient) GetPR(stdctx.Context, string, string, int) (*gh.PullRequest, error) {
	return nil, nil
}
func (c *recordClient) ListFiles(stdctx.Context, string, string, int, *gh.ListOptions) ([]*gh.CommitFile, *gh.Response, error) {
	return nil, &gh.Response{}, nil
}

func (c *recordClient) CreateReview(_ stdctx.Context, _, _ string, _ int, r *gh.PullRequestReviewRequest) (*gh.PullRequestReview, error) {
	c.createReviewN++
	c.gotReview = r
	return &gh.PullRequestReview{}, c.createReviewErr
}

func (c *recordClient) ListReviewComments(_ stdctx.Context, _, _ string, _ int, opts *gh.PullRequestListCommentsOptions) ([]*gh.PullRequestComment, *gh.Response, error) {
	if c.listRevErr != nil {
		return nil, nil, c.listRevErr
	}
	return pageOf(c.reviewComments, optPage(opts))
}

func (c *recordClient) ListIssueComments(_ stdctx.Context, _, _ string, _ int, opts *gh.IssueListCommentsOptions) ([]*gh.IssueComment, *gh.Response, error) {
	if c.listIssueErr != nil {
		return nil, nil, c.listIssueErr
	}
	idx := 0
	if opts != nil && opts.Page > 0 {
		idx = opts.Page
	}
	resp := &gh.Response{}
	if idx+1 < len(c.issueComments) {
		resp.NextPage = idx + 1
	}
	if idx >= len(c.issueComments) {
		return nil, resp, nil
	}
	return c.issueComments[idx], resp, nil
}

func (c *recordClient) CreateIssueComment(_ stdctx.Context, _, _ string, _ int, com *gh.IssueComment) (*gh.IssueComment, error) {
	c.createIssueN++
	c.createdIssue = com
	return com, c.createIssueErr
}

func (c *recordClient) EditIssueComment(_ stdctx.Context, _, _ string, id int64, com *gh.IssueComment) (*gh.IssueComment, error) {
	c.editN++
	c.editedID = id
	c.editedBody = com.GetBody()
	return com, c.editErr
}

func optPage(opts *gh.PullRequestListCommentsOptions) int {
	if opts != nil && opts.Page > 0 {
		return opts.Page
	}
	return 0
}

func pageOf(pages [][]*gh.PullRequestComment, idx int) ([]*gh.PullRequestComment, *gh.Response, error) {
	resp := &gh.Response{}
	if idx+1 < len(pages) {
		resp.NextPage = idx + 1
	}
	if idx >= len(pages) {
		return nil, resp, nil
	}
	return pages[idx], resp, nil
}

const sampleFileDiff = `@@ -1,3 +1,5 @@
 package p
+var a = 1
+var b = 2
 func f() {}
 func g() {}
`

func sampleDiffs() []diff.Diff {
	return []diff.Diff{{NewPath: "p.go", Diff: sampleFileDiff}}
}

func TestFilterToDiffHunks(t *testing.T) {
	diffs := sampleDiffs()
	// new-side lines for the hunk above: 1 (context), 2 (added), 3 (added), 4 (context), 5 (context).
	findings := []engine.Finding{
		{File: "p.go", Line: 2, Category: "bug"},   // added → kept
		{File: "p.go", Line: 4, Category: "style"}, // context → kept
		{File: "p.go", Line: 99, Category: "x"},    // out of hunk → dropped
		{File: "p.go", Line: 0, Category: "drift"}, // Line==0 → dropped
		{File: "other.go", Line: 2, Category: "x"}, // wrong file → dropped
	}
	got := filterToDiffHunks(findings, diffs)
	if len(got) != 2 {
		t.Fatalf("want 2 kept, got %d: %+v", len(got), got)
	}
	if got[0].Line != 2 || got[1].Line != 4 {
		t.Fatalf("kept wrong findings: %+v", got)
	}
}

func TestFilterToDiffHunksRenamed(t *testing.T) {
	diffs := []diff.Diff{{OldPath: "old.go", NewPath: "new.go", IsRenamed: true, Diff: sampleFileDiff}}
	findings := []engine.Finding{
		{File: "new.go", Line: 2},
		{File: "old.go", Line: 2}, // filter keys on new-side path only
	}
	got := filterToDiffHunks(findings, diffs)
	if len(got) != 1 || got[0].File != "new.go" {
		t.Fatalf("renamed file must anchor on new path; got %+v", got)
	}
}

func TestPostReviewShape(t *testing.T) {
	c := &recordClient{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 7, HeadSHA: "headsha"}
	findings := []engine.Finding{
		{File: "p.go", Line: 2, Severity: "high", Category: "bug", Rationale: "boom"},
		{File: "p.go", Line: 99, Rationale: "out of hunk"}, // dropped by filter
	}
	n, err := PostReview(stdctx.Background(), c, info, findings, sampleDiffs(), "summary body", nil)
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if n != 1 {
		t.Fatalf("want 1 inline comment posted, got %d", n)
	}
	r := c.gotReview
	if r == nil {
		t.Fatal("CreateReview not called")
	}
	if r.GetCommitID() != "headsha" {
		t.Errorf("CommitID = %q, want head SHA", r.GetCommitID())
	}
	if r.GetEvent() != "COMMENT" {
		t.Errorf("Event = %q, want COMMENT", r.GetEvent())
	}
	if len(r.Comments) != 1 {
		t.Fatalf("want 1 comment, got %d", len(r.Comments))
	}
	dc := r.Comments[0]
	if dc.Position != nil {
		t.Error("Position must never be set (comfort-fade Line/Side only)")
	}
	if dc.GetSide() != "RIGHT" {
		t.Errorf("Side = %q, want RIGHT", dc.GetSide())
	}
	if dc.GetLine() != 2 {
		t.Errorf("Line = %d, want 2", dc.GetLine())
	}
	if !strings.Contains(dc.GetBody(), "miucr:fp=") {
		t.Error("inline body must carry the hidden fingerprint marker")
	}
}

func TestPostReviewSkipsExistingFingerprints(t *testing.T) {
	c := &recordClient{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"}
	f := engine.Finding{File: "p.go", Line: 2, Category: "bug", Rationale: "dup"}
	fp := fingerprint(f)

	n, err := PostReview(stdctx.Background(), c, info, []engine.Finding{f}, sampleDiffs(), "", map[string]bool{fp: true})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if n != 0 {
		t.Fatalf("already-posted fingerprint must be skipped, got %d posted", n)
	}
	if c.createReviewN != 0 {
		t.Errorf("no review should be created when nothing to post, got %d calls", c.createReviewN)
	}
}

func TestExistingFingerprints(t *testing.T) {
	c := &recordClient{
		reviewComments: [][]*gh.PullRequestComment{
			{{Body: gh.Ptr("note\n\n<!-- miucr:fp=00112233aabbccdd -->")}},
			{{Body: gh.Ptr("plain comment, no marker")}},
		},
	}
	got, err := ExistingFingerprints(stdctx.Background(), c, &PRInfo{Owner: "o", Repo: "r", Number: 1})
	if err != nil {
		t.Fatalf("ExistingFingerprints: %v", err)
	}
	if !got["00112233aabbccdd"] {
		t.Fatalf("want fp extracted from review comments, got %+v", got)
	}
	if len(got) != 1 {
		t.Fatalf("want exactly 1 fp, got %d", len(got))
	}
}

func TestUpsertSummaryCommentCreatesWhenAbsent(t *testing.T) {
	c := &recordClient{
		issueComments: [][]*gh.IssueComment{
			{{ID: gh.Ptr(int64(1)), Body: gh.Ptr("unrelated comment")}},
		},
	}
	action, err := UpsertSummaryComment(stdctx.Background(), c, &PRInfo{Owner: "o", Repo: "r", Number: 1}, "the summary")
	if err != nil {
		t.Fatalf("UpsertSummaryComment: %v", err)
	}
	if action != "created" {
		t.Fatalf("want created, got %q", action)
	}
	if c.createIssueN != 1 || c.editN != 0 {
		t.Fatalf("want 1 create / 0 edit, got %d/%d", c.createIssueN, c.editN)
	}
	if !strings.HasPrefix(c.createdIssue.GetBody(), SummarySentinel) {
		t.Error("created summary must start with the sentinel")
	}
}

func TestUpsertSummaryCommentEditsWhenPresent(t *testing.T) {
	c := &recordClient{
		issueComments: [][]*gh.IssueComment{
			{{ID: gh.Ptr(int64(9)), Body: gh.Ptr(SummarySentinel + "\nold body")}},
		},
	}
	action, err := UpsertSummaryComment(stdctx.Background(), c, &PRInfo{Owner: "o", Repo: "r", Number: 1}, "new body")
	if err != nil {
		t.Fatalf("UpsertSummaryComment: %v", err)
	}
	if action != "edited" {
		t.Fatalf("want edited, got %q", action)
	}
	if c.editN != 1 || c.createIssueN != 0 {
		t.Fatalf("want 1 edit / 0 create, got %d/%d", c.editN, c.createIssueN)
	}
	if c.editedID != 9 {
		t.Errorf("edited wrong comment id %d, want 9", c.editedID)
	}
	if !strings.HasPrefix(c.editedBody, SummarySentinel) {
		t.Error("edited summary must keep the sentinel")
	}
}

func TestPostReviewRateLimitMapped(t *testing.T) {
	c := &recordClient{createReviewErr: &gh.RateLimitError{Message: "rate limited"}}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"}
	findings := []engine.Finding{{File: "p.go", Line: 2, Rationale: "x"}}
	_, err := PostReview(stdctx.Background(), c, info, findings, sampleDiffs(), "", nil)
	if err == nil {
		t.Fatal("want rate-limit error")
	}
	var ce *clierr.CLIError
	if !asCLIErr(err, &ce) || ce.Code != "github.rate_limited" {
		t.Fatalf("want github.rate_limited, got %v", err)
	}
	if !ce.Retry {
		t.Error("rate-limit error must be retryable")
	}
}

func TestFingerprintStable(t *testing.T) {
	f := engine.Finding{File: "p.go", Line: 2, Category: "bug", Rationale: "same"}
	if fingerprint(f) != fingerprint(f) {
		t.Fatal("fingerprint must be deterministic")
	}
	g := f
	g.Line = 3
	if fingerprint(f) == fingerprint(g) {
		t.Fatal("different line must yield a different fingerprint")
	}
}
