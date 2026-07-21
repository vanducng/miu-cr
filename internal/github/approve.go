package github

import (
	stdctx "context"
	"fmt"
	"log/slog"
	"strings"
	"time"

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
	approveReasonMergeConflict         = "merge_conflict"
	approveReasonChecksNotGreen        = "checks_not_green"
	approveReasonReadinessUnverified   = "readiness_unverified"
	approveReasonAlreadyDone           = "already_approved"
	approveReasonSelfForbidden         = "self_approve_forbidden"
	approveReasonForbidden             = "approve_forbidden"
	approveReasonRejected              = "approve_rejected"
	approveReasonIdempotencyUnverified = "idempotency_unverified"
)

const defaultApprovalMaxPriority = "P4"

const approvalReadinessAttempts = 3

var approvalReadinessRetryDelay = 500 * time.Millisecond

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

func approvalChecksGreen(ctx stdctx.Context, client Client, info *PRInfo) (bool, error) {
	opts := &gh.ListCheckRunsOptions{Filter: gh.Ptr("latest"), ListOptions: gh.ListOptions{PerPage: 100}}
	for {
		runs, resp, err := client.ListCheckRunsForRef(ctx, info.Owner, info.Repo, info.HeadSHA, opts)
		if err != nil {
			return false, err
		}
		if runs != nil {
			for _, run := range runs.CheckRuns {
				if run.GetName() == checkRunName {
					continue
				}
				if run.GetStatus() != "completed" {
					return false, nil
				}
				switch run.GetConclusion() {
				case "success", "neutral", "skipped":
				default:
					return false, nil
				}
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	statusOpts := &gh.ListOptions{PerPage: 100}
	for {
		statuses, resp, err := client.GetCombinedStatus(ctx, info.Owner, info.Repo, info.HeadSHA, statusOpts)
		if err != nil {
			return false, err
		}
		if statuses != nil {
			for _, status := range statuses.Statuses {
				if status.GetContext() == checkRunName {
					continue
				}
				if status.GetState() != "success" {
					return false, nil
				}
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		statusOpts.Page = resp.NextPage
	}
	return true, nil
}

func approvalReadiness(ctx stdctx.Context, client Client, info *PRInfo) string {
	var fresh *gh.PullRequest
	for attempt := 0; attempt < approvalReadinessAttempts; attempt++ {
		var err error
		fresh, err = client.GetPR(ctx, info.Owner, info.Repo, info.Number)
		if err != nil {
			slog.Warn("approval readiness fetch failed", "error", config.RedactString(err.Error()))
			return approveReasonReadinessUnverified
		}
		if fresh == nil || fresh.GetHead() == nil || fresh.GetHead().GetSHA() != info.HeadSHA {
			return approveReasonHeadMoved
		}
		if fresh.Mergeable != nil {
			break
		}
		if attempt+1 < approvalReadinessAttempts {
			select {
			case <-ctx.Done():
				return approveReasonReadinessUnverified
			case <-time.After(approvalReadinessRetryDelay):
			}
		}
	}
	if fresh.Mergeable == nil {
		return approveReasonReadinessUnverified
	}
	if !fresh.GetMergeable() {
		return approveReasonMergeConflict
	}
	green, err := approvalChecksGreen(ctx, client, info)
	if err != nil {
		slog.Warn("approval readiness checks failed", "error", config.RedactString(err.Error()))
		return approveReasonReadinessUnverified
	}
	if !green {
		return approveReasonChecksNotGreen
	}
	return approveReasonApproved
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
		policy.Note = "always"
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

func approvalBody(policy config.ApprovalPolicy, findings []engine.Finding, summaryURL string, priorApproval bool, headSHA string) string {
	policy = normalizeApprovalPolicy(policy)
	if policy.Note == "none" || (policy.Note == "on_findings" && len(findings) == 0) {
		return ""
	}
	if priorApproval && len(findings) == 0 {
		return ""
	}
	summary := "See the code review summary."
	if u := strings.TrimSpace(summaryURL); u != "" {
		summary = fmt.Sprintf("See the [code review summary](%s).", u)
	}
	if policy.Mode == "threshold" && len(findings) > 0 {
		if priorApproval {
			return fmt.Sprintf("LGTM after re-reviewing %s; only findings at or below `%s` remain. %s", approvalCommitLabel(headSHA), policy.MaxPriority, summary)
		}
		return fmt.Sprintf("LGTM with only findings at or below `%s` remaining. %s", policy.MaxPriority, summary)
	}
	return "LGTM. " + summary
}

func approvalCommitLabel(sha string) string {
	sha = strings.TrimSpace(sha)
	if sha == "" {
		return "the latest commit"
	}
	if len(sha) > 7 {
		sha = sha[:7]
	}
	return fmt.Sprintf("latest commit `%s`", sha)
}

// ApprovalWouldApprove exposes the dry approval decision for reuse checks.
func ApprovalWouldApprove(info PRInfo, policy config.ApprovalPolicy, gateClean bool, findings []engine.Finding, reviewedFiles int) bool {
	event, _ := resolveEvent(PostReviewOptions{Approval: policy}, info, gateClean, findings, reviewedFiles, true)
	return event == "APPROVE"
}

type approvalReviewState struct {
	current bool
	prior   bool
}

func approvalReviews(ctx stdctx.Context, client Client, info *PRInfo) (approvalReviewState, error) {
	var out approvalReviewState
	reviews, _, err := client.ListReviews(ctx, info.Owner, info.Repo, info.Number, &gh.ListOptions{PerPage: 100})
	if err != nil {
		return out, mapWriteError("github.list_reviews_failed", "listing reviews", err)
	}
	for _, r := range reviews {
		if r.GetState() != "APPROVED" {
			continue
		}
		commitID := strings.TrimSpace(r.GetCommitID())
		if info.HeadSHA != "" && commitID == info.HeadSHA {
			out.current = true
		} else if commitID != "" {
			out.prior = true
		}
		if out.current && out.prior {
			break
		}
	}
	return out, nil
}

// alreadyApproved reports whether an APPROVED review already exists at the current
// head SHA, so a re-run at the same SHA posts no second APPROVE.
func alreadyApproved(ctx stdctx.Context, client Client, info *PRInfo) (bool, error) {
	state, err := approvalReviews(ctx, client, info)
	return state.current, err
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
func ApproveResolvedLedger(ctx stdctx.Context, client Client, info *PRInfo, policy config.ApprovalPolicy, summaryURL string) (bool, string) {
	if info == nil || info.HeadSHA == "" {
		return false, approveReasonHeadUnknown
	}
	// gateClean + zero findings: a fully-resolved ledger has no open finding above
	// any gate. reviewedFiles=1 because a ledger only exists after a real review.
	if !ApprovalWouldApprove(*info, policy, true, nil, 1) {
		return false, approveReasonThresholdFailed
	}
	state, err := approvalReviews(ctx, client, info)
	if err != nil {
		return false, "list_reviews_failed"
	}
	if state.current {
		return false, approveReasonApproved
	}
	if reason := approvalReadiness(ctx, client, info); reason != approveReasonApproved {
		return false, reason
	}
	req := &gh.PullRequestReviewRequest{
		CommitID: gh.Ptr(info.HeadSHA),
		Event:    gh.Ptr("APPROVE"),
	}
	if body := approvalBody(policy, nil, summaryURL, state.prior, info.HeadSHA); strings.TrimSpace(body) != "" {
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
