package github

import (
	stdctx "context"
	"errors"
	"strings"
	"testing"

	gh "github.com/google/go-github/v84/github"

	"github.com/vanducng/miu-cr/internal/config"
)

const deferredSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func deferredInfo() *PRInfo {
	return &PRInfo{
		Owner:             "o",
		Repo:              "r",
		Number:            1,
		HeadSHA:           deferredSHA,
		AuthorAssociation: "MEMBER",
		HTMLBase:          "https://github.com/o/r",
	}
}

func greenCheck() *gh.CheckRun {
	return &gh.CheckRun{Name: gh.Ptr("build"), Status: gh.Ptr("completed"), Conclusion: gh.Ptr("success")}
}

func deferredClient(body string, runs []*gh.CheckRun) *recordClient {
	return &recordClient{
		headSHA: deferredSHA,
		issueComments: [][]*gh.IssueComment{{
			{ID: gh.Ptr(int64(9)), Body: gh.Ptr(body)},
		}},
		existingCheckRuns: runs,
	}
}

func legacyWaitingSummary() string {
	return strings.Join([]string{
		ReviewMarker,
		runsCountToken(2),
		publishedToken(deferredSHA, "0123456789abcdef"),
		"## Code Review Summary",
		"",
		"**Result:** Review passed! Still clean on this pass.",
		"",
		approvalChecksPrefix + " Fix any failures, then update the pull request.",
		"",
		"**What changed:**",
		"- A test job invokes the runner directly.",
		"",
		renderLedgerMarker([]LedgerEntry{}),
		"",
	}, "\n")
}

func TestRetryDeferredApprovalApprovesLegacyWaitingSummary(t *testing.T) {
	c := deferredClient(legacyWaitingSummary(), []*gh.CheckRun{greenCheck()})
	res, err := RetryDeferredApproval(stdctx.Background(), c, deferredInfo(), config.ApprovalPolicy{Mode: "clean"})
	if err != nil {
		t.Fatalf("RetryDeferredApproval: %v", err)
	}
	if !res.Approved || !res.Cleared || res.Reason != approveReasonApproved {
		t.Fatalf("got approved=%v cleared=%v reason=%q", res.Approved, res.Cleared, res.Reason)
	}
	if c.gotReview.GetEvent() != "APPROVE" || c.gotReview.GetCommitID() != deferredSHA {
		t.Fatalf("approval review = %+v", c.gotReview)
	}
	if strings.Contains(c.editedBody, "Waiting for CI") || strings.Contains(c.editedBody, "miu-cr-approval:") {
		t.Fatalf("waiting notice still in summary:\n%s", c.editedBody)
	}
	if !strings.Contains(c.editedBody, "Still clean on this pass.") || !strings.Contains(c.editedBody, "A test job invokes the runner directly.") {
		t.Fatalf("summary lost its review text:\n%s", c.editedBody)
	}
}

func TestRetryDeferredApprovalApprovesRenderedMarker(t *testing.T) {
	info := deferredInfo()
	body := RenderSummaryFull(info, nil, nil, 0, nil, nil, SummaryOptions{
		ApprovalReason: approveReasonChecksNotGreen,
		Published:      true,
		PublishKey:     "0123456789abcdef",
	})
	if !strings.Contains(body, approvalReasonToken(approveReasonChecksNotGreen)) {
		t.Fatalf("rendered summary missing deferral marker:\n%s", body)
	}
	c := deferredClient(body, []*gh.CheckRun{greenCheck()})
	res, err := RetryDeferredApproval(stdctx.Background(), c, info, config.ApprovalPolicy{Mode: "clean"})
	if err != nil {
		t.Fatalf("RetryDeferredApproval: %v", err)
	}
	if !res.Approved || strings.Contains(c.editedBody, approvalChecksPrefix) {
		t.Fatalf("approved=%v body=\n%s", res.Approved, c.editedBody)
	}
}

func TestRetryDeferredApprovalWaitsUntilChecksPass(t *testing.T) {
	for _, tc := range []struct {
		name   string
		runs   []*gh.CheckRun
		status []*gh.RepoStatus
	}{
		{name: "pending", runs: []*gh.CheckRun{{Name: gh.Ptr("build"), Status: gh.Ptr("in_progress")}}},
		{name: "failed", runs: []*gh.CheckRun{{Name: gh.Ptr("build"), Status: gh.Ptr("completed"), Conclusion: gh.Ptr("failure")}}},
		{name: "commit status", status: []*gh.RepoStatus{{Context: gh.Ptr("ci"), State: gh.Ptr("failure")}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := deferredClient(legacyWaitingSummary(), tc.runs)
			c.combinedStatuses = tc.status
			res, err := RetryDeferredApproval(stdctx.Background(), c, deferredInfo(), config.ApprovalPolicy{Mode: "clean"})
			if err != nil {
				t.Fatalf("RetryDeferredApproval: %v", err)
			}
			if res.Approved || res.Cleared || c.createReviewN != 0 || c.editN != 0 {
				t.Fatalf("non-green checks approved or edited the summary: %+v reviews=%d edits=%d", res, c.createReviewN, c.editN)
			}
			if res.Reason != approveReasonChecksNotGreen {
				t.Fatalf("reason = %q", res.Reason)
			}
		})
	}
}

