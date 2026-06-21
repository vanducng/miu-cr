package github

import (
	stdctx "context"
	"fmt"
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
	reviews        []*gh.PullRequestReview
	listRevErr     error
	listIssueErr   error
	listReviewsErr error

	headSHA string // GetPR returns this head SHA; empty means "headsha"

	createReviewErr  error
	createReviewErrN error // returned only on the Nth (1-based) CreateReview call
	createIssueErr   error
	editErr          error

	gotReview     *gh.PullRequestReviewRequest
	gotReviews    []*gh.PullRequestReviewRequest
	createdIssue  *gh.IssueComment
	editedID      int64
	editedBody    string
	createReviewN int
	createIssueN  int
	editN         int
}

func (c *recordClient) GetPR(stdctx.Context, string, string, int) (*gh.PullRequest, error) {
	sha := c.headSHA
	if sha == "" {
		sha = "headsha"
	}
	return &gh.PullRequest{Head: &gh.PullRequestBranch{SHA: gh.Ptr(sha)}}, nil
}
func (c *recordClient) ListFiles(stdctx.Context, string, string, int, *gh.ListOptions) ([]*gh.CommitFile, *gh.Response, error) {
	return nil, &gh.Response{}, nil
}

func (c *recordClient) CreateReview(_ stdctx.Context, _, _ string, _ int, r *gh.PullRequestReviewRequest) (*gh.PullRequestReview, error) {
	c.createReviewN++
	c.gotReview = r
	c.gotReviews = append(c.gotReviews, r)
	if c.createReviewErrN != nil && c.createReviewN == 1 {
		return nil, c.createReviewErrN
	}
	return &gh.PullRequestReview{}, c.createReviewErr
}

