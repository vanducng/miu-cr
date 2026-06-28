package wire

import (
	stdctx "context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gh "github.com/google/go-github/v84/github"

	"github.com/vanducng/miu-cr/internal/cli"
	"github.com/vanducng/miu-cr/internal/config"
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

	headSHA         string                  // re-fetched head SHA returned by GetPR (defaults to "headsha")
	reviews         []*gh.PullRequestReview // existing reviews returned by ListReviews
	reviewThreads   []mgithub.ReviewThread
	lastReviewed    *gh.PullRequestReviewRequest // last CreateReview request, for Event assertions
	lastCheck       *gh.CreateCheckRunOptions    // last CreateCheckRun opts, for --mode checks assertions
	checkRunN       int
	createReviewErr error // injected CreateReview failure (e.g. fork 403)
}

func (f *fakeGitHub) GetPR(stdctx.Context, string, string, int) (*gh.PullRequest, error) {
	sha := f.headSHA
	if sha == "" {
		sha = "headsha"
	}
	return &gh.PullRequest{Head: &gh.PullRequestBranch{SHA: gh.Ptr(sha)}}, nil
}
func (f *fakeGitHub) ListFiles(stdctx.Context, string, string, int, *gh.ListOptions) ([]*gh.CommitFile, *gh.Response, error) {
	return nil, &gh.Response{}, nil
}
func (f *fakeGitHub) GetCommit(stdctx.Context, string, string, string) (*gh.Commit, error) {
	return nil, nil
}
func (f *fakeGitHub) ListReviews(stdctx.Context, string, string, int, *gh.ListOptions) ([]*gh.PullRequestReview, *gh.Response, error) {
	f.order = append(f.order, "list_reviews")
	return f.reviews, &gh.Response{}, nil
}

