package github

import (
	stdctx "context"
	"errors"
	"log/slog"
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
	login, err := currentLogin(ctx, client)
	if err != nil {
		return DeferredApprovalResult{Reason: "comment_author_unverified"}, err
	}
	if login == "" {
		return DeferredApprovalResult{Reason: "comment_author_unverified"}, nil
	}
	commentID, body, err := markedCommentByAuthor(ctx, client, info, login)
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

// approveClearedPublishedHead approves when the summary's published commit is
// still the current head and the ledger has nothing open. A wait on CI or
// mergeability is written onto that summary so a later poll can approve it.
func approveClearedPublishedHead(ctx stdctx.Context, client Client, info *PRInfo, policy config.ApprovalPolicy, summaryID int64, body string) (bool, string) {
	if info == nil || summaryID == 0 || strings.TrimSpace(body) == "" {
		return false, ""
	}
	reviewed := parsePublishedCommit(body)
	if reviewed == "" || !strings.EqualFold(reviewed, info.HeadSHA) || !LedgerClearForApproval(ParseLedger(body)) {
		return false, ""
	}
	approved, reason := ApproveResolvedLedger(ctx, client, info, policy, summaryCommentURL(info, summaryID, ""))
	if approved || !approvalRetryReason(reason) {
		return approved, reason
	}
	if err := stampDeferredApproval(ctx, client, info, summaryID, body, reason); err != nil {
		slog.Warn("deferred approval notice failed",
			"repo", info.Owner+"/"+info.Repo, "pr", info.Number, "head_sha", shortSHA(info.HeadSHA),
			"reason", reason, "error", config.RedactString(err.Error()))
		return false, "summary_edit_failed"
	}
	return false, reason
}

func approvalRetryReason(reason string) bool {
	switch reason {
	case approveReasonChecksNotGreen, approveReasonReadinessUnverified:
		return true
	default:
		return false
	}
}

func stampDeferredApproval(ctx stdctx.Context, client Client, info *PRInfo, summaryID int64, body, reason string) error {
	if deferredApprovalReason(body) != "" {
		return nil
	}
	next := insertApprovalBlocker(body, reason)
	if next == body {
		return nil
	}
	if _, err := client.EditIssueComment(ctx, info.Owner, info.Repo, summaryID, &gh.IssueComment{Body: gh.Ptr(next)}); err != nil {
		return mapWriteError("github.deferred_approval_failed", "editing summary comment", err)
	}
	return nil
}

func insertApprovalBlocker(body, reason string) string {
	var block strings.Builder
	renderApprovalBlocker(&block, reason)
	insertion := strings.TrimRight(block.String(), "\n")
	if insertion == "" {
		return body
	}
	idx := strings.Index(body, "**Result:**")
	if idx < 0 {
		return body
	}
	rel := strings.IndexByte(body[idx:], '\n')
	if rel < 0 {
		return body + "\n\n" + insertion + "\n"
	}
	insertAt := idx + rel + 1
	if insertAt < len(body) && body[insertAt] == '\n' {
		insertAt++
	}
	return body[:insertAt] + insertion + "\n\n" + body[insertAt:]
}

// DeferredApprovalNeedsAttention reports retry reasons that are operational
// failures rather than an expected skip. The host logs these at warn.
func DeferredApprovalNeedsAttention(reason string) bool {
	switch reason {
	case "create_review_failed", "list_reviews_failed", "summary_fetch_failed", "summary_edit_failed", "comment_author_unverified":
		return true
	default:
		return false
	}
}

func currentLogin(ctx stdctx.Context, client Client) (string, error) {
	if client == nil {
		return "", errors.New("github: missing client")
	}
	login, err := client.CurrentLogin(ctx)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(login) == "" {
		return "", errors.New("github: empty authenticated login")
	}
	return login, nil
}

// markedCommentByAuthor returns the lowest-id summary comment written by login.
// It uses the same selection as summary upsert, so a forged marker cannot approve
// and cannot become the comment a later review edits.
func markedCommentByAuthor(ctx stdctx.Context, client Client, info *PRInfo, login string) (int64, string, error) {
	id, _, body, err := lowestMarkedComment(ctx, client, info, login)
	if err != nil {
		return 0, "", mapWriteError("github.deferred_approval_failed", "listing issue comments", err)
	}
	return id, body, nil
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
		if !ledgerBlocksApproval(e.Status) {
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
