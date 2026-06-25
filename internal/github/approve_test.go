package github

import (
	stdctx "context"
	"net/http"
	"testing"

	gh "github.com/google/go-github/v84/github"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/diff"
)

func TestResolveEvent(t *testing.T) {
	base := PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h", AuthorAssociation: "MEMBER"}
	on := PostReviewOptions{ApproveClean: true}

	tests := []struct {
		name          string
		opts          PostReviewOptions
		info          PRInfo
		gateClean     bool
		reviewedFiles int
		headUnchanged bool
		wantEvent     string
		wantReason    string
	}{
		{"approve all pass", on, base, true, 3, true, "APPROVE", approveReasonApproved},
		{"trusted owner", on, withAssoc(base, "OWNER"), true, 3, true, "APPROVE", approveReasonApproved},
		{"trusted collaborator", on, withAssoc(base, "COLLABORATOR"), true, 3, true, "APPROVE", approveReasonApproved},
		{"not requested", PostReviewOptions{}, base, true, 3, true, "COMMENT", approveReasonNotRequested},
		{"gate failed", on, base, false, 3, true, "COMMENT", approveReasonGateFailed},
		{"fork", on, withFork(base), true, 3, true, "COMMENT", approveReasonFork},
		{"untrusted none", on, withAssoc(base, "NONE"), true, 3, true, "COMMENT", approveReasonUntrusted},
		{"untrusted first-timer", on, withAssoc(base, "FIRST_TIMER"), true, 3, true, "COMMENT", approveReasonUntrusted},
		{"untrusted first-time-contrib", on, withAssoc(base, "FIRST_TIME_CONTRIBUTOR"), true, 3, true, "COMMENT", approveReasonUntrusted},
		{"untrusted contributor", on, withAssoc(base, "CONTRIBUTOR"), true, 3, true, "COMMENT", approveReasonUntrusted},
		{"untrusted empty fails closed", on, withAssoc(base, ""), true, 3, true, "COMMENT", approveReasonUntrusted},
		{"untrusted unknown tier", on, withAssoc(base, "MANNEQUIN"), true, 3, true, "COMMENT", approveReasonUntrusted},
		{"nothing reviewed", on, base, true, 0, true, "COMMENT", approveReasonNothingDone},
		{"head moved", on, base, true, 3, false, "COMMENT", approveReasonHeadMoved},
		{"head unknown beats unchanged", on, withHead(base, ""), true, 3, true, "COMMENT", approveReasonHeadUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev, rs := resolveEvent(tc.opts, tc.info, tc.gateClean, tc.reviewedFiles, tc.headUnchanged)
			if ev != tc.wantEvent || rs != tc.wantReason {
				t.Fatalf("got (%q,%q), want (%q,%q)", ev, rs, tc.wantEvent, tc.wantReason)
			}
		})
	}
}

func withFork(p PRInfo) PRInfo { p.IsFork = true; return p }
func withAssoc(p PRInfo, a string) PRInfo {
	p.AuthorAssociation = a
	return p
}
func withHead(p PRInfo, h string) PRInfo { p.HeadSHA = h; return p }

// cleanFinding is below the empty gate so a clean PR has no gate-failing findings.
func approveInfo() *PRInfo {
	return &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "headsha", AuthorAssociation: "MEMBER"}
}

func approveOpts() PostReviewOptions {
	return PostReviewOptions{ApproveClean: true, GateClean: true, ReviewedFiles: 2}
}

func TestPostReviewApprovesCleanPR(t *testing.T) {
	c := &recordClient{}
	res, err := PostReview(stdctx.Background(), c, approveInfo(), nil, nil, staticSummary("looks good"), nil, approveOpts())
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if res.Event != "APPROVE" || res.Reason != approveReasonApproved {
		t.Fatalf("got (%q,%q), want APPROVE/approved", res.Event, res.Reason)
	}
	if c.gotReview == nil || c.gotReview.GetEvent() != "APPROVE" {
		t.Fatalf("CreateReview Event must be APPROVE, got %+v", c.gotReview)
	}
}

func TestPostReviewApproveDegradesForkToComment(t *testing.T) {
	c := &recordClient{}
	info := approveInfo()
	info.IsFork = true
	res, err := PostReview(stdctx.Background(), c, info, nil, nil, staticSummary("review"), nil, approveOpts())
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if res.Event != "COMMENT" || res.Reason != approveReasonFork {
		t.Fatalf("fork must degrade to COMMENT/fork, got (%q,%q)", res.Event, res.Reason)
	}
	if c.gotReview.GetEvent() != "COMMENT" {
		t.Fatalf("Event must be COMMENT, got %q", c.gotReview.GetEvent())
	}
}