func (f *fakeGitHub) CreateReview(_ stdctx.Context, _, _ string, _ int, r *gh.PullRequestReviewRequest) (*gh.PullRequestReview, error) {
	f.order = append(f.order, "create_review")
	f.lastReviewed = r
	if f.createReviewErr != nil {
		return nil, f.createReviewErr
	}
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
func (f *fakeGitHub) ReviewThreads(stdctx.Context, string, string, int) ([]mgithub.ReviewThread, error) {
	f.order = append(f.order, "review_threads")
	return f.reviewThreads, nil
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

func (f *fakeGitHub) CreateCheckRun(_ stdctx.Context, _, _ string, opts gh.CreateCheckRunOptions) (*gh.CheckRun, error) {
	f.order = append(f.order, "create_check")
	f.checkRunN++
	o := opts
	f.lastCheck = &o
	return &gh.CheckRun{ID: gh.Ptr(int64(7))}, nil
}
func (f *fakeGitHub) UpdateCheckRun(stdctx.Context, string, string, int64, gh.UpdateCheckRunOptions) (*gh.CheckRun, error) {
	f.order = append(f.order, "update_check")
	return &gh.CheckRun{ID: gh.Ptr(int64(7))}, nil
}
func (f *fakeGitHub) ListCheckRunsForRef(stdctx.Context, string, string, string, *gh.ListCheckRunsOptions) (*gh.ListCheckRunsResults, *gh.Response, error) {
	f.order = append(f.order, "list_check")
	return &gh.ListCheckRunsResults{}, &gh.Response{}, nil
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

	// ReviewCount is set by FetchPR in production (prior runs token + 1); publishReview
	// is called directly here, so mirror that: 1 for the first run.
	info := &mgithub.PRInfo{Owner: "o", Repo: "r", Number: 7, HeadSHA: head, BaseSHA: base, BaseBranch: "main", ReviewCount: 1}
	res := engine.ReviewResult{
		Findings: []engine.Finding{
			// Anchored to the added line (new-side line 4) so it lands in a diff hunk.
			{File: "foo.go", Line: 4, Severity: "high", Category: "bug", Rationale: "boom", QuotedCode: "func B() {}"},
		},
		Stats: map[string]any{"truncation_level": "full", "files_reviewed": float64(1)},
	}

	// Upsert model: one inline posted via CreateReview with NO body; the summary is a
	// separate issue comment created before review, then finalized after it succeeds.
	pr := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, res, pr, cli.PRReviewRequest{Gate: "high"}, nil, embedWriter{}, nil, nil, testReuseKey); err != nil {
		t.Fatalf("publishReview: %v", err)
	}
	if pr.PostedInline != 1 {
		t.Fatalf("first run: want 1 inline posted, got %d", pr.PostedInline)
	}
	if pr.SummaryAction != "edited" {
		t.Fatalf("first run: want summary action edited, got %q", pr.SummaryAction)
	}
	if fake.createIssueN != 1 || fake.editN != 1 {
		t.Fatalf("first run must create then finalize the summary issue comment: create=%d edit=%d", fake.createIssueN, fake.editN)
	}
	// The review body must be EMPTY, the summary leaves the body entirely.
	if b := fake.lastReviewed.GetBody(); strings.TrimSpace(b) != "" {
		t.Fatalf("review body must be empty (summary lives in the issue comment), got:\n%s", b)
	}
	// The summary issue comment carries the marker + this run's Review attempts: 1 footer.
	if len(fake.issueComments) != 1 {
		t.Fatalf("want one summary issue comment, have %d", len(fake.issueComments))
	}
	summary := fake.issueComments[0].GetBody()
	if !strings.Contains(summary, mgithub.ReviewMarker) {
		t.Fatalf("summary comment must carry the marker:\n%s", summary)
	}
	if !strings.Contains(summary, "Review attempts: 1") {
		t.Fatalf("first-run summary must show Review attempts: 1:\n%s", summary)
	}
	if !strings.Contains(summary, "<!-- miu-cr-published:") {
		t.Fatalf("successful first-run summary must carry the published marker:\n%s", summary)
	}
	if !strings.Contains(summary, ":"+testReuseKey+" -->") {
		t.Fatalf("successful first-run summary must carry the supplied publish key:\n%s", summary)
	}
	// Lifecycle mode: the Open table, the footer timestamp, and a parseable embedded
	// ledger marker carrying this run's finding as open.
	if !strings.Contains(summary, "⚠️ Open") || !strings.Contains(summary, "Last reviewed") {
		t.Fatalf("first-run summary must render the ledger Open table + Last reviewed footer:\n%s", summary)
	}
	if led := mgithub.ParseLedger(summary); len(led) != 1 || led[0].Status != "open" {
		t.Fatalf("embedded ledger must carry the finding as open, got %+v", led)
	}
	if le, ri := indexOf(fake.order, "list_review"), indexOf(fake.order, "create_review"); le < 0 || ri < 0 || le > ri {
		t.Fatalf("ExistingFingerprints (list_review) must run before the review; order=%v", fake.order)
	}
	// Summary upsert must be posted BEFORE the inline review (it anchors above it).
	if ci, cr := indexOf(fake.order, "create_issue"), indexOf(fake.order, "create_review"); cr < 0 || ci < 0 || ci > cr {
		t.Fatalf("summary upsert (create_issue) must precede the inline review (create_review); order=%v", fake.order)
	}

	// Re-run at the same SHA: 0 new inline (fingerprint dedupe), the summary comment
	// is EDITED in place (not stacked), and the count advances to Review attempts: 2.
	fake.order = nil
	info.ReviewCount = 2 // FetchPR would read the prior token (1) and +1
	pr2 := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, res, pr2, cli.PRReviewRequest{Gate: "high"}, nil, embedWriter{}, nil, nil, testReuseKey); err != nil {
		t.Fatalf("publishReview re-run: %v", err)
	}
	if pr2.PostedInline != 0 {
		t.Fatalf("re-run: want 0 new inline, got %d", pr2.PostedInline)
	}
	if pr2.SummaryAction != "edited" {
		t.Fatalf("re-run: want summary action edited, got %q", pr2.SummaryAction)
	}
	if len(fake.reviewComments) != 1 {
		t.Errorf("re-run must not duplicate inline comments, have %d", len(fake.reviewComments))
	}
	if fake.createIssueN != 1 || fake.editN != 3 {
		t.Fatalf("re-run must EDIT (not stack) the summary: create=%d edit=%d", fake.createIssueN, fake.editN)
	}
	if len(fake.issueComments) != 1 {
		t.Fatalf("re-run must not create a second summary comment, have %d", len(fake.issueComments))
	}
	if got := fake.issueComments[0].GetBody(); !strings.Contains(got, "Review attempts: 2") {
		t.Fatalf("re-run summary must advance to Review attempts: 2:\n%s", got)
	}
}

