package github

import (
	stdctx "context"
	"fmt"
	"strings"

	gh "github.com/google/go-github/v84/github"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine"
)

// Approve reason codes recorded in PRResult when the resolver degrades APPROVE to
// COMMENT. All are non-fatal: a precondition miss never fails a run.
const (
	approveReasonApproved              = "approved"
	approveReasonNotRequested          = "not_requested"
	approveReasonGateFailed            = "gate_failed"
	approveReasonThresholdFailed       = "findings_above_approval_threshold"
	approveReasonFork                  = "fork"
	approveReasonUntrusted             = "untrusted_author"
	approveReasonNothingDone           = "nothing_reviewed"
	approveReasonHeadUnknown           = "head_unknown"
	approveReasonHeadMoved             = "head_moved"
	approveReasonAlreadyDone           = "already_approved"
	approveReasonSelfForbidden         = "self_approve_forbidden"
	approveReasonForbidden             = "approve_forbidden"
	approveReasonRejected              = "approve_rejected"
	approveReasonIdempotencyUnverified = "idempotency_unverified"
)

const defaultApprovalMaxPriority = "P4"

// trustedAssociations are the only AuthorAssociation values we auto-approve. This
// is a fail-CLOSED allowlist, not a denylist: an empty string (API didn't populate
// it / missing scope / contract change), NONE, CONTRIBUTOR, a first-timer tier, or
// any future low-trust value GitHub may add are all untrusted by default. Forks are
// excluded separately; this guards the same-repo low-trust author case.
var trustedAssociations = map[string]bool{
	"OWNER":        true,
	"MEMBER":       true,
	"COLLABORATOR": true,
}

// trustedAuthor reports whether the PR author's association is in the trusted
// allowlist (fail-closed: unknown/empty → untrusted).
func trustedAuthor(info PRInfo) bool {
	return trustedAssociations[info.AuthorAssociation]
}

// resolveEvent decides the CreateReview Event. It returns APPROVE only when the
// configured approval policy and every safety predicate hold; otherwise COMMENT
// with a reason. self_approve_forbidden is NOT decided here, it is a reactive
// 422 catch in PostReview, so it never appears as a resolveEvent reason.
func resolveEvent(opts PostReviewOptions, info PRInfo, gateClean bool, findings []engine.Finding, reviewedFiles int, headUnchanged bool) (event, reason string) {
	policy := normalizeApprovalPolicy(opts.Approval)
	if policy.Mode == "" || policy.Mode == "off" {
		return "COMMENT", approveReasonNotRequested
	}
	if !gateClean {
		return "COMMENT", approveReasonGateFailed
	}
	if !approvalFindingsAllowed(policy, findings) {
		return "COMMENT", approveReasonThresholdFailed
	}
	if info.IsFork {
		return "COMMENT", approveReasonFork
	}
	if !trustedAuthor(info) {
		return "COMMENT", approveReasonUntrusted
	}
	if reviewedFiles <= 0 {
		return "COMMENT", approveReasonNothingDone
	}
	// An empty HeadSHA makes the head-unchanged comparison unreliable: we can't
	// confirm what we'd be approving, so treat it as not-safe rather than risk an
	// APPROVE on an unknown head.
	if info.HeadSHA == "" {
		return "COMMENT", approveReasonHeadUnknown
	}
	if !headUnchanged {
		return "COMMENT", approveReasonHeadMoved
	}
	return "APPROVE", approveReasonApproved
}

func normalizeApprovalPolicy(policy config.ApprovalPolicy) config.ApprovalPolicy {
	switch policy.Mode {
	case "", "off":
		return config.ApprovalPolicy{}
	case "clean":
		policy.MaxPriority = ""
	case "threshold":
		if policy.MaxPriority == "" {
			policy.MaxPriority = defaultApprovalMaxPriority
		}
	default:
		return config.ApprovalPolicy{}
	}
	if policy.Note == "" {
		if policy.Mode == "threshold" {
			policy.Note = "on_findings"
		} else {
			policy.Note = "none"
		}
	}
	return policy
}

func approvalFindingsAllowed(policy config.ApprovalPolicy, findings []engine.Finding) bool {
	switch policy.Mode {
	case "clean":
		return len(findings) == 0
	case "threshold":
		return engine.MaxSeverityRank(findings) <= approvalPriorityRank(policy.MaxPriority)
	default:
		return false
	}
}

