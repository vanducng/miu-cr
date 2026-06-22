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

	headSHA      string                       // re-fetched head SHA returned by GetPR (defaults to "headsha")
	reviews      []*gh.PullRequestReview      // existing reviews returned by ListReviews
	lastReviewed *gh.PullRequestReviewRequest // last CreateReview request, for Event assertions
	lastCheck    *gh.CreateCheckRunOptions    // last CreateCheckRun opts, for --mode checks assertions
	checkRunN    int
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
func (f *fakeGitHub) ListReviews(stdctx.Context, string, string, int, *gh.ListOptions) ([]*gh.PullRequestReview, *gh.Response, error) {
	f.order = append(f.order, "list_reviews")
	return f.reviews, &gh.Response{}, nil
}

func (f *fakeGitHub) CreateReview(_ stdctx.Context, _, _ string, _ int, r *gh.PullRequestReviewRequest) (*gh.PullRequestReview, error) {
	f.order = append(f.order, "create_review")
	f.lastReviewed = r
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
	if err := publishReview(stdctx.Background(), client, runner, dir, info, res, pr, cli.PRReviewRequest{Gate: "high"}, nil, embedWriter{}); err != nil {
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
	if err := publishReview(stdctx.Background(), client, runner, dir, info, res, pr2, cli.PRReviewRequest{Gate: "high"}, nil, embedWriter{}); err != nil {
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
	if err := publishReview(stdctx.Background(), client, runner, dir, info, res, pr, cli.PRReviewRequest{Gate: "high", Mode: "checks"}, nil, embedWriter{}); err != nil {
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
	if err := publishReview(stdctx.Background(), client, runner, dir, info, res, pr, cli.PRReviewRequest{Gate: "high", Mode: "checks"}, nil, ew); err != nil {
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

func TestPublishReviewApproveClean(t *testing.T) {
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
	if err := publishReview(stdctx.Background(), client, runner, dir, info, cleanReviewResult(), pr, cli.PRReviewRequest{Gate: "high", ApproveClean: true}, nil, embedWriter{}); err != nil {
		t.Fatalf("publishReview: %v", err)
	}
	if pr.ApproveAction != "approved" || pr.ApproveReason != "approved" {
		t.Fatalf("clean trusted non-fork PR must APPROVE, got action=%q reason=%q", pr.ApproveAction, pr.ApproveReason)
	}
	if fake.lastReviewed == nil || fake.lastReviewed.GetEvent() != "APPROVE" {
		t.Fatalf("CreateReview Event must be APPROVE, got %v", fake.lastReviewed)
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
	if err := publishReview(stdctx.Background(), client, runner, dir, info, cleanReviewResult(), pr, cli.PRReviewRequest{Gate: "high", ApproveClean: true}, nil, embedWriter{}); err != nil {
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
	if err := publishReview(stdctx.Background(), client, runner, dir, info, cleanReviewResult(), pr, cli.PRReviewRequest{Gate: "high", ApproveClean: true}, nil, embedWriter{}); err != nil {
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

	// Even a clean trusted non-fork PR is COMMENT when --approve-clean is OFF.
	info := &mgithub.PRInfo{Owner: "o", Repo: "r", Number: 7, HeadSHA: head, BaseSHA: base, BaseBranch: "main", AuthorAssociation: "MEMBER"}
	pr := &cli.PRResult{SummaryAction: "none"}
	if err := publishReview(stdctx.Background(), client, runner, dir, info, cleanReviewResult(), pr, cli.PRReviewRequest{Gate: "high"}, nil, embedWriter{}); err != nil {
		t.Fatalf("publishReview: %v", err)
	}
	if pr.ApproveAction != "commented" || pr.ApproveReason != "not_requested" {
		t.Fatalf("flag off must be commented/not_requested, got action=%q reason=%q", pr.ApproveAction, pr.ApproveReason)
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
	if err := publishReview(stdctx.Background(), client, runner, dir, info, res, prOff, cli.PRReviewRequest{Gate: "high"}, nil, embedWriter{}); err != nil {
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
	if err := publishReview(stdctx.Background(), client2, runner, dir, info, res, prOn, cli.PRReviewRequest{Gate: "high", Suggest: true}, nil, embedWriter{}); err != nil {
		t.Fatalf("publishReview suggest-on: %v", err)
	}
	if prOn.SuggestionsPosted != 1 {
		t.Fatalf("suggest ON must post 1 native suggestion, got %d", prOn.SuggestionsPosted)
	}
}