func TestRetryDeferredApprovalRefusesOpenFindingOnCleanPolicy(t *testing.T) {
	body := strings.Replace(legacyWaitingSummary(), renderLedgerMarker([]LedgerEntry{}), renderLedgerMarker([]LedgerEntry{{
		FP: "aaaaaaaaaaaaaaaa", Path: "a.go", Status: statusOpen, Sev: "high", FirstSev: "high",
	}}), 1)
	c := deferredClient(body, []*gh.CheckRun{greenCheck()})
	res, err := RetryDeferredApproval(stdctx.Background(), c, deferredInfo(), config.ApprovalPolicy{Mode: "clean"})
	if err != nil {
		t.Fatalf("RetryDeferredApproval: %v", err)
	}
	if res.Approved || c.createReviewN != 0 || res.Reason != approveReasonThresholdFailed {
		t.Fatalf("open finding was approved: %+v reviews=%d", res, c.createReviewN)
	}
}

func TestRetryDeferredApprovalThresholdAllowsInfoFinding(t *testing.T) {
	body := strings.Replace(legacyWaitingSummary(), renderLedgerMarker([]LedgerEntry{}), renderLedgerMarker([]LedgerEntry{{
		FP: "aaaaaaaaaaaaaaaa", Path: "a.go", Status: statusOpen, Sev: "info", FirstSev: "info",
	}}), 1)
	c := deferredClient(body, []*gh.CheckRun{greenCheck()})
	res, err := RetryDeferredApproval(stdctx.Background(), c, deferredInfo(), config.ApprovalPolicy{Mode: "threshold"})
	if err != nil {
		t.Fatalf("RetryDeferredApproval: %v", err)
	}
	if !res.Approved || c.gotReview.GetEvent() != "APPROVE" {
		t.Fatalf("P4 finding should approve under the default threshold: %+v", res)
	}
}

func TestRetryDeferredApprovalSkipsUnsafeSummaries(t *testing.T) {
	moved := strings.Replace(legacyWaitingSummary(), deferredSHA, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 1)
	conflict := strings.Replace(legacyWaitingSummary(), approvalChecksPrefix+" Fix any failures, then update the pull request.", "> **Approval:** Resolve the merge conflicts, then update the pull request.", 1)
	for _, tc := range []struct {
		name   string
		body   string
		policy config.ApprovalPolicy
		reason string
	}{
		{name: "approval off", body: legacyWaitingSummary(), policy: config.ApprovalPolicy{}, reason: approveReasonNotRequested},
		{name: "head moved", body: moved, policy: config.ApprovalPolicy{Mode: "clean"}, reason: approveReasonHeadMoved},
		{name: "merge conflict is not deferred", body: conflict, policy: config.ApprovalPolicy{Mode: "clean"}, reason: "not_deferred"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := deferredClient(tc.body, []*gh.CheckRun{greenCheck()})
			res, err := RetryDeferredApproval(stdctx.Background(), c, deferredInfo(), tc.policy)
			if err != nil {
				t.Fatalf("RetryDeferredApproval: %v", err)
			}
			if res.Approved || c.createReviewN != 0 || res.Reason != tc.reason {
				t.Fatalf("got %+v reviews=%d, want reason %q", res, c.createReviewN, tc.reason)
			}
		})
	}
}

func TestRetryDeferredApprovalClearsNoticeWhenAlreadyApproved(t *testing.T) {
	c := deferredClient(legacyWaitingSummary(), []*gh.CheckRun{greenCheck()})
	c.reviews = []*gh.PullRequestReview{{State: gh.Ptr("APPROVED"), CommitID: gh.Ptr(deferredSHA)}}
	res, err := RetryDeferredApproval(stdctx.Background(), c, deferredInfo(), config.ApprovalPolicy{Mode: "clean"})
	if err != nil {
		t.Fatalf("RetryDeferredApproval: %v", err)
	}
	if res.Approved || !res.Cleared || c.createReviewN != 0 {
		t.Fatalf("already-approved head created another review: %+v reviews=%d", res, c.createReviewN)
	}
	if strings.Contains(c.editedBody, approvalChecksPrefix) {
		t.Fatalf("waiting notice kept after an existing approval:\n%s", c.editedBody)
	}
}

func TestRetryDeferredApprovalReportsSummaryEditFailure(t *testing.T) {
	c := deferredClient(legacyWaitingSummary(), []*gh.CheckRun{greenCheck()})
	c.editErr = errors.New("edit failed")
	res, err := RetryDeferredApproval(stdctx.Background(), c, deferredInfo(), config.ApprovalPolicy{Mode: "clean"})
	if err == nil || !res.Approved || res.Reason != "summary_edit_failed" {
		t.Fatalf("got approved=%v reason=%q err=%v", res.Approved, res.Reason, err)
	}
}