// TestPublishReviewLedgerResolvesAcrossRuns proves the wire layer threads the
// comment-embedded ledger end-to-end: run1 opens a finding; feeding run1's posted
// body back as PriorLedger (as FetchPR does), a run2 where the finding is GONE
// flips it to resolved in the upserted summary, preserving the origin commit.
func TestPublishReviewLedgerResolvesAcrossRuns(t *testing.T) {
	runner := gitcmd.New()
	dir, base, head := setupRepo(t, runner)

	fake := &fakeGitHub{}
	restore := newGitHubClient
	newGitHubClient = func(string) mgithub.Client { return fake }
	t.Cleanup(func() { newGitHubClient = restore })
	client := newGitHubClient("")

	info := &mgithub.PRInfo{Owner: "o", Repo: "r", Number: 7, HeadSHA: head, BaseSHA: base, BaseBranch: "main", ReviewCount: 1}
	res := engine.ReviewResult{
		Findings: []engine.Finding{
			{File: "foo.go", Line: 4, Severity: "high", Category: "bug", Rationale: "boom", QuotedCode: "func B() {}"},
		},
		Stats: map[string]any{"truncation_level": "full", "files_reviewed": float64(1)},
	}
	pr := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, res, pr, cli.PRReviewRequest{Gate: "high"}, nil, embedWriter{}, nil, nil, ""); err != nil {
		t.Fatalf("run1: %v", err)
	}
	led1 := mgithub.ParseLedger(fake.issueComments[0].GetBody())
	if len(led1) != 1 || led1[0].Status != "open" {
		t.Fatalf("run1 ledger must hold one open finding, got %+v", led1)
	}
	originSHA := led1[0].OpenSHA

	// run2: feed prior ledger back; the finding is gone (foo.go still in the diff) → resolved.
	info.PriorLedger = led1
	info.ReviewCount = 2
	res2 := engine.ReviewResult{Findings: nil, Stats: map[string]any{"truncation_level": "full", "files_reviewed": float64(1)}}
	pr2 := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, res2, pr2, cli.PRReviewRequest{Gate: "high"}, nil, embedWriter{}, nil, nil, ""); err != nil {
		t.Fatalf("run2: %v", err)
	}
	body2 := fake.issueComments[0].GetBody()
	led2 := mgithub.ParseLedger(body2)
	if len(led2) != 1 || led2[0].Status != "resolved" {
		t.Fatalf("run2 must resolve the now-absent finding, got %+v", led2)
	}
	if led2[0].OpenSHA != originSHA {
		t.Fatalf("origin commit must be preserved across runs: %q vs %q", led2[0].OpenSHA, originSHA)
	}
	if !strings.Contains(body2, "Resolved (1)") {
		t.Fatalf("run2 summary must render a Resolved section:\n%s", body2)
	}
}

func TestPublishReviewResolvedGitHubThreadSuppressesLedgerOpen(t *testing.T) {
	runner := gitcmd.New()
	dir, base, head := setupRepo(t, runner)

	fake := &fakeGitHub{}
	restore := newGitHubClient
	newGitHubClient = func(string) mgithub.Client { return fake }
	t.Cleanup(func() { newGitHubClient = restore })
	client := newGitHubClient("")

	finding := engine.Finding{File: "foo.go", Line: 4, Severity: "low", Category: "bug", Rationale: "boom", QuotedCode: "func B() {}", Title: "Last-team-wins for clients on multiple teams"}
	info := &mgithub.PRInfo{Owner: "o", Repo: "r", Number: 7, HeadSHA: head, BaseSHA: base, BaseBranch: "main", ReviewCount: 1}
	res := engine.ReviewResult{
		Findings: []engine.Finding{finding},
		Stats:    map[string]any{"truncation_level": "full", "files_reviewed": float64(1)},
	}
	pr := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, res, pr, cli.PRReviewRequest{Gate: "low"}, nil, embedWriter{}, nil, nil, ""); err != nil {
		t.Fatalf("run1: %v", err)
	}
	led1 := mgithub.ParseLedger(fake.issueComments[0].GetBody())
	if len(led1) != 1 || led1[0].Status != "open" {
		t.Fatalf("run1 ledger must hold one open finding, got %+v", led1)
	}

	info.PriorLedger = led1
	info.ReviewCount = 2
	fake.reviewThreads = []mgithub.ReviewThread{{
		Resolved: true,
		Comments: []mgithub.ReviewThreadComment{{
			Body: "prior bot finding\n\n<!-- miucr:fp=" + mgithub.Fingerprint(finding) + " -->",
		}},
	}}

	pr2 := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, res, pr2, cli.PRReviewRequest{Gate: "low"}, nil, embedWriter{}, nil, nil, ""); err != nil {
		t.Fatalf("run2: %v", err)
	}
	if pr2.PostedInline != 0 {
		t.Fatalf("resolved GitHub thread must suppress reposting, got %d inline", pr2.PostedInline)
	}
	body2 := fake.issueComments[0].GetBody()
	led2 := mgithub.ParseLedger(body2)
	if len(led2) != 1 || led2[0].Status != "resolved" {
		t.Fatalf("resolved GitHub thread must flip ledger entry to resolved, got %+v", led2)
	}
	if strings.Contains(body2, "Open (1)") {
		t.Fatalf("resolved GitHub thread must not remain in the Open table:\n%s", body2)
	}
	if !strings.Contains(body2, "Resolved (1)") {
		t.Fatalf("resolved GitHub thread must render in the Resolved table:\n%s", body2)
	}
}