func TestPostReviewApproveDegradesUntrustedToComment(t *testing.T) {
	c := &recordClient{}
	info := approveInfo()
	info.AuthorAssociation = "NONE"
	res, err := PostReview(stdctx.Background(), c, info, nil, nil, staticSummary("review"), nil, approveOpts())
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if res.Event != "COMMENT" || res.Reason != approveReasonUntrusted {
		t.Fatalf("untrusted must degrade, got (%q,%q)", res.Event, res.Reason)
	}
}

func TestPostReviewApproveDegradesGateFailedToComment(t *testing.T) {
	c := &recordClient{}
	opts := approveOpts()
	opts.GateClean = false
	res, err := PostReview(stdctx.Background(), c, approveInfo(), nil, nil, staticSummary("review"), nil, opts)
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if res.Event != "COMMENT" || res.Reason != approveReasonGateFailed {
		t.Fatalf("gate-failed must degrade, got (%q,%q)", res.Event, res.Reason)
	}
}

func TestPostReviewApproveDegradesNothingReviewedToComment(t *testing.T) {
	c := &recordClient{}
	opts := approveOpts()
	opts.ReviewedFiles = 0
	res, err := PostReview(stdctx.Background(), c, approveInfo(), nil, nil, staticSummary("review"), nil, opts)
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if res.Event != "COMMENT" || res.Reason != approveReasonNothingDone {
		t.Fatalf("nothing-reviewed must degrade, got (%q,%q)", res.Event, res.Reason)
	}
}

func TestPostReviewApproveDegradesHeadMovedToComment(t *testing.T) {
	c := &recordClient{headSHA: "moved"} // GetPR returns a different head than info.HeadSHA
	res, err := PostReview(stdctx.Background(), c, approveInfo(), nil, nil, staticSummary("review"), nil, approveOpts())
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if res.Event != "COMMENT" || res.Reason != approveReasonHeadMoved {
		t.Fatalf("head-moved must degrade, got (%q,%q)", res.Event, res.Reason)
	}
}

func TestPostReviewApproveDegradesNilHeadToComment(t *testing.T) {
	c := &nilHeadClient{}
	res, err := PostReview(stdctx.Background(), c, approveInfo(), nil, nil, staticSummary("review"), nil, approveOpts())
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if res.Event != "COMMENT" || res.Reason != approveReasonHeadMoved {
		t.Fatalf("nil-head re-fetch must degrade to head_moved, got (%q,%q)", res.Event, res.Reason)
	}
}

func TestPostReviewSkipsSecondApproveAtSameSHA(t *testing.T) {
	c := &recordClient{
		reviews: []*gh.PullRequestReview{
			{State: gh.Ptr("APPROVED"), CommitID: gh.Ptr("headsha")},
		},
	}
	res, err := PostReview(stdctx.Background(), c, approveInfo(), nil, nil, staticSummary("review"), nil, approveOpts())
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if res.Event != "COMMENT" || res.Reason != approveReasonAlreadyDone {
		t.Fatalf("already-approved-at-SHA must degrade, got (%q,%q)", res.Event, res.Reason)
	}
	if c.gotReview.GetEvent() == "APPROVE" {
		t.Fatal("must not post a second APPROVE at the same head SHA")
	}
}

func TestPostReviewApproveReevaluatesAtNewSHA(t *testing.T) {
	// An APPROVED review at an OLD sha must NOT block a fresh APPROVE at headsha.
	c := &recordClient{
		reviews: []*gh.PullRequestReview{
			{State: gh.Ptr("APPROVED"), CommitID: gh.Ptr("oldsha")},
		},
	}
	res, err := PostReview(stdctx.Background(), c, approveInfo(), nil, nil, staticSummary("review"), nil, approveOpts())
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if res.Event != "APPROVE" {
		t.Fatalf("a stale-SHA approval must not block a new APPROVE, got %q", res.Event)
	}
}