func (c *recordClient) ListReviews(_ stdctx.Context, _, _ string, _ int, _ *gh.ListOptions) ([]*gh.PullRequestReview, *gh.Response, error) {
	if c.listReviewsErr != nil {
		return nil, nil, c.listReviewsErr
	}
	return c.reviews, &gh.Response{}, nil
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
	res, err := PostReview(stdctx.Background(), c, info, findings, sampleDiffs(), "summary body", nil, PostReviewOptions{})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if res.Posted != 1 {
		t.Fatalf("want 1 inline comment posted, got %d", res.Posted)
	}
	if res.Omitted != 0 {
		t.Fatalf("want 0 omitted under the cap, got %d", res.Omitted)
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

	res, err := PostReview(stdctx.Background(), c, info, []engine.Finding{f}, sampleDiffs(), "", map[string]bool{fp: true}, PostReviewOptions{})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if res.Posted != 0 {
		t.Fatalf("already-posted fingerprint must be skipped, got %d posted", res.Posted)
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
	_, err := PostReview(stdctx.Background(), c, info, findings, sampleDiffs(), "", nil, PostReviewOptions{})
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

func TestPostReviewCapsInlineComments(t *testing.T) {
	c := &recordClient{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"}

	const n = 45 // > maxInlineComments (40)
	var sb strings.Builder
	fmt.Fprintf(&sb, "@@ -1,1 +1,%d @@\n package p\n", n+1)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, "+line %d\n", i+1)
	}
	diffs := []diff.Diff{{NewPath: "big.go", Diff: sb.String()}}

	findings := make([]engine.Finding, 0, n)
	for i := 0; i < n; i++ {
		findings = append(findings, engine.Finding{
			File: "big.go", Line: i + 2, Severity: "high", Category: "bug",
			Rationale: fmt.Sprintf("issue %d", i),
		})
	}

	res, err := PostReview(stdctx.Background(), c, info, findings, diffs, "", nil, PostReviewOptions{})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	posted, omitted := res.Posted, res.Omitted
	if posted != maxInlineComments {
		t.Fatalf("inline must be capped at %d, got %d", maxInlineComments, posted)
	}
	if omitted != n-maxInlineComments {
		t.Fatalf("want %d omitted, got %d", n-maxInlineComments, omitted)
	}
	if c.gotReview == nil || len(c.gotReview.Comments) != maxInlineComments {
		t.Fatalf("review must carry exactly %d inline comments", maxInlineComments)
	}
	summary := RenderSummary(info, findings, nil, omitted)
	if !strings.Contains(summary, fmt.Sprintf("Omitted inline: %d", omitted)) {
		t.Errorf("summary must note the omitted count:\n%s", summary)
	}
}

func TestCommentBodyEscapesEmbeddedFence(t *testing.T) {
	f := engine.Finding{
		Severity:       "high",
		Category:       "bug",
		Rationale:      "embeds a fence",
		SuggestedPatch: "before\n```\nafter",
	}
	// Suggest OFF and multi-line patch → plain hint, never a one-click suggestion.
	body := commentBody(f, "", PostReviewOptions{})
	if strings.Contains(body, "suggestion") {
		t.Errorf("must NOT emit a one-click suggestion fence (latent M2 bug):\n%s", body)
	}
	if !strings.Contains(body, "````go") {
		t.Errorf("want a 4-backtick hint fence so the embedded ``` cannot terminate it early:\n%s", body)
	}
	if !strings.Contains(body, "before\n```\nafter") {
		t.Errorf("patch content must survive intact:\n%s", body)
	}
	if c := strings.Count(body, "````"); c != 2 {
		t.Errorf("want exactly one opening + one closing 4-backtick fence, got %d:\n%s", c, body)
	}
}

// suggestDiff carries a 3-line new-file body anchored by a hunk so findings on
// lines 1..3 survive filterToDiffHunks; line 2 is the candidate for replacement.
func suggestDiff() []diff.Diff {
	d := `@@ -1,3 +1,3 @@
 package p
-var a = 0
+var a = 1
 func f() {}
`
	return []diff.Diff{{
		NewPath:        "p.go",
		Diff:           d,
		NewFileContent: "package p\nvar a = 1\nfunc f() {}\n",
	}}
}

func suggestFinding() engine.Finding {
	return engine.Finding{
		File:           "p.go",
		Line:           2,
		Severity:       "high",
		Category:       "bug",
		Rationale:      "use a constant",
		QuotedCode:     "var a = 1",
		SuggestedPatch: "var a = 2",
	}
}

func postedBody(t *testing.T, c *recordClient) string {
	t.Helper()
	if c.gotReview == nil || len(c.gotReview.Comments) != 1 {
		t.Fatalf("want exactly 1 inline comment posted, got review=%+v", c.gotReview)
	}
	if c.gotReview.Comments[0].StartLine != nil {
		t.Fatal("StartLine must never be set (multi-line is out)")
	}
	if c.gotReview.Comments[0].StartSide != nil {
		t.Fatal("StartSide must never be set (multi-line is out)")
	}
	return c.gotReview.Comments[0].GetBody()
}

func TestSuggestEmitsNativeSuggestionForCleanSingleLine(t *testing.T) {
	c := &recordClient{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"}
	_, err := PostReview(stdctx.Background(), c, info, []engine.Finding{suggestFinding()}, suggestDiff(), "", nil, PostReviewOptions{Suggest: true})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	body := postedBody(t, c)
	if !strings.Contains(body, "```suggestion\nvar a = 2\n```") {
		t.Errorf("want a native single-line suggestion:\n%s", body)
	}
}

func TestSuggestOffNeverEmitsSuggestion(t *testing.T) {
	c := &recordClient{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"}
	_, err := PostReview(stdctx.Background(), c, info, []engine.Finding{suggestFinding()}, suggestDiff(), "", nil, PostReviewOptions{})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	body := postedBody(t, c)
	if strings.Contains(body, "suggestion") {
		t.Errorf("Suggest OFF must never emit a suggestion fence:\n%s", body)
	}
	if !strings.Contains(body, "var a = 2") {
		t.Errorf("patch must still appear as a hint:\n%s", body)
	}
}

func TestSuggestMultiLineDegradesToHint(t *testing.T) {
	c := &recordClient{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"}
	f := suggestFinding()
	f.EndLine = 3 // multi-line range → always hint
	_, err := PostReview(stdctx.Background(), c, info, []engine.Finding{f}, suggestDiff(), "", nil, PostReviewOptions{Suggest: true})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	body := postedBody(t, c)
	if strings.Contains(body, "suggestion") {
		t.Errorf("multi-line finding must degrade to a hint:\n%s", body)
	}
}

func TestSuggestMultiLinePatchDegradesToHint(t *testing.T) {
	c := &recordClient{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"}
	f := suggestFinding()
	f.SuggestedPatch = "var a = 2\nvar c = 3" // patch spans 2 lines → not a clean single-line replace
	_, err := PostReview(stdctx.Background(), c, info, []engine.Finding{f}, suggestDiff(), "", nil, PostReviewOptions{Suggest: true})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if strings.Contains(postedBody(t, c), "suggestion") {
		t.Error("a multi-line patch must degrade to a hint")
	}
}

func TestSuggestNoOpDegradesToHint(t *testing.T) {
	c := &recordClient{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"}
	f := suggestFinding()
	f.SuggestedPatch = "var a = 1" // identical to raw line → no-op
	_, err := PostReview(stdctx.Background(), c, info, []engine.Finding{f}, suggestDiff(), "", nil, PostReviewOptions{Suggest: true})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if strings.Contains(postedBody(t, c), "suggestion") {
		t.Error("a no-op replacement must degrade to a hint")
	}
}

func TestSuggestOldSideAnchoredDegradesToHint(t *testing.T) {
	c := &recordClient{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"}
	f := suggestFinding()
	// Anchor fell back to old side: QuotedCode does NOT match raw NewFileContent[Line].
	f.QuotedCode = "var a = 0"
	_, err := PostReview(stdctx.Background(), c, info, []engine.Finding{f}, suggestDiff(), "", nil, PostReviewOptions{Suggest: true})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if strings.Contains(postedBody(t, c), "suggestion") {
		t.Error("an old-side-anchored finding must never become a suggestion")
	}
}

func TestSuggestBelowFloorDegradesToHint(t *testing.T) {
	c := &recordClient{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"}
	f := suggestFinding()
	f.Severity = "low" // below the medium floor
	_, err := PostReview(stdctx.Background(), c, info, []engine.Finding{f}, suggestDiff(), "", nil, PostReviewOptions{Suggest: true})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if strings.Contains(postedBody(t, c), "suggestion") {
		t.Error("a below-floor finding must degrade to a hint")
	}
}

func TestSuggestSeverityFloorUsesEngineRank(t *testing.T) {
	if !meetsSuggestionFloor("high") || !meetsSuggestionFloor("critical") || !meetsSuggestionFloor("medium") {
		t.Fatal("medium/high/critical must meet the floor")
	}
	if meetsSuggestionFloor("low") || meetsSuggestionFloor("info") || meetsSuggestionFloor("") {
		t.Fatal("low/info/empty must be below the floor")
	}
	if severityRankOf("high") <= severityRankOf("medium") {
		t.Fatal("engine rank must order high above medium (NOT the inverted github rank)")
	}
}

func TestExistingFingerprintsRateLimitMapped(t *testing.T) {
	c := &recordClient{listRevErr: &gh.RateLimitError{Message: "rate limited"}}
	_, err := ExistingFingerprints(stdctx.Background(), c, &PRInfo{Owner: "o", Repo: "r", Number: 1})
	if err == nil {
		t.Fatal("want rate-limit error")
	}
	var ce *clierr.CLIError
	if !asCLIErr(err, &ce) || ce.Code != "github.rate_limited" {
		t.Fatalf("want github.rate_limited, got %v", err)
	}
	if !ce.Retry {
		t.Error("list rate-limit error must be retryable")
	}
}

func TestUpsertSummaryListRateLimitMapped(t *testing.T) {
	c := &recordClient{listIssueErr: &gh.RateLimitError{Message: "rate limited"}}
	_, err := UpsertSummaryComment(stdctx.Background(), c, &PRInfo{Owner: "o", Repo: "r", Number: 1}, "body")
	if err == nil {
		t.Fatal("want rate-limit error")
	}
	var ce *clierr.CLIError
	if !asCLIErr(err, &ce) || ce.Code != "github.rate_limited" {
		t.Fatalf("want github.rate_limited, got %v", err)
	}
	if !ce.Retry {
		t.Error("list rate-limit error must be retryable")
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