// TestPublishReviewForkFallback: a fork PR whose inline CreateReview 403s under
// Actions degrades to ::error:: annotations; publishReview returns fork_fallback,
// posts nothing, and does NOT attempt the summary upsert (it would 403 too).
func TestPublishReviewForkFallback(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	runner := gitcmd.New()
	dir, base, head := setupRepo(t, runner)

	fake := &fakeGitHub{createReviewErr: &gh.ErrorResponse{
		Response: &http.Response{StatusCode: 403},
		Message:  "Resource not accessible by integration",
	}}
	restore := newGitHubClient
	newGitHubClient = func(string) mgithub.Client { return fake }
	t.Cleanup(func() { newGitHubClient = restore })
	client := newGitHubClient("")

	var actionsOut strings.Builder
	info := &mgithub.PRInfo{Owner: "o", Repo: "r", Number: 7, HeadSHA: head, BaseSHA: base, BaseBranch: "main", IsFork: true, ReviewCount: 1}
	res := engine.ReviewResult{
		Findings: []engine.Finding{findingB()},
		Stats:    map[string]any{"truncation_level": "full", "files_reviewed": float64(1)},
	}
	pr := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, res, pr, cli.PRReviewRequest{Gate: "high", ActionsOut: &actionsOut}, nil, embedWriter{}, nil, nil, ""); err != nil {
		t.Fatalf("fork fallback must not hard-fail: %v", err)
	}
	if pr.SummaryAction != "fork_fallback" || pr.Posted {
		t.Fatalf("want fork_fallback/not-posted, got action=%q posted=%v", pr.SummaryAction, pr.Posted)
	}
	if pr.FallbackAnnotations == 0 {
		t.Fatalf("fork fallback must emit ::error:: annotations, got %d", pr.FallbackAnnotations)
	}
	if fake.createIssueN != 0 || fake.editN != 0 {
		t.Fatalf("fork fallback must NOT attempt the summary upsert: create=%d edit=%d", fake.createIssueN, fake.editN)
	}
}

func TestPublishChecksWireFlow(t *testing.T) {
	runner := gitcmd.New()
	dir, base, head := setupRepo(t, runner)

	fake := &fakeGitHub{}
	restore := newGitHubClient
	newGitHubClient = func(string) mgithub.Client { return fake }
	t.Cleanup(func() { newGitHubClient = restore })
	client := newGitHubClient("")

	info := &mgithub.PRInfo{Owner: "o", Repo: "r", Number: 7, HeadSHA: head, BaseSHA: base, BaseBranch: "main"}
	res := engine.ReviewResult{
		Findings: []engine.Finding{
			{File: "foo.go", Line: 4, Severity: "high", Category: "bug", Rationale: "boom", QuotedCode: "func B() {}"},
		},
		Stats: map[string]any{"truncation_level": "full", "files_reviewed": float64(1)},
	}

	pr := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, res, pr, cli.PRReviewRequest{Gate: "high", Mode: "checks"}, nil, embedWriter{}, nil, nil, ""); err != nil {
		t.Fatalf("publishReview (checks): %v", err)
	}
	if pr.Mode != "checks" || !pr.Posted {
		t.Fatalf("want checks mode posted, got mode=%q posted=%v", pr.Mode, pr.Posted)
	}
	if pr.CheckConclusion != "failure" {
		t.Fatalf("a high finding at gate high must conclude failure, got %q", pr.CheckConclusion)
	}
	if pr.PostedInline != 1 {
		t.Fatalf("want 1 annotation, got %d", pr.PostedInline)
	}
	if fake.checkRunN != 1 || fake.lastCheck == nil {
		t.Fatalf("want exactly one CreateCheckRun, got %d", fake.checkRunN)
	}
	if fake.lastCheck.HeadSHA != head {
		t.Fatalf("CheckRun must anchor at head SHA, got %q", fake.lastCheck.HeadSHA)
	}
	// checks mode posts no inline review and no summary comment.
	if fake.createReviewN != 0 || fake.createIssueN != 0 {
		t.Fatalf("checks mode must not post a review/summary: review=%d issue=%d", fake.createReviewN, fake.createIssueN)
	}
}