func TestPostReviewSelfApprove422DegradesToComment(t *testing.T) {
	c := &recordClient{createReviewErrFirst: selfApprove422()}
	res, err := PostReview(stdctx.Background(), c, approveInfo(), nil, nil, staticSummary("review"), nil, approveOpts())
	if err != nil {
		t.Fatalf("self-approve 422 must NOT error: %v", err)
	}
	if res.Event != "COMMENT" || res.Reason != approveReasonSelfForbidden {
		t.Fatalf("self-approve must degrade, got (%q,%q)", res.Event, res.Reason)
	}
	if c.createReviewN != 2 {
		t.Fatalf("want a COMMENT retry after the 422, got %d CreateReview calls", c.createReviewN)
	}
	if last := c.gotReviews[len(c.gotReviews)-1]; last.GetEvent() != "COMMENT" {
		t.Fatalf("retry Event must be COMMENT, got %q", last.GetEvent())
	}
}

func TestPostReviewApproveListReviewsErrorDegradesToComment(t *testing.T) {
	// The idempotency check failing means we can't confirm there isn't already an
	// APPROVE → degrade to COMMENT (never a duplicate APPROVE), never an error.
	c := &recordClient{listReviewsErr: errBoom{}}
	res, err := PostReview(stdctx.Background(), c, approveInfo(), nil, nil, staticSummary("review"), nil, approveOpts())
	if err != nil {
		t.Fatalf("ListReviews error must not surface: %v", err)
	}
	if res.Event != "COMMENT" || res.Reason != approveReasonIdempotencyUnverified {
		t.Fatalf("idempotency-unverified must degrade to COMMENT, got (%q,%q)", res.Event, res.Reason)
	}
	if c.gotReview.GetEvent() == "APPROVE" {
		t.Fatal("must not post an APPROVE when idempotency is unverified")
	}
}

func TestPostReviewNoApproveWhenFlagOff(t *testing.T) {
	c := &recordClient{}
	res, err := PostReview(stdctx.Background(), c, approveInfo(), nil, nil, staticSummary("review"), nil, PostReviewOptions{})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if res.Event != "COMMENT" || res.Reason != approveReasonNotRequested {
		t.Fatalf("flag off must be COMMENT/not_requested, got (%q,%q)", res.Event, res.Reason)
	}
}

// approve gate clean uses the engine: prove a high finding fails the medium gate
// and zero findings passes, matching how the wire layer computes GateClean.
func TestGateCleanWiring(t *testing.T) {
	if engine.GateFailed(nil, "medium") {
		t.Fatal("zero findings must be clean at gate medium")
	}
	if !engine.GateFailed([]engine.Finding{{Severity: "high"}}, "medium") {
		t.Fatal("a high finding must fail the medium gate")
	}
	_ = diff.Diff{} // keep diff import for parity with sibling test files
}

// --- helpers ---

type nilHeadClient struct{ recordClient }

func (c *nilHeadClient) GetPR(stdctx.Context, string, string, int) (*gh.PullRequest, error) {
	return &gh.PullRequest{}, nil // Head == nil
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }

func selfApprove422() error {
	return &gh.ErrorResponse{
		Response: &http.Response{StatusCode: 422},
		Message:  "Can not approve your own pull request",
	}
}

// generic422 is a 422 unrelated to self-approval (e.g. a stale commit, branch
// protection); its message must NOT contain the self-approve marker.
func generic422() error {
	return &gh.ErrorResponse{
		Response: &http.Response{StatusCode: 422},
		Message:  "Unprocessable Entity: No commit found for SHA",
	}
}

func TestPostReviewNonSelfApprove422DegradesToCommentRejected(t *testing.T) {
	// A 422 that is NOT a self-approve must degrade to COMMENT/approve_rejected,
	// never be mislabeled self_approve_forbidden, and never surface as an error.
	c := &recordClient{createReviewErrFirst: generic422()}
	res, err := PostReview(stdctx.Background(), c, approveInfo(), nil, nil, staticSummary("review"), nil, approveOpts())
	if err != nil {
		t.Fatalf("a non-self 422 must degrade, not error: %v", err)
	}
	if res.Event != "COMMENT" || res.Reason != approveReasonRejected {
		t.Fatalf("want COMMENT/approve_rejected, got (%q,%q)", res.Event, res.Reason)
	}
	if c.createReviewN != 2 {
		t.Fatalf("want a COMMENT retry after the 422, got %d CreateReview calls", c.createReviewN)
	}
	if last := c.gotReviews[len(c.gotReviews)-1]; last.GetEvent() != "COMMENT" {
		t.Fatalf("retry Event must be COMMENT, got %q", last.GetEvent())
	}
}

