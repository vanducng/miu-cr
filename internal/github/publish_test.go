package github

import (
	stdctx "context"
	"fmt"
	"sort"
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

	headSHA                  string // GetPR returns this head SHA; empty means "headsha"
	conflicted               bool
	mergeabilityUnknownCalls int
	getPRN                   int

	createReviewErr      error
	createReviewErrFirst error // returned on the FIRST CreateReview call only (the APPROVE attempt)
	createIssueErr       error
	editErr              error

	gotReview     *gh.PullRequestReviewRequest
	gotReviews    []*gh.PullRequestReviewRequest
	createdIssue  *gh.IssueComment
	editedID      int64
	editedBody    string
	createReviewN int
	createIssueN  int
	editN         int

	// Stateful issue-comment store for the upsert invariant: when non-nil it backs
	// ListIssueComments (sorted by id) and is mutated by Create (append, next id) /
	// Edit (replace body by id). Existing page-based tests leave it nil and use the
	// issueComments [][] pages above unchanged.
	issueStore []*gh.IssueComment
	issueIDSeq int64

	createCheckErr error
	gotCheck       *gh.CreateCheckRunOptions
	gotCheckUpd    []*gh.UpdateCheckRunOptions
	gotCheckUpdID  int64
	checkRunN      int

	existingCheckRuns []*gh.CheckRun // returned by ListCheckRunsForRef (nil → create path)
	listCheckErr      error
	listCheckRunN     int
	combinedStatuses  []*gh.RepoStatus
	combinedStatusErr error
}

func (c *recordClient) GetPR(stdctx.Context, string, string, int) (*gh.PullRequest, error) {
	c.getPRN++
	sha := c.headSHA
	if sha == "" {
		sha = "headsha"
	}
	pr := &gh.PullRequest{Head: &gh.PullRequestBranch{SHA: gh.Ptr(sha)}}
	if c.getPRN > c.mergeabilityUnknownCalls {
		pr.Mergeable = gh.Ptr(!c.conflicted)
	}
	return pr, nil
}
func (c *recordClient) ListFiles(stdctx.Context, string, string, int, *gh.ListOptions) ([]*gh.CommitFile, *gh.Response, error) {
	return nil, &gh.Response{}, nil
}
func (c *recordClient) GetCommit(stdctx.Context, string, string, string) (*gh.Commit, error) {
	return nil, nil
}

func (c *recordClient) CreateReview(_ stdctx.Context, _, _ string, _ int, r *gh.PullRequestReviewRequest) (*gh.PullRequestReview, error) {
	c.createReviewN++
	c.gotReview = r
	c.gotReviews = append(c.gotReviews, r)
	if c.createReviewErrFirst != nil && c.createReviewN == 1 {
		return nil, c.createReviewErrFirst
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
	if c.issueStore != nil {
		sorted := append([]*gh.IssueComment(nil), c.issueStore...)
		sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].GetID() < sorted[j].GetID() })
		return sorted, &gh.Response{}, nil
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
	if c.createIssueErr != nil {
		return nil, c.createIssueErr
	}
	c.issueIDSeq++
	stored := &gh.IssueComment{ID: gh.Ptr(c.issueIDSeq), Body: gh.Ptr(com.GetBody())}
	c.issueStore = append(c.issueStore, stored)
	return stored, nil
}

func (c *recordClient) EditIssueComment(_ stdctx.Context, _, _ string, id int64, com *gh.IssueComment) (*gh.IssueComment, error) {
	c.editN++
	c.editedID = id
	c.editedBody = com.GetBody()
	if c.editErr != nil {
		return nil, c.editErr
	}
	for _, ic := range c.issueStore {
		if ic.GetID() == id {
			ic.Body = gh.Ptr(com.GetBody())
		}
	}
	return com, nil
}

func (c *recordClient) CreateIssueReaction(stdctx.Context, string, string, int, string) (*gh.Reaction, error) {
	return &gh.Reaction{}, nil
}

func (c *recordClient) CreateCheckRun(_ stdctx.Context, _, _ string, opts gh.CreateCheckRunOptions) (*gh.CheckRun, error) {
	c.checkRunN++
	o := opts
	c.gotCheck = &o
	if c.createCheckErr != nil {
		return nil, c.createCheckErr
	}
	return &gh.CheckRun{ID: gh.Ptr(int64(42))}, nil
}

func (c *recordClient) UpdateCheckRun(_ stdctx.Context, _, _ string, id int64, opts gh.UpdateCheckRunOptions) (*gh.CheckRun, error) {
	c.gotCheckUpdID = id
	o := opts
	c.gotCheckUpd = append(c.gotCheckUpd, &o)
	return &gh.CheckRun{ID: gh.Ptr(id)}, nil
}