func TestPublishChecksSubagentDegradedFails(t *testing.T) {
	runner := gitcmd.New()
	dir, base, head := setupRepo(t, runner)

	fake := &fakeGitHub{}
	restore := newGitHubClient
	newGitHubClient = func(string) mgithub.Client { return fake }
	t.Cleanup(func() { newGitHubClient = restore })
	client := newGitHubClient("")

	info := &mgithub.PRInfo{Owner: "o", Repo: "r", Number: 7, HeadSHA: head, BaseSHA: base, BaseBranch: "main"}
	res := engine.ReviewResult{
		Stats: map[string]any{"truncation_level": "full", "files_reviewed": float64(1), "subagents_degraded": true},
	}

	pr := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, res, pr, cli.PRReviewRequest{Gate: "high", Mode: "checks"}, nil, embedWriter{}, nil, nil, ""); err != nil {
		t.Fatalf("publishReview (checks): %v", err)
	}
	if pr.CheckConclusion != "failure" {
		t.Fatalf("degraded subagent run must conclude failure, got %q", pr.CheckConclusion)
	}
	if pr.PostedInline != 0 {
		t.Fatalf("want 0 annotations, got %d", pr.PostedInline)
	}
}

// The checks reporter must feed semantic recall too: the annotated findings' code
// anchors are embedded + upserted just like the inline path's posted findings.
func TestPublishChecksFeedsEmbeddingWriter(t *testing.T) {
	runner := gitcmd.New()
	dir, base, head := setupRepo(t, runner)
	_, client := wireFake(t)
	info := &mgithub.PRInfo{Owner: "o", Repo: "r", Number: 7, HeadSHA: head, BaseSHA: base, BaseBranch: "main"}
	res := engine.ReviewResult{
		Findings: []engine.Finding{findingB()}, // anchored at new-side line 4
		Stats:    map[string]any{"truncation_level": "full", "files_reviewed": float64(1)},
	}
	emb := newCaptureEmbedder()
	st := &fakeEmbeddingStore{}
	ew := embedWriter{emb: emb, store: st, repo: "o/r"}

	pr := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, res, pr, cli.PRReviewRequest{Gate: "high", Mode: "checks"}, nil, ew, nil, nil, ""); err != nil {
		t.Fatalf("publishReview (checks): %v", err)
	}
	if pr.Mode != "checks" || pr.PostedInline != 1 {
		t.Fatalf("checks mode must annotate the in-hunk finding, mode=%q inline=%d", pr.Mode, pr.PostedInline)
	}
	if len(st.upserted) != 1 {
		t.Fatalf("checks path must feed the embedding writer, got %d upserts", len(st.upserted))
	}
	if v, _ := res.Stats["semantic_write"].(string); v != "upserted=1" {
		t.Fatalf("semantic_write stat: want upserted=1, got %v", res.Stats["semantic_write"])
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

// cleanReviewResult builds a no-finding result with ≥1 file reviewed so the
// approve resolver's gateClean + reviewedFiles predicates can hold.
func cleanReviewResult() engine.ReviewResult {
	return engine.ReviewResult{
		Findings: nil,
		Stats:    map[string]any{"truncation_level": "full", "files_reviewed": float64(1)},
	}
}

func TestPublishReviewApprovalClean(t *testing.T) {
	runner := gitcmd.New()
	dir, base, head := setupRepo(t, runner)

	fake := &fakeGitHub{headSHA: head} // re-fetched head matches → head unchanged
	restore := newGitHubClient
	newGitHubClient = func(string) mgithub.Client { return fake }
	t.Cleanup(func() { newGitHubClient = restore })
	client := newGitHubClient("")

	// Clean, non-fork, trusted author → APPROVE.
	info := &mgithub.PRInfo{Owner: "o", Repo: "r", Number: 7, HeadSHA: head, BaseSHA: base, BaseBranch: "main", AuthorAssociation: "MEMBER"}
	pr := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, cleanReviewResult(), pr, cli.PRReviewRequest{Gate: "high", Approval: config.ApprovalPolicy{Mode: "clean"}}, nil, embedWriter{}, nil, nil, ""); err != nil {
		t.Fatalf("publishReview: %v", err)
	}
	if pr.ApproveAction != "approved" || pr.ApproveReason != "approved" {
		t.Fatalf("clean trusted non-fork PR must APPROVE, got action=%q reason=%q", pr.ApproveAction, pr.ApproveReason)
	}
	if fake.lastReviewed == nil || fake.lastReviewed.GetEvent() != "APPROVE" {
		t.Fatalf("CreateReview Event must be APPROVE, got %v", fake.lastReviewed)
	}
	// The APPROVE review carries no body; the summary still upserts as an issue comment.
	if strings.TrimSpace(fake.lastReviewed.GetBody()) != "" {
		t.Fatalf("APPROVE review body must be empty (summary lives in the issue comment), got:\n%s", fake.lastReviewed.GetBody())
	}
	if pr.SummaryAction != "edited" || fake.createIssueN != 1 || fake.editN != 1 {
		t.Fatalf("clean approve must still upsert the summary: action=%q create=%d edit=%d", pr.SummaryAction, fake.createIssueN, fake.editN)
	}
}

