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
		{"not requested", PostReviewOptions{}, base, true, 3, true, "COMMENT", approveReasonNotRequested},
		{"gate failed", on, base, false, 3, true, "COMMENT", approveReasonGateFailed},
		{"fork", on, withFork(base), true, 3, true, "COMMENT", approveReasonFork},
		{"untrusted none", on, withAssoc(base, "NONE"), true, 3, true, "COMMENT", approveReasonUntrusted},
		{"untrusted first-timer", on, withAssoc(base, "FIRST_TIMER"), true, 3, true, "COMMENT", approveReasonUntrusted},
		{"untrusted first-time-contrib", on, withAssoc(base, "FIRST_TIME_CONTRIBUTOR"), true, 3, true, "COMMENT", approveReasonUntrusted},
		{"nothing reviewed", on, base, true, 0, true, "COMMENT", approveReasonNothingDone},
		{"head moved", on, base, true, 3, false, "COMMENT", approveReasonHeadMoved},
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

// cleanFinding is below the empty gate so a clean PR has no gate-failing findings.
func approveInfo() *PRInfo {
	return &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "headsha", AuthorAssociation: "MEMBER"}
}

func approveOpts() PostReviewOptions {
	return PostReviewOptions{ApproveClean: true, GateClean: true, ReviewedFiles: 2}
}

func TestPostReviewApprovesCleanPR(t *testing.T) {
	c := &recordClient{}
	res, err := PostReview(stdctx.Background(), c, approveInfo(), nil, nil, "looks good", nil, approveOpts())
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
	res, err := PostReview(stdctx.Background(), c, info, nil, nil, "review", nil, approveOpts())
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
	res, err := PostReview(stdctx.Background(), c, info, nil, nil, "review", nil, approveOpts())
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
	res, err := PostReview(stdctx.Background(), c, approveInfo(), nil, nil, "review", nil, opts)
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
	res, err := PostReview(stdctx.Background(), c, approveInfo(), nil, nil, "review", nil, opts)
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if res.Event != "COMMENT" || res.Reason != approveReasonNothingDone {
		t.Fatalf("nothing-reviewed must degrade, got (%q,%q)", res.Event, res.Reason)
	}
}

func TestPostReviewApproveDegradesHeadMovedToComment(t *testing.T) {
	c := &recordClient{headSHA: "moved"} // GetPR returns a different head than info.HeadSHA
	res, err := PostReview(stdctx.Background(), c, approveInfo(), nil, nil, "review", nil, approveOpts())
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if res.Event != "COMMENT" || res.Reason != approveReasonHeadMoved {
		t.Fatalf("head-moved must degrade, got (%q,%q)", res.Event, res.Reason)
	}
}

func TestPostReviewApproveDegradesNilHeadToComment(t *testing.T) {
	c := &nilHeadClient{}
	res, err := PostReview(stdctx.Background(), c, approveInfo(), nil, nil, "review", nil, approveOpts())
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
	res, err := PostReview(stdctx.Background(), c, approveInfo(), nil, nil, "review", nil, approveOpts())
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
	res, err := PostReview(stdctx.Background(), c, approveInfo(), nil, nil, "review", nil, approveOpts())
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if res.Event != "APPROVE" {
		t.Fatalf("a stale-SHA approval must not block a new APPROVE, got %q", res.Event)
	}
}

func TestPostReviewSelfApprove422DegradesToComment(t *testing.T) {
	c := &recordClient{createReviewErrN: selfApprove422()}
	res, err := PostReview(stdctx.Background(), c, approveInfo(), nil, nil, "review", nil, approveOpts())
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

func TestPostReviewApproveSurvivesListReviewsError(t *testing.T) {
	// A read failure on the idempotency check must not block APPROVE (degrade, not error).
	c := &recordClient{listReviewsErr: errBoom{}}
	res, err := PostReview(stdctx.Background(), c, approveInfo(), nil, nil, "review", nil, approveOpts())
	if err != nil {
		t.Fatalf("ListReviews error must not surface: %v", err)
	}
	if res.Event != "APPROVE" {
		t.Fatalf("a ListReviews read error must still allow APPROVE, got %q", res.Event)
	}
}

func TestPostReviewNoApproveWhenFlagOff(t *testing.T) {
	c := &recordClient{}
	res, err := PostReview(stdctx.Background(), c, approveInfo(), nil, nil, "review", nil, PostReviewOptions{})
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