func TestPostReviewApproveRealErrorSurfaces(t *testing.T) {
	// A genuine (non-422) CreateReview failure on the APPROVE path must surface as
	// an error and must NOT report a phantom approval in the returned result.
	c := &recordClient{createReviewErrFirst: errBoom{}}
	res, err := PostReview(stdctx.Background(), c, approveInfo(), nil, nil, staticSummary("review"), nil, approveOpts())
	if err == nil {
		t.Fatal("a real CreateReview error must surface to the caller")
	}
	if res.Event == "APPROVE" {
		t.Fatalf("must not report a phantom approval on error, got Event=%q", res.Event)
	}
	if c.createReviewN != 1 {
		t.Fatalf("a real error must not be retried as COMMENT, got %d calls", c.createReviewN)
	}
}

func TestAlreadyApprovedIgnoresEmptyCommitAgainstEmptyHead(t *testing.T) {
	// A malformed review (APPROVED, empty CommitID) must NOT match an empty
	// HeadSHA, otherwise "" == "" falsely blocks a needed APPROVE.
	c := &recordClient{
		reviews: []*gh.PullRequestReview{
			{State: gh.Ptr("APPROVED"), CommitID: gh.Ptr("")},
		},
	}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: ""}
	done, err := alreadyApproved(stdctx.Background(), c, info)
	if err != nil {
		t.Fatalf("alreadyApproved: %v", err)
	}
	if done {
		t.Fatal("empty CommitID must not match an empty HeadSHA")
	}
}

func TestPostReviewApproveDegradesEmptyHeadToCommentUnknown(t *testing.T) {
	// An empty HeadSHA makes head verification unreliable → degrade to COMMENT
	// with head_unknown, never an APPROVE on an unknown head.
	c := &recordClient{}
	info := approveInfo()
	info.HeadSHA = ""
	res, err := PostReview(stdctx.Background(), c, info, nil, nil, staticSummary("review"), nil, approveOpts())
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if res.Event != "COMMENT" || res.Reason != approveReasonHeadUnknown {
		t.Fatalf("empty head must degrade to COMMENT/head_unknown, got (%q,%q)", res.Event, res.Reason)
	}
	if c.gotReview.GetEvent() == "APPROVE" {
		t.Fatal("must not APPROVE on an unknown head")
	}
}

func TestPostReviewApprove422CommentRetryFailureZeroesPostedAndErrors(t *testing.T) {
	// APPROVE 422s → degrade to COMMENT, but the COMMENT retry itself fails. The
	// returned result must report Posted==0 (nothing landed) and surface the error.
	c := &recordClient{
		createReviewErrFirst: generic422(), // call 1 (APPROVE) → degrade to COMMENT
		createReviewErr:      errBoom{},    // call 2 (COMMENT retry) → hard failure
	}
	findings := []engine.Finding{{File: "p.go", Line: 2, Severity: "high", Category: "bug", Rationale: "boom"}}
	res, err := PostReview(stdctx.Background(), c, approveInfo(), findings, sampleDiffs(), staticSummary("review"), nil, approveOpts())
	if err == nil {
		t.Fatal("a failed COMMENT retry must surface an error")
	}
	if res.Posted != 0 {
		t.Fatalf("nothing landed → Posted must be 0, got %d", res.Posted)
	}
	if c.createReviewN != 2 {
		t.Fatalf("want APPROVE then a COMMENT retry, got %d CreateReview calls", c.createReviewN)
	}
}

func TestPostReviewApprove422EmptyDegradeSkipsPost(t *testing.T) {
	// APPROVE 422s, but with 0 inline comments AND no summary there is nothing to
	// post, skip the empty COMMENT review (GitHub would 422 it anyway).
	c := &recordClient{createReviewErrFirst: selfApprove422()}
	res, err := PostReview(stdctx.Background(), c, approveInfo(), nil, nil, staticSummary(""), nil, approveOpts())
	if err != nil {
		t.Fatalf("empty degrade must not error: %v", err)
	}
	if res.Event != "COMMENT" || res.Reason != approveReasonSelfForbidden {
		t.Fatalf("want COMMENT/self_approve_forbidden, got (%q,%q)", res.Event, res.Reason)
	}
	if res.Posted != 0 {
		t.Fatalf("nothing to post → Posted must be 0, got %d", res.Posted)
	}
	if c.createReviewN != 1 {
		t.Fatalf("must not submit an empty COMMENT review, got %d CreateReview calls", c.createReviewN)
	}
}