func TestPublishReviewApprovalThreshold(t *testing.T) {
	runner := gitcmd.New()
	dir, base, head := setupRepo(t, runner)

	fake := &fakeGitHub{headSHA: head}
	restore := newGitHubClient
	newGitHubClient = func(string) mgithub.Client { return fake }
	t.Cleanup(func() { newGitHubClient = restore })
	client := newGitHubClient("")

	info := &mgithub.PRInfo{Owner: "o", Repo: "r", Number: 7, HeadSHA: head, BaseSHA: base, BaseBranch: "main", AuthorAssociation: "MEMBER"}
	res := engine.ReviewResult{
		Findings: []engine.Finding{{File: "foo.go", Line: 4, Severity: "low", Category: "style", Rationale: "minor issue", QuotedCode: "func B() {}"}},
		Stats:    map[string]any{"truncation_level": "full", "files_reviewed": float64(1)},
	}
	req := cli.PRReviewRequest{Gate: "high", Approval: config.ApprovalPolicy{Mode: "threshold", MaxSeverity: "low", Note: "on_findings"}}
	pr := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, res, pr, req, nil, embedWriter{}, nil, nil, ""); err != nil {
		t.Fatalf("publishReview: %v", err)
	}
	if pr.ApproveAction != "approved" || pr.ApproveReason != "approved" {
		t.Fatalf("threshold-approved PR must APPROVE, got action=%q reason=%q", pr.ApproveAction, pr.ApproveReason)
	}
	if fake.lastReviewed == nil || fake.lastReviewed.GetEvent() != "APPROVE" {
		t.Fatalf("CreateReview Event must be APPROVE, got %v", fake.lastReviewed)
	}
	if !strings.Contains(fake.lastReviewed.GetBody(), "at or below `low`") {
		t.Fatalf("threshold approval must include a review body note, got:\n%s", fake.lastReviewed.GetBody())
	}
}

func TestPublishReviewApproveDegradesSubagentFailure(t *testing.T) {
	runner := gitcmd.New()
	dir, base, head := setupRepo(t, runner)

	fake := &fakeGitHub{headSHA: head}
	restore := newGitHubClient
	newGitHubClient = func(string) mgithub.Client { return fake }
	t.Cleanup(func() { newGitHubClient = restore })
	client := newGitHubClient("")

	res := cleanReviewResult()
	res.Stats["subagents_degraded"] = true
	info := &mgithub.PRInfo{Owner: "o", Repo: "r", Number: 7, HeadSHA: head, BaseSHA: base, BaseBranch: "main", AuthorAssociation: "MEMBER"}
	pr := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, res, pr, cli.PRReviewRequest{Gate: "high", Approval: config.ApprovalPolicy{Mode: "clean"}}, nil, embedWriter{}, nil, nil, ""); err != nil {
		t.Fatalf("publishReview: %v", err)
	}
	if pr.ApproveAction != "commented" || pr.ApproveReason != "gate_failed" {
		t.Fatalf("degraded subagent run must not approve, got action=%q reason=%q", pr.ApproveAction, pr.ApproveReason)
	}
}