func approvalPriorityRank(priority string) int {
	switch priority {
	case "P0":
		return engine.MaxSeverityRank([]engine.Finding{{Severity: "critical"}})
	case "P1":
		return engine.MaxSeverityRank([]engine.Finding{{Severity: "high"}})
	case "P2":
		return engine.MaxSeverityRank([]engine.Finding{{Severity: "medium"}})
	case "P3":
		return engine.MaxSeverityRank([]engine.Finding{{Severity: "low"}})
	case "P4":
		return engine.MaxSeverityRank([]engine.Finding{{Severity: "info"}})
	default:
		return -1
	}
}

func approvalBody(policy config.ApprovalPolicy, findings []engine.Finding) string {
	policy = normalizeApprovalPolicy(policy)
	if policy.Note == "none" || (policy.Note == "on_findings" && len(findings) == 0) {
		return ""
	}
	if policy.Mode == "threshold" && len(findings) > 0 {
		return fmt.Sprintf("Approved: only findings at or below `%s` remain under the configured approval policy. Review the summary before merge.", policy.MaxPriority)
	}
	return "Approved by the configured approval policy."
}

// ApprovalWouldApprove exposes the dry approval decision for reuse checks.
func ApprovalWouldApprove(info PRInfo, policy config.ApprovalPolicy, gateClean bool, findings []engine.Finding, reviewedFiles int) bool {
	event, _ := resolveEvent(PostReviewOptions{Approval: policy}, info, gateClean, findings, reviewedFiles, true)
	return event == "APPROVE"
}

// alreadyApproved reports whether an APPROVED review already exists at the current
// head SHA, so a re-run at the same SHA posts no second APPROVE. First page only
// (PerPage:100): a PR with >100 reviews may miss an existing APPROVE and post a
// duplicate, low-harm (a redundant APPROVE on an already-clean PR), so full
// pagination's rate-limit cost isn't worth it (YAGNI).
func alreadyApproved(ctx stdctx.Context, client Client, info *PRInfo) (bool, error) {
	reviews, _, err := client.ListReviews(ctx, info.Owner, info.Repo, info.Number, &gh.ListOptions{PerPage: 100})
	if err != nil {
		return false, mapWriteError("github.list_reviews_failed", "listing reviews", err)
	}
	for _, r := range reviews {
		// Guard the empty case: an empty HeadSHA must never match a review whose
		// CommitID is also empty ("" == "" → a false-positive that blocks APPROVE).
		if r.GetState() == "APPROVED" && info.HeadSHA != "" && r.GetCommitID() == info.HeadSHA {
			return true, nil
		}
	}
	return false, nil
}

// HasApprovedReview exposes the approval idempotency check for reuse checks.
func HasApprovedReview(ctx stdctx.Context, client Client, info *PRInfo) (bool, error) {
	return alreadyApproved(ctx, client, info)
}

// ApproveResolvedLedger submits an APPROVE when a resolution-cleared ledger leaves
// the PR approvable: the policy would approve zero open findings on info.HeadSHA and
// no APPROVE exists there yet. The caller MUST have verified info.HeadSHA is the
// reviewed head (so we approve what was actually reviewed). Best-effort from the
// background resolution sync: expected approval rejections (self-approve, 403/401,
// 422) degrade to "not approved" without erroring; only info is required non-nil.
func ApproveResolvedLedger(ctx stdctx.Context, client Client, info *PRInfo, policy config.ApprovalPolicy) (bool, string) {
	if info == nil || info.HeadSHA == "" {
		return false, approveReasonHeadUnknown
	}
	// gateClean + zero findings: a fully-resolved ledger has no open finding above
	// any gate. reviewedFiles=1 because a ledger only exists after a real review.
	if !ApprovalWouldApprove(*info, policy, true, nil, 1) {
		return false, approveReasonThresholdFailed
	}
	if approved, err := alreadyApproved(ctx, client, info); err != nil {
		return false, "list_reviews_failed"
	} else if approved {
		return false, approveReasonApproved
	}
	req := &gh.PullRequestReviewRequest{
		CommitID: gh.Ptr(info.HeadSHA),
		Event:    gh.Ptr("APPROVE"),
	}
	if body := approvalBody(policy, nil); strings.TrimSpace(body) != "" {
		req.Body = gh.Ptr(body)
	}
	if _, err := client.CreateReview(ctx, info.Owner, info.Repo, info.Number, req); err != nil {
		switch {
		case isSelfApprove422(err):
			return false, approveReasonSelfForbidden
		case is422(err):
			return false, approveReasonRejected
		case is403(err) || is401(err):
			return false, approveReasonForbidden
		default:
			return false, "create_review_failed"
		}
	}
	return true, approveReasonApproved
}