func (c *recordClient) ListCheckRunsForRef(_ stdctx.Context, _, _, _ string, _ *gh.ListCheckRunsOptions) (*gh.ListCheckRunsResults, *gh.Response, error) {
	c.listCheckRunN++
	if c.listCheckErr != nil {
		return nil, nil, c.listCheckErr
	}
	return &gh.ListCheckRunsResults{CheckRuns: c.existingCheckRuns, Total: gh.Ptr(len(c.existingCheckRuns))}, &gh.Response{}, nil
}

func (c *recordClient) GetCombinedStatus(_ stdctx.Context, _, _, _ string, _ *gh.ListOptions) (*gh.CombinedStatus, *gh.Response, error) {
	if c.combinedStatusErr != nil {
		return nil, nil, c.combinedStatusErr
	}
	return &gh.CombinedStatus{Statuses: c.combinedStatuses}, &gh.Response{}, nil
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

// staticSummary adapts a constant body into PostReview's summaryFn (ignores the
// omitted set). nil/"" body → a nil summaryFn (no review body).
func staticSummary(body string) func(int, []engine.Finding) string {
	if body == "" {
		return nil
	}
	return func(int, []engine.Finding) string { return body }
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

func TestFilterFindingsModes(t *testing.T) {
	diffs := sampleDiffs() // p.go: added 2,3; context 1,4,5
	findings := []engine.Finding{
		{File: "p.go", Line: 2},  // added
		{File: "p.go", Line: 4},  // context
		{File: "p.go", Line: 99}, // in-file, off-hunk
		{File: "p.go", Line: 0},  // drift
		{File: "x.go", Line: 1},  // file not in diff
	}
	cases := []struct {
		mode FilterMode
		want int
	}{
		{FilterAdded, 1},       // only line 2
		{FilterDiffContext, 2}, // lines 2,4
		{FilterFile, 4},        // every p.go finding (incl. off-hunk + drift), not x.go
		{FilterNoFilter, 5},    // all
	}
	for _, c := range cases {
		got := filterFindings(findings, diffs, c.mode)
		if len(got) != c.want {
			t.Errorf("mode %s: want %d kept, got %d: %+v", c.mode, c.want, len(got), got)
		}
	}
}

func TestInlineEligibleNeverWidensPastDiffContext(t *testing.T) {
	diffs := sampleDiffs()
	findings := []engine.Finding{
		{File: "p.go", Line: 2},  // added → inline-eligible in every mode
		{File: "p.go", Line: 4},  // context → eligible except added
		{File: "p.go", Line: 99}, // off-hunk → NEVER inline-eligible
	}
	// file/nofilter must NOT make the off-hunk finding inline-eligible.
	for _, m := range []FilterMode{FilterFile, FilterNoFilter, FilterDiffContext} {
		got := inlineEligible(findings, diffs, m)
		if len(got) != 2 {
			t.Errorf("mode %s: inline set should be 2 (off-hunk excluded), got %d", m, len(got))
		}
	}
	if got := inlineEligible(findings, diffs, FilterAdded); len(got) != 1 {
		t.Errorf("added inline set should be 1, got %d", len(got))
	}
}

func TestValidFilterMode(t *testing.T) {
	for _, ok := range []string{"added", "diff_context", "file", "nofilter"} {
		if !ValidFilterMode(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	if ValidFilterMode("bogus") || ValidFilterMode("") {
		t.Error("bogus/empty should be invalid")
	}
}

func TestValidMinSeverity(t *testing.T) {
	for _, ok := range []string{"none", "info", "low", "medium", "high", "critical"} {
		if !ValidMinSeverity(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	if ValidMinSeverity("bogus") || ValidMinSeverity("") {
		t.Error("bogus/empty should be invalid")
	}
}

func TestMinSeverityFloor(t *testing.T) {
	findings := []engine.Finding{
		{File: "p.go", Line: 1, Severity: "info"},
		{File: "p.go", Line: 2, Severity: "low"},
		{File: "p.go", Line: 3, Severity: "high"},
		{File: "p.go", Line: 4, Severity: "critical"},
		{File: "p.go", Line: 5, Severity: ""}, // ungraded
	}
	// Empty/"none" is a no-op.
	if got := minSeverityFloor(findings, ""); len(got) != 5 {
		t.Fatalf("empty floor must keep all, got %d", len(got))
	}
	if got := minSeverityFloor(findings, "none"); len(got) != 5 {
		t.Fatalf("none floor must keep all, got %d", len(got))
	}
	// high floor keeps high+critical only (info/low/ungraded dropped).
	got := minSeverityFloor(findings, "high")
	if len(got) != 2 {
		t.Fatalf("high floor: want 2 kept, got %d: %+v", len(got), got)
	}
	for _, f := range got {
		if f.Severity != "high" && f.Severity != "critical" {
			t.Fatalf("high floor leaked %q", f.Severity)
		}
	}
}

func TestPostReviewMinSeverityFloor(t *testing.T) {
	c := &recordClient{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"}
	// sampleDiffs: added lines 2,3 (inline-eligible). Mix severities on them.
	findings := []engine.Finding{
		{File: "p.go", Line: 2, Severity: "low", Category: "x", Rationale: "minor"},
		{File: "p.go", Line: 3, Severity: "high", Category: "bug", Rationale: "boom"},
	}
	res, err := PostReview(stdctx.Background(), c, info, findings, sampleDiffs(), staticSummary("summary"), nil, PostReviewOptions{MinSeverity: "high"})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if res.Posted != 1 {
		t.Fatalf("min-severity high: want 1 inline posted, got %d", res.Posted)
	}
	if c.gotReview == nil || len(c.gotReview.Comments) != 1 {
		t.Fatalf("want exactly 1 inline comment under the floor")
	}
	if c.gotReview.Comments[0].GetLine() != 3 {
		t.Fatalf("the high finding (line 3) must be the one posted, got line %d", c.gotReview.Comments[0].GetLine())
	}
	// The below-threshold finding still reaches the summary header counts.
	summary := RenderSummary(info, findings, nil, 0)
	if !strings.Contains(summary, shieldsCount("P1", 1, "orange")) || !strings.Contains(summary, shieldsCount("P3", 1, "blue")) || !strings.Contains(summary, "2 findings") {
		t.Fatalf("both findings must appear in the summary count badges:\n%s", summary)
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
	res, err := PostReview(stdctx.Background(), c, info, findings, sampleDiffs(), staticSummary("summary body"), nil, PostReviewOptions{})
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

	res, err := PostReview(stdctx.Background(), c, info, []engine.Finding{f}, sampleDiffs(), staticSummary(""), map[string]bool{fp: true}, PostReviewOptions{})
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
			{{Body: gh.Ptr("note\n\n<!-- miucr:fp=00112233aabbccdd -->"), HTMLURL: gh.Ptr("https://github.com/o/r/pull/1#discussion_r42")}},
			{{Body: gh.Ptr("plain comment, no marker")}},
		},
	}
	got, err := ExistingFingerprints(stdctx.Background(), c, &PRInfo{Owner: "o", Repo: "r", Number: 1})
	if err != nil {
		t.Fatalf("ExistingFingerprints: %v", err)
	}
	if got["00112233aabbccdd"] != "https://github.com/o/r/pull/1#discussion_r42" {
		t.Fatalf("want fp mapped to its inline comment URL, got %+v", got)
	}
	if len(got) != 1 {
		t.Fatalf("want exactly 1 fp, got %d", len(got))
	}
}

func TestExistingFingerprintsFirstCommentWins(t *testing.T) {
	// Two comments share one fp marker (root then reply); the ROOT (first) comment's
	// URL must win so the Location deep-links to the thread, not a reply.
	c := &recordClient{
		reviewComments: [][]*gh.PullRequestComment{
			{
				{Body: gh.Ptr("root\n<!-- miucr:fp=00112233aabbccdd -->"), HTMLURL: gh.Ptr("https://github.com/o/r/pull/1#discussion_root")},
				{Body: gh.Ptr("reply\n<!-- miucr:fp=00112233aabbccdd -->"), HTMLURL: gh.Ptr("https://github.com/o/r/pull/1#discussion_reply")},
			},
		},
	}
	got, err := ExistingFingerprints(stdctx.Background(), c, &PRInfo{Owner: "o", Repo: "r", Number: 1})
	if err != nil {
		t.Fatalf("ExistingFingerprints: %v", err)
	}
	if got["00112233aabbccdd"] != "https://github.com/o/r/pull/1#discussion_root" {
		t.Fatalf("first (root) comment URL must win, got %q", got["00112233aabbccdd"])
	}
}

// PostReview primitive: a non-nil summaryFn renders into the CreateReview BODY
// (marker + reviewed-commit lead-in), and no issue comment is touched. The wire
// layer now passes nil (summary upserts as an issue comment instead); this asserts
// the primitive's body path still works for any caller that wants it.
func TestPostReviewSummaryIsReviewBody(t *testing.T) {
	c := &recordClient{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "deadbeef"}
	findings := []engine.Finding{{File: "p.go", Line: 2, Severity: "high", Category: "bug", Rationale: "boom"}}
	summaryFn := func(omitted int, omittedFindings []engine.Finding) string {
		return RenderSummary(info, findings, nil, omitted)
	}
	if _, err := PostReview(stdctx.Background(), c, info, findings, sampleDiffs(), summaryFn, nil, PostReviewOptions{}); err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	r := c.gotReview
	if r == nil || r.GetBody() == "" {
		t.Fatal("CreateReview must carry the summary as its body")
	}
	if !strings.Contains(r.GetBody(), ReviewMarker) {
		t.Errorf("review body must contain the marker:\n%s", r.GetBody())
	}
	if !strings.Contains(r.GetBody(), "Last reviewed commit `deadbee`") {
		t.Errorf("review body must contain the reviewed-commit footer:\n%s", r.GetBody())
	}
	if c.createIssueN != 0 || c.editN != 0 {
		t.Errorf("no issue comment must be created/edited (Codex pattern), got create=%d edit=%d", c.createIssueN, c.editN)
	}
}

func TestPostReviewRateLimitMapped(t *testing.T) {
	c := &recordClient{createReviewErr: &gh.RateLimitError{Message: "rate limited"}}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"}
	findings := []engine.Finding{{File: "p.go", Line: 2, Rationale: "x"}}
	_, err := PostReview(stdctx.Background(), c, info, findings, sampleDiffs(), staticSummary(""), nil, PostReviewOptions{})
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

	res, err := PostReview(stdctx.Background(), c, info, findings, diffs, staticSummary(""), nil, PostReviewOptions{})
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
	body, native := commentBody(nil, f, "", PostReviewOptions{}, false)
	if native {
		t.Error("Suggest OFF must report native=false")
	}
	if strings.Contains(body, "suggestion") {
		t.Errorf("must NOT emit a one-click suggestion fence (latent M2 bug):\n%s", body)
	}
	if !strings.Contains(body, "````\nbefore\n```\nafter\n````") {
		t.Errorf("want a neutral (no-language) 4-backtick hint fence so the embedded ``` cannot terminate it early:\n%s", body)
	}
	if strings.Contains(body, "````go") {
		t.Errorf("hint fence must NOT hardcode a go language tag (findings span languages):\n%s", body)
	}
	if c := strings.Count(body, "````"); c != 2 {
		t.Errorf("want exactly one opening + one closing 4-backtick fence, got %d:\n%s", c, body)
	}
}

// A rationale's inline `code` spans are preserved (rendered monospace), but an
// unbalanced backtick still cannot leak into the trailing one-click suggestion
// fence: the fence sits after a blank line, so any open inline span closes at that
// paragraph boundary. This guards the mdProse change that stopped escaping all
// backticks in favour of preserving code spans.
func TestCommentBodyRationaleInlineCodeSafeWithPatch(t *testing.T) {
	f := engine.Finding{
		Severity:       "medium",
		Category:       "bug",
		Rationale:      "column `delivered_at` is missing; an unbalanced ` cannot reach the fence",
		SuggestedPatch: "ORDER BY created_at DESC",
	}
	body, _ := commentBody(nil, f, "", PostReviewOptions{}, false)
	if !strings.Contains(body, "`delivered_at`") {
		t.Errorf("inline code span in rationale must be preserved as monospace:\n%s", body)
	}
	if !strings.Contains(body, "```\nORDER BY created_at DESC\n```") {
		t.Errorf("the patch fence must render intact after a backtick-bearing rationale:\n%s", body)
	}
}

// commentBody leads with the bold title when present; absent, the body is
// byte-for-byte today's body; an untrusted title is mdInline-escaped (no breakout).
func TestCommentBodyTitle(t *testing.T) {
	base := engine.Finding{Severity: "high", Category: "bug", Rationale: "nil deref on err path"}

	noTitle, _ := commentBody(nil, base, "", PostReviewOptions{}, false)
	if strings.Contains(noTitle, "\n\n\n") {
		t.Errorf("no-title body must not carry an extra title line:\n%s", noTitle)
	}

	withTitle := base
	withTitle.Title = "Unchecked nil deref"
	body, _ := commentBody(nil, withTitle, "", PostReviewOptions{}, false)
	if !strings.Contains(body, "**Unchecked nil deref**") {
		t.Errorf("body must lead with the bold title:\n%s", body)
	}
	// Title sits before the rationale.
	if strings.Index(body, "**Unchecked nil deref**") > strings.Index(body, "nil deref on err path") {
		t.Errorf("title must precede the rationale:\n%s", body)
	}

	evil := base
	evil.Title = "pwn](http://evil) `code` <script>"
	eb, _ := commentBody(nil, evil, "", PostReviewOptions{}, false)
	if strings.Contains(eb, "pwn](http://evil)") || strings.Contains(eb, "<script>") {
		t.Errorf("untrusted title must be mdInline-escaped:\n%s", eb)
	}
}

func TestCommentBodyRationaleEscaped(t *testing.T) {
	// Untrusted rationale must not break out of the comment: no </details>, no
	// <!-- sentinel -->, no <script>, and no ``` fence that would swallow a
	// subsequent suggestion/patch block.
	f := engine.Finding{
		Severity:  "high",
		Category:  "bug",
		Rationale: "real </details> <!-- miucr:fp=deadbeef --> and a ```go fence``` <script>alert(1)</script>",
	}
	body, _ := commentBody(nil, f, "", PostReviewOptions{}, false)
	for _, bad := range []string{"</details>", "<!--", "<script>", "```"} {
		if strings.Contains(body, bad) {
			t.Errorf("rationale breakout %q not escaped in body:\n%s", bad, body)
		}
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
	return c.gotReview.Comments[0].GetBody()
}

func TestSuggestEmitsNativeSuggestionForCleanSingleLine(t *testing.T) {
	c := &recordClient{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"}
	_, err := PostReview(stdctx.Background(), c, info, []engine.Finding{suggestFinding()}, suggestDiff(), staticSummary(""), nil, PostReviewOptions{Suggest: true})
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
	_, err := PostReview(stdctx.Background(), c, info, []engine.Finding{suggestFinding()}, suggestDiff(), staticSummary(""), nil, PostReviewOptions{})
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
	_, err := PostReview(stdctx.Background(), c, info, []engine.Finding{f}, suggestDiff(), staticSummary(""), nil, PostReviewOptions{Suggest: true})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	body := postedBody(t, c)
	if strings.Contains(body, "suggestion") {
		t.Errorf("multi-line finding must degrade to a hint:\n%s", body)
	}
}

func TestSuggestMultiLinePatchOnSingleAnchorEmits(t *testing.T) {
	// A wrap/guard fix: single-line anchor (QuotedCode-proven), multi-line patch.
	// GitHub replaces exactly the anchored line with the block, so this is safe.
	c := &recordClient{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"}
	f := suggestFinding()
	f.SuggestedPatch = "if a == nil {\n\tvar a = 2\n}" // wrap the anchored line
	_, err := PostReview(stdctx.Background(), c, info, []engine.Finding{f}, suggestDiff(), staticSummary(""), nil, PostReviewOptions{Suggest: true})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if !strings.Contains(postedBody(t, c), "```suggestion\nif a == nil {\n\tvar a = 2\n}\n```") {
		t.Errorf("a multi-line wrap patch on a proven single-line anchor must emit a suggestion:\n%s", postedBody(t, c))
	}
}

func TestSuggestMultiLinePatchAnchorMismatchDropped(t *testing.T) {
	// The safety boundary: a multi-line patch whose anchored line does NOT match
	// QuotedCode must be dropped, never a wrong-span replace (no data loss).
	c := &recordClient{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"}
	f := suggestFinding()
	f.QuotedCode = "var a = 0" // old-side anchor: != raw NewFileContent line 2 ("var a = 1")
	f.SuggestedPatch = "if a == nil {\n\tvar a = 2\n}"
	_, err := PostReview(stdctx.Background(), c, info, []engine.Finding{f}, suggestDiff(), staticSummary(""), nil, PostReviewOptions{Suggest: true})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if strings.Contains(postedBody(t, c), "suggestion") {
		t.Errorf("a multi-line patch on a mismatched anchor must be dropped:\n%s", postedBody(t, c))
	}
}

func TestSuggestNoOpDegradesToHint(t *testing.T) {
	c := &recordClient{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"}
	f := suggestFinding()
	f.SuggestedPatch = "var a = 1" // identical to raw line → no-op
	_, err := PostReview(stdctx.Background(), c, info, []engine.Finding{f}, suggestDiff(), staticSummary(""), nil, PostReviewOptions{Suggest: true})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if strings.Contains(postedBody(t, c), "suggestion") {
		t.Error("a no-op replacement must degrade to a hint")
	}
}

func TestSuggestOperatorPrefixedPatchStillSuggests(t *testing.T) {
	// A patch that legitimately begins with +/- (operator-prefixed code) and
	// differs from the raw line must still emit a suggestion, the no-op check
	// must NOT strip +/- from the patch (else it falsely reads as a no-op).
	c := &recordClient{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"}
	d := `@@ -1,3 +1,3 @@
 package p
-var a = 0
+delta
 func f() {}
`
	diffs := []diff.Diff{{
		NewPath:        "p.go",
		Diff:           d,
		NewFileContent: "package p\ndelta\nfunc f() {}\n",
	}}
	f := engine.Finding{
		File:           "p.go",
		Line:           2,
		Severity:       "high",
		Category:       "bug",
		Rationale:      "negate the delta",
		QuotedCode:     "delta",
		SuggestedPatch: "+delta", // starts with '+' but differs from raw "delta"
	}
	_, err := PostReview(stdctx.Background(), c, info, []engine.Finding{f}, diffs, staticSummary(""), nil, PostReviewOptions{Suggest: true})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	body := postedBody(t, c)
	if !strings.Contains(body, "```suggestion\n+delta\n```") {
		t.Errorf("operator-prefixed patch differing from the raw line must still suggest:\n%s", body)
	}
}

func TestSuggestOldSideAnchoredDegradesToHint(t *testing.T) {
	c := &recordClient{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"}
	f := suggestFinding()
	// Anchor fell back to old side: QuotedCode does NOT match raw NewFileContent[Line].
	f.QuotedCode = "var a = 0"
	_, err := PostReview(stdctx.Background(), c, info, []engine.Finding{f}, suggestDiff(), staticSummary(""), nil, PostReviewOptions{Suggest: true})
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
	_, err := PostReview(stdctx.Background(), c, info, []engine.Finding{f}, suggestDiff(), staticSummary(""), nil, PostReviewOptions{Suggest: true})
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

func TestFingerprintStable(t *testing.T) {
	f := engine.Finding{File: "p.go", Line: 2, Category: "bug", QuotedCode: "x := y / 0"}
	if fingerprint(f) != fingerprint(f) {
		t.Fatal("fingerprint must be deterministic")
	}
}

// Cross-push: the SAME QuotedCode that re-anchors to a different Line must yield
// the SAME fingerprint so the existing marker dedupes the re-post (no DB needed).
func TestFingerprintCrossPushSameCode(t *testing.T) {
	f := engine.Finding{File: "p.go", Line: 2, Category: "bug", QuotedCode: "x := y / 0"}
	g := f
	g.Line = 99
	if fingerprint(f) != fingerprint(g) {
		t.Fatal("same QuotedCode at a different Line must yield the SAME fingerprint")
	}
}

// Rationale is LLM free-text and must NOT fragment the key.
func TestFingerprintIgnoresRationale(t *testing.T) {
	f := engine.Finding{File: "p.go", Category: "bug", QuotedCode: "x := y / 0", Rationale: "divide by zero"}
	g := f
	g.Rationale = "totally different prose"
	if fingerprint(f) != fingerprint(g) {
		t.Fatal("Rationale must not change the fingerprint")
	}
}

// No over-dedup: findings differing only by leading indentation must differ.
func TestFingerprintIndentationDistinct(t *testing.T) {
	f := engine.Finding{File: "p.go", Category: "bug", QuotedCode: "x := 1"}
	g := f
	g.QuotedCode = "    x := 1"
	if fingerprint(f) == fingerprint(g) {
		t.Fatal("indentation-only difference must yield a DIFFERENT fingerprint (no over-dedup)")
	}
}

// No over-dedup: findings differing only by an interior blank line must differ.
func TestFingerprintBlankLineDistinct(t *testing.T) {
	f := engine.Finding{File: "p.go", Category: "bug", QuotedCode: "a()\nb()"}
	g := f
	g.QuotedCode = "a()\n\nb()"
	if fingerprint(f) == fingerprint(g) {
		t.Fatal("blank-line-only difference must yield a DIFFERENT fingerprint (no over-dedup)")
	}
}

// Same file+category but genuinely different code must differ.
func TestFingerprintDifferentCode(t *testing.T) {
	f := engine.Finding{File: "p.go", Category: "bug", QuotedCode: "x := y / 0"}
	g := f
	g.QuotedCode = "z := w / 0"
	if fingerprint(f) == fingerprint(g) {
		t.Fatal("different code must yield a different fingerprint")
	}
}

// normalizeForFingerprint strips the diff column ONLY for a wholly diff-formatted
// quote. Genuine code with a leading '-'/'+' line (mixed with non-diff lines) must
// NOT be collapsed into its marker-less form (over-dedup).
func TestFingerprintLeadingMarkerCodeNotStripped(t *testing.T) {
	f := engine.Finding{File: "p.go", Category: "bug", QuotedCode: "-1\nfoo()"}
	g := f
	g.QuotedCode = "1\nfoo()"
	if fingerprint(f) == fingerprint(g) {
		t.Fatal("non-diff code with a leading '-' must not collide with its marker-less form")
	}
}

// A wholly diff-formatted quote (every non-blank line begins +/-/space) strips the
// diff column, so the same change quoted as a hunk maps to its marker-less form.
func TestFingerprintWhollyDiffStripped(t *testing.T) {
	diff := engine.Finding{File: "p.go", Category: "bug", QuotedCode: "-old()\n+new()"}
	plain := engine.Finding{File: "p.go", Category: "bug", QuotedCode: "old()\nnew()"}
	if fingerprint(diff) != fingerprint(plain) {
		t.Fatal("a wholly diff-formatted quote should normalize to its marker-less form")
	}
}

// Under-dedup (documented, best-effort): the same bug quoted with a different span
// yields a DIFFERENT fingerprint, exact content match is the M5 ceiling, semantic
// matching is M7. This asserts the accepted limitation rather than a desired win.
func TestFingerprintUnderDedupDifferentSpan(t *testing.T) {
	f := engine.Finding{File: "p.go", Category: "bug", QuotedCode: "x := y / 0"}
	g := f
	g.QuotedCode = "x := y / 0\nreturn x"
	if fingerprint(f) == fingerprint(g) {
		t.Fatal("documented under-dedup: a different quote span is expected to differ (M7 = semantic)")
	}
}

// Guard: two findings whose QuotedCode normalizes to empty (empty or lone diff
// marker) on the same file+category must NOT collapse to one fp (silent
// over-dedup). The empty-quote path disambiguates by Line+Rationale.
func TestFingerprintEmptyQuoteDistinct(t *testing.T) {
	for _, tc := range []struct{ name, code string }{
		{"empty", ""},
		{"lone-plus", "+"},
		{"lone-minus", "-"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := engine.Finding{File: "p.go", Line: 4, Category: "bug", QuotedCode: tc.code, Rationale: "first bug"}
			g := engine.Finding{File: "p.go", Line: 9, Category: "bug", QuotedCode: tc.code, Rationale: "second bug"}
			if fingerprint(f) == fingerprint(g) {
				t.Fatalf("two empty-quote findings (same file+category) must yield DIFFERENT fps")
			}
		})
	}
}

// normalizeForFingerprint strips a leading diff +/- marker and trailing whitespace
// and normalizes CRLF, but preserves leading indentation and blank lines, so a
// diff-quoted finding maps to the same fp as its plain-quoted twin.
func TestFingerprintDiffMarkerAndCRLF(t *testing.T) {
	plain := engine.Finding{File: "p.go", Category: "bug", QuotedCode: "    a()\n    b()"}
	diffQuoted := engine.Finding{File: "p.go", Category: "bug", QuotedCode: "+    a()  \r\n+    b()  "}
	if fingerprint(plain) != fingerprint(diffQuoted) {
		t.Fatalf("diff marker + trailing ws + CRLF must normalize to the plain fp")
	}
}

// The marker round-trips: fpMarker(fingerprint(f)) is exactly 16 lowercase hex.
func TestFingerprintMarkerRoundTrip(t *testing.T) {
	f := engine.Finding{File: "p.go", Category: "bug", QuotedCode: "x := y / 0"}
	m := fpMarker(fingerprint(f))
	got := fpMarkerRe.FindStringSubmatch(m)
	if got == nil {
		t.Fatalf("fpMarker(%q) does not match fpMarkerRe", m)
	}
	if len(got[1]) != 16 {
		t.Fatalf("fingerprint width = %d, want 16 hex", len(got[1]))
	}
}

// multiLineDiff: a single hunk whose new-side lines 1..4 are all on the RIGHT
// side, so a finding spanning lines 2..3 is contiguous within ONE hunk.
func multiLineDiff() []diff.Diff {
	d := `@@ -1,4 +1,4 @@
 package p
-var a = 0
-var b = 0
+var a = 1
+var b = 2
 func f() {}
`
	return []diff.Diff{{
		NewPath:        "p.go",
		Diff:           d,
		NewFileContent: "package p\nvar a = 1\nvar b = 2\nfunc f() {}\n",
	}}
}

func TestPostReviewMultiLineRangeContiguousInOneHunk(t *testing.T) {
	c := &recordClient{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"}
	f := engine.Finding{File: "p.go", Line: 2, EndLine: 3, Severity: "high", Category: "bug", Rationale: "span"}
	res, err := PostReview(stdctx.Background(), c, info, []engine.Finding{f}, multiLineDiff(), staticSummary(""), nil, PostReviewOptions{})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if res.Ranges != 1 {
		t.Fatalf("want 1 multi-line range, got %d", res.Ranges)
	}
	dc := c.gotReview.Comments[0]
	if dc.GetStartLine() != 2 || dc.GetStartSide() != "RIGHT" || dc.GetLine() != 3 || dc.GetSide() != "RIGHT" {
		t.Fatalf("want RIGHT range StartLine=2..Line=3, got start=%d/%s line=%d/%s",
			dc.GetStartLine(), dc.GetStartSide(), dc.GetLine(), dc.GetSide())
	}
}

func TestPostReviewMultiLineCrossHunkFallsBackToSingleLine(t *testing.T) {
	// Two separate hunks: line 2 in hunk 1, line 20 in hunk 2. A 2..20 span is NOT
	// contiguous within one hunk → must fall back to single-line (no GitHub 422).
	c := &recordClient{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"}
	f := engine.Finding{File: "p.go", Line: 2, EndLine: 20, Severity: "high", Category: "bug", Rationale: "x"}
	d := `@@ -1,2 +1,2 @@
 package p
-var a = 0
+var a = 1
@@ -19,2 +19,2 @@
 func g() {}
-var z = 0
+var z = 1
`
	diffs := []diff.Diff{{NewPath: "p.go", Diff: d}}
	res, err := PostReview(stdctx.Background(), c, info, []engine.Finding{f}, diffs, staticSummary(""), nil, PostReviewOptions{})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if res.Ranges != 0 {
		t.Fatalf("cross-hunk span must fall back to single-line, got %d ranges", res.Ranges)
	}
	dc := c.gotReview.Comments[0]
	if dc.StartLine != nil || dc.GetLine() != 2 {
		t.Fatalf("want single-line at Line=2, got start=%v line=%d", dc.StartLine, dc.GetLine())
	}
}

func TestPostReviewMultiLineSuggestionForCleanOnDiffSpan(t *testing.T) {
	c := &recordClient{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"}
	f := engine.Finding{
		File: "p.go", Line: 2, EndLine: 3, Severity: "high", Category: "bug",
		Rationale:      "swap both",
		QuotedCode:     "var a = 1\nvar b = 2",
		SuggestedPatch: "var a = 2\nvar b = 3",
	}
	res, err := PostReview(stdctx.Background(), c, info, []engine.Finding{f}, multiLineDiff(), staticSummary(""), nil, PostReviewOptions{Suggest: true})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if res.Suggestions != 1 || res.Ranges != 1 {
		t.Fatalf("want a 1 multi-line suggestion on a range, got suggestions=%d ranges=%d", res.Suggestions, res.Ranges)
	}
	if !strings.Contains(c.gotReview.Comments[0].GetBody(), "```suggestion\nvar a = 2\nvar b = 3\n```") {
		t.Errorf("want a native multi-line suggestion:\n%s", c.gotReview.Comments[0].GetBody())
	}
}

func TestPostReviewMultiLineSuggestionRejectsMismatchedSpan(t *testing.T) {
	// QuotedCode does NOT match the raw new-file span → never a one-click apply.
	c := &recordClient{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"}
	f := engine.Finding{
		File: "p.go", Line: 2, EndLine: 3, Severity: "high", Category: "bug",
		Rationale:      "swap",
		QuotedCode:     "var a = 1\nUNRELATED",
		SuggestedPatch: "var a = 2\nvar b = 3",
	}
	_, err := PostReview(stdctx.Background(), c, info, []engine.Finding{f}, multiLineDiff(), staticSummary(""), nil, PostReviewOptions{Suggest: true})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if strings.Contains(c.gotReview.Comments[0].GetBody(), "suggestion") {
		t.Errorf("a span mismatch must degrade to a hint, never a one-click suggestion:\n%s", c.gotReview.Comments[0].GetBody())
	}
}

// TestPostReviewMultiLineSuggestionRejectsOffRangeSpan covers the decoupled-gate
// bug: Line is in one hunk, EndLine in a LATER hunk across an unchanged gap, but the
// QuotedCode DOES match the contiguous raw new-file span. filterToDiffHunks keeps it
// (only Line must be in a hunk) and cleanMultiLineReplacement passes (new-file lines
// match), yet rangeInOneHunk(Line,EndLine)=false → the comment is single-line. It
// MUST NOT carry a multi-line ```suggestion fence: a single-anchored multi-line
// suggestion inserts the block instead of replacing the span (a broken one-click
// patch). The same rangeInOneHunk proof must gate the suggestion, not just the range.
func TestPostReviewMultiLineSuggestionRejectsOffRangeSpan(t *testing.T) {
	c := &recordClient{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"}
	// Two hunks; new-file line 3 ("mid") is an unchanged gap NOT in any hunk set, so
	// the 2..4 span is not contiguous-in-one-hunk even though it exists contiguously
	// in the new file and matches QuotedCode verbatim.
	d := `@@ -1,2 +1,2 @@
 package p
-var a = 0
+var a = 1
@@ -3,2 +3,2 @@
 mid
-var b = 0
+var b = 2
`
	diffs := []diff.Diff{{
		NewPath:        "p.go",
		Diff:           d,
		NewFileContent: "package p\nvar a = 1\nmid\nvar b = 2\n",
	}}
	f := engine.Finding{
		File: "p.go", Line: 2, EndLine: 4, Severity: "high", Category: "bug",
		Rationale:      "off-range span",
		QuotedCode:     "var a = 1\nmid\nvar b = 2",
		SuggestedPatch: "var a = 2\nmid2\nvar b = 3",
	}
	res, err := PostReview(stdctx.Background(), c, info, []engine.Finding{f}, diffs, staticSummary(""), nil, PostReviewOptions{Suggest: true})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if res.Ranges != 0 {
		t.Fatalf("off-range span must fall back to single-line, got %d ranges", res.Ranges)
	}
	if res.Suggestions != 0 {
		t.Fatalf("off-range span must NOT emit a native suggestion, got %d", res.Suggestions)
	}
	dc := c.gotReview.Comments[0]
	if dc.StartLine != nil {
		t.Fatalf("want single-line comment (no StartLine), got start=%d", dc.GetStartLine())
	}
	if strings.Contains(dc.GetBody(), "```suggestion") {
		t.Errorf("a single-line comment must NOT carry a multi-line one-click suggestion:\n%s", dc.GetBody())
	}
}