func TestPublishReviewApproveDegradesFork(t *testing.T) {
	runner := gitcmd.New()
	dir, base, head := setupRepo(t, runner)

	fake := &fakeGitHub{headSHA: head}
	restore := newGitHubClient
	newGitHubClient = func(string) mgithub.Client { return fake }
	t.Cleanup(func() { newGitHubClient = restore })
	client := newGitHubClient("")

	// Fork → COMMENT with reason "fork", never APPROVE.
	info := &mgithub.PRInfo{Owner: "o", Repo: "r", Number: 7, HeadSHA: head, BaseSHA: base, BaseBranch: "main", AuthorAssociation: "MEMBER", IsFork: true}
	pr := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, cleanReviewResult(), pr, cli.PRReviewRequest{Gate: "high", Approval: config.ApprovalPolicy{Mode: "clean"}}, nil, embedWriter{}, nil, nil, ""); err != nil {
		t.Fatalf("publishReview: %v", err)
	}
	if pr.ApproveAction != "commented" || pr.ApproveReason != "fork" {
		t.Fatalf("fork must degrade to commented/fork, got action=%q reason=%q", pr.ApproveAction, pr.ApproveReason)
	}
	if fake.lastReviewed != nil && fake.lastReviewed.GetEvent() == "APPROVE" {
		t.Fatalf("fork must never submit APPROVE")
	}
}

func TestPublishReviewApproveDegradesUntrusted(t *testing.T) {
	runner := gitcmd.New()
	dir, base, head := setupRepo(t, runner)

	fake := &fakeGitHub{headSHA: head}
	restore := newGitHubClient
	newGitHubClient = func(string) mgithub.Client { return fake }
	t.Cleanup(func() { newGitHubClient = restore })
	client := newGitHubClient("")

	info := &mgithub.PRInfo{Owner: "o", Repo: "r", Number: 7, HeadSHA: head, BaseSHA: base, BaseBranch: "main", AuthorAssociation: "FIRST_TIME_CONTRIBUTOR"}
	pr := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, cleanReviewResult(), pr, cli.PRReviewRequest{Gate: "high", Approval: config.ApprovalPolicy{Mode: "clean"}}, nil, embedWriter{}, nil, nil, ""); err != nil {
		t.Fatalf("publishReview: %v", err)
	}
	if pr.ApproveAction != "commented" || pr.ApproveReason != "untrusted_author" {
		t.Fatalf("untrusted author must degrade, got action=%q reason=%q", pr.ApproveAction, pr.ApproveReason)
	}
}

func TestPublishReviewApproveDefaultOff(t *testing.T) {
	runner := gitcmd.New()
	dir, base, head := setupRepo(t, runner)

	fake := &fakeGitHub{headSHA: head}
	restore := newGitHubClient
	newGitHubClient = func(string) mgithub.Client { return fake }
	t.Cleanup(func() { newGitHubClient = restore })
	client := newGitHubClient("")

	// Even a clean trusted non-fork PR is COMMENT when approval is OFF.
	info := &mgithub.PRInfo{Owner: "o", Repo: "r", Number: 7, HeadSHA: head, BaseSHA: base, BaseBranch: "main", AuthorAssociation: "MEMBER"}
	pr := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, cleanReviewResult(), pr, cli.PRReviewRequest{Gate: "high"}, nil, embedWriter{}, nil, nil, ""); err != nil {
		t.Fatalf("publishReview: %v", err)
	}
	if pr.ApproveAction != "commented" || pr.ApproveReason != "not_requested" {
		t.Fatalf("flag off must be commented/not_requested, got action=%q reason=%q", pr.ApproveAction, pr.ApproveReason)
	}
	// Empty review (no inline, no body, COMMENT): the empty-review guard skips
	// CreateReview entirely (no 422), but the summary still upserts.
	if fake.createReviewN != 0 {
		t.Fatalf("a no-inline COMMENT review must not call CreateReview (empty-guard), got %d", fake.createReviewN)
	}
	if pr.SummaryAction != "edited" || fake.createIssueN != 1 || fake.editN != 1 {
		t.Fatalf("empty review must still upsert the summary: action=%q create=%d edit=%d", pr.SummaryAction, fake.createIssueN, fake.editN)
	}
}

