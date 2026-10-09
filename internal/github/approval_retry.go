package github

import (
	stdctx "context"
	"regexp"
	"strings"

	gh "github.com/google/go-github/v84/github"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine"
)

const (
	approvalChecksPrefix    = "> **Approval:** Waiting for CI checks to finish successfully."
	approvalReadinessPrefix = "> **Approval:** GitHub readiness could not be verified."
)

// approvalReasonRe reads the hidden deferral marker. Summaries posted before the
// marker only carry the approval line, which deferredApprovalReason still accepts.
var approvalReasonRe = regexp.MustCompile(`<!-- miu-cr-approval:(checks_not_green|readiness_unverified) -->`)

// DeferredApprovalResult is the outcome of one same-head approval retry.
type DeferredApprovalResult struct {
	Approved bool
	Cleared  bool
	Reason   string
}

// RetryDeferredApproval approves a head whose review already passed policy but
// waited on CI or unverified mergeability. It does not start a model review.
// A same-head check rerun that goes green is enough; a new push is not required.
func RetryDeferredApproval(ctx stdctx.Context, client Client, info *PRInfo, policy config.ApprovalPolicy) (DeferredApprovalResult, error) {
	if normalizeApprovalPolicy(policy).Mode == "" {
		return DeferredApprovalResult{Reason: approveReasonNotRequested}, nil
	}
	if info == nil || info.HeadSHA == "" {
		return DeferredApprovalResult{Reason: approveReasonHeadUnknown}, nil
	}
	commentID, body, err := lowestMarkedComment(ctx, client, info)
	if err != nil {
		return DeferredApprovalResult{Reason: "summary_fetch_failed"}, err
	}
	if commentID == 0 || deferredApprovalReason(body) == "" {
		return DeferredApprovalResult{Reason: "not_deferred"}, nil
	}
	published := parsePublishedCommit(body)
	if published == "" || !strings.EqualFold(published, info.HeadSHA) {
		return DeferredApprovalResult{Reason: approveReasonHeadMoved}, nil
	}
	if !approvalFindingsAllowed(normalizeApprovalPolicy(policy), openLedgerFindings(ParseLedger(body))) {
		return DeferredApprovalResult{Reason: approveReasonThresholdFailed}, nil
	}
	approved, reason := ApproveResolvedLedger(ctx, client, info, policy, summaryCommentURL(info, commentID, ""))
	if !approved && reason != approveReasonApproved {
		return DeferredApprovalResult{Reason: reason}, nil
	}
	next := clearDeferredApprovalNotice(body)
	if next == body {
		if approved {
			return DeferredApprovalResult{Approved: true, Reason: approveReasonApproved}, nil
		}
		return DeferredApprovalResult{Reason: approveReasonAlreadyDone}, nil
	}
	if _, err := client.EditIssueComment(ctx, info.Owner, info.Repo, commentID, &gh.IssueComment{Body: gh.Ptr(next)}); err != nil {
		return DeferredApprovalResult{Approved: approved, Reason: "summary_edit_failed"}, mapWriteError("github.deferred_approval_failed", "editing summary comment", err)
	}
	if approved {
		return DeferredApprovalResult{Approved: true, Cleared: true, Reason: approveReasonApproved}, nil
	}
	return DeferredApprovalResult{Cleared: true, Reason: approveReasonAlreadyDone}, nil
}

func approvalReasonToken(reason string) string {
	return "<!-- miu-cr-approval:" + reason + " -->"
}

func deferredApprovalReason(body string) string {
	if m := approvalReasonRe.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	switch {
	case strings.Contains(body, approvalChecksPrefix):
		return approveReasonChecksNotGreen
	case strings.Contains(body, approvalReadinessPrefix):
		return approveReasonReadinessUnverified
	default:
		return ""
	}
}

func openLedgerFindings(entries []LedgerEntry) []engine.Finding {
	var out []engine.Finding
	for _, e := range entries {
		if e.Status == statusResolved {
			continue
		}
		out = append(out, engine.Finding{Severity: e.Sev})
	}
	return out
}

func clearDeferredApprovalNotice(body string) string {
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		if approvalReasonRe.MatchString(strings.TrimSpace(lines[i])) {
			continue
		}
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, approvalChecksPrefix) || strings.HasPrefix(trimmed, approvalReadinessPrefix) {
			if i+1 < len(lines) && strings.TrimSpace(lines[i+1]) == "" {
				i++
			}
			continue
		}
		out = append(out, lines[i])
	}
	return strings.Join(out, "\n")
}
