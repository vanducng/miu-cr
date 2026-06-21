package github

import (
	stdctx "context"

	gh "github.com/google/go-github/v84/github"
)

// Approve reason codes recorded in PRResult when the resolver degrades APPROVE to
// COMMENT. All are non-fatal: a precondition miss never fails a run.
const (
	approveReasonApproved              = "approved"
	approveReasonNotRequested          = "not_requested"
	approveReasonGateFailed            = "gate_failed"
	approveReasonFork                  = "fork"
	approveReasonUntrusted             = "untrusted_author"
	approveReasonNothingDone           = "nothing_reviewed"
	approveReasonHeadMoved             = "head_moved"
	approveReasonAlreadyDone           = "already_approved"
	approveReasonSelfForbidden         = "self_approve_forbidden"
	approveReasonRejected              = "approve_rejected"
	approveReasonIdempotencyUnverified = "idempotency_unverified"
)

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

// resolveEvent decides the CreateReview Event. It returns APPROVE only when
// approve-clean is requested AND every safety predicate holds; otherwise COMMENT
// with a reason. self_approve_forbidden is NOT decided here — it is a reactive
// 422 catch in PostReview — so it never appears as a resolveEvent reason.
func resolveEvent(opts PostReviewOptions, info PRInfo, gateClean bool, reviewedFiles int, headUnchanged bool) (event, reason string) {
	if !opts.ApproveClean {
		return "COMMENT", approveReasonNotRequested
	}
	if !gateClean {
		return "COMMENT", approveReasonGateFailed
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
	if !headUnchanged {
		return "COMMENT", approveReasonHeadMoved
	}
	return "APPROVE", approveReasonApproved
}

// alreadyApproved reports whether an APPROVED review already exists at the current
// head SHA, so a re-run at the same SHA posts no second APPROVE. First page only
// (PerPage:100): a PR with >100 reviews may miss an existing APPROVE and post a
// duplicate — low-harm (a redundant APPROVE on an already-clean PR), so full
// pagination's rate-limit cost isn't worth it (YAGNI).
func alreadyApproved(ctx stdctx.Context, client Client, info *PRInfo) (bool, error) {
	reviews, _, err := client.ListReviews(ctx, info.Owner, info.Repo, info.Number, &gh.ListOptions{PerPage: 100})
	if err != nil {
		return false, mapWriteError("github.list_reviews_failed", "listing reviews", err)
	}
	for _, r := range reviews {
		if r.GetState() == "APPROVED" && r.GetCommitID() == info.HeadSHA {
			return true, nil
		}
	}
	return false, nil
}