func TestPublishReviewSuggestCount(t *testing.T) {
	runner := gitcmd.New()
	dir, base, head := setupRepo(t, runner)

	fake := &fakeGitHub{headSHA: head}
	restore := newGitHubClient
	newGitHubClient = func(string) mgithub.Client { return fake }
	t.Cleanup(func() { newGitHubClient = restore })
	client := newGitHubClient("")

	info := &mgithub.PRInfo{Owner: "o", Repo: "r", Number: 7, HeadSHA: head, BaseSHA: base, BaseBranch: "main"}
	// The added line (new-side 4) is "func B() {}"; a single-line verbatim-replacing
	// patch at/above the medium floor must emit one native suggestion.
	res := engine.ReviewResult{
		Findings: []engine.Finding{
			{File: "foo.go", Line: 4, Severity: "high", Category: "bug", Rationale: "boom", QuotedCode: "func B() {}", SuggestedPatch: "func B() int { return 0 }"},
		},
		Stats: map[string]any{"truncation_level": "full", "files_reviewed": float64(1)},
	}

	// With --suggest OFF: a patch is shown as a plain hint, suggestions_posted=0.
	prOff := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, res, prOff, cli.PRReviewRequest{Gate: "high"}, nil, embedWriter{}, nil, nil, ""); err != nil {
		t.Fatalf("publishReview suggest-off: %v", err)
	}
	if prOff.SuggestionsPosted != 0 {
		t.Fatalf("suggest OFF must post 0 native suggestions, got %d", prOff.SuggestionsPosted)
	}

	// Fresh fake so the fingerprint dedupe doesn't skip the re-post.
	fake2 := &fakeGitHub{headSHA: head}
	newGitHubClient = func(string) mgithub.Client { return fake2 }
	client2 := newGitHubClient("")
	prOn := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client2, runner, dir, info, res, prOn, cli.PRReviewRequest{Gate: "high", Suggest: true}, nil, embedWriter{}, nil, nil, ""); err != nil {
		t.Fatalf("publishReview suggest-on: %v", err)
	}
	if prOn.SuggestionsPosted != 1 {
		t.Fatalf("suggest ON must post 1 native suggestion, got %d", prOn.SuggestionsPosted)
	}
}

// patchRepairedCount maps the engine's patch_repair stat onto the data.pr envelope
// field. The repair loop records counts as float64; OFF leaves the stat absent so
// the count is 0 (omitempty drops the field).
func TestPatchRepairedCount(t *testing.T) {
	cases := []struct {
		name  string
		stats map[string]any
		want  int
	}{
		{"off: stat absent", map[string]any{"truncation_level": "full"}, 0},
		{"on: repaired 2", map[string]any{"patch_repair": map[string]any{"attempted": float64(3), "repaired": float64(2)}}, 2},
		{"on: repaired 0", map[string]any{"patch_repair": map[string]any{"attempted": float64(1), "repaired": float64(0)}}, 0},
		{"mistyped stat", map[string]any{"patch_repair": "nope"}, 0},
		{"nil stats", nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := patchRepairedCount(tc.stats); got != tc.want {
				t.Fatalf("patchRepairedCount=%d want %d", got, tc.want)
			}
		})
	}
}

func TestRetryTransient(t *testing.T) {
	defer func(b time.Duration) { retryBackoffBase = b }(retryBackoffBase)
	retryBackoffBase = time.Millisecond // keep the test off real wall-clock backoff
	// Retryable error: retried until it succeeds, within the attempt budget.
	calls := 0
	err := retryTransient(stdctx.Background(), 3, func() error {
		calls++
		if calls < 2 {
			return &cli.CLIError{Code: "github.unavailable", Retry: true}
		}
		return nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("retryable error must retry to success: err=%v calls=%d", err, calls)
	}
	// Non-retryable error: returned immediately, no retry.
	calls = 0
	err = retryTransient(stdctx.Background(), 3, func() error {
		calls++
		return &cli.CLIError{Code: "config.invalid", Retry: false}
	})
	if err == nil || calls != 1 {
		t.Fatalf("non-retryable error must not retry: err=%v calls=%d", err, calls)
	}
}
