package github

import (
	stdctx "context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	gh "github.com/google/go-github/v84/github"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/diff"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

// SummarySentinel is the hidden HTML marker on the first line of the summary
// comment body; its presence in an existing issue comment makes the upsert edit
// (not duplicate) our own comment.
const SummarySentinel = "<!-- miu-cr-review -->"

const fpPrefix = "miucr:fp="

// maxInlineComments caps inline comments in a single review. GitHub 422s the whole
// review when it carries too many inline comments (~50); we post the top-N by
// severity and note any omitted count in the summary body.
const maxInlineComments = 40

var fpMarkerRe = regexp.MustCompile(`<!-- miucr:fp=([0-9a-f]{16}) -->`)

// DiffsForPR re-derives the PR diff by re-running the engine's own diff.GetDiff
// over the temp clone with the SAME ModeRange/From/To the engine anchored
// against, so inline filtering and anchoring share one deterministic hunk set
// without threading diffs out of internal/engine.
func DiffsForPR(ctx stdctx.Context, runner *gitcmd.Runner, tempDir, baseSHA, headSHA string) ([]diff.Diff, error) {
	return diff.GetDiff(ctx, diff.ModeRange, tempDir, baseSHA, headSHA, "", runner)
}

// filterToDiffHunks keeps only findings whose anchored Line lands on a RIGHT-side
// (added or context) line inside one of the PR's diff hunks. Line==0 (drift) and
// out-of-hunk findings are dropped — GitHub 422s on inline comments off the diff.
func filterToDiffHunks(findings []engine.Finding, diffs []diff.Diff) []engine.Finding {
	rightLines := make(map[string]map[int]bool, len(diffs))
	for i := range diffs {
		d := &diffs[i]
		path := d.NewPath
		if path == "" || path == "/dev/null" {
			continue
		}
		lines := rightLines[path]
		if lines == nil {
			lines = map[int]bool{}
			rightLines[path] = lines
		}
		for _, h := range diff.ParseHunks(d.Diff) {
			newLine := h.NewStart
			for _, l := range h.Lines {
				switch l.Type {
				case diff.HunkContext:
					lines[newLine] = true
					newLine++
				case diff.HunkAdded:
					lines[newLine] = true
					newLine++
				case diff.HunkDeleted:
				}
			}
		}
	}

	kept := make([]engine.Finding, 0, len(findings))
	for _, f := range findings {
		if f.Line == 0 {
			continue
		}
		if rightLines[f.File][f.Line] {
			kept = append(kept, f)
		}
	}
	return kept
}

// fingerprint is a stable short hash over path|line|category|prosehash, where
// prosehash folds the rationale so identical findings dedupe across re-runs.
// M2 limitation: Line is part of the key, so re-running on the SAME head SHA dedupes,
// but a new push that shifts lines may re-post; full cross-push thread tracking is M5.
func fingerprint(f engine.Finding) string {
	prose := sha256.Sum256([]byte(f.Rationale))
	key := fmt.Sprintf("%s|%d|%s|%x", f.File, f.Line, f.Category, prose[:8])
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

func fpMarker(fp string) string { return fmt.Sprintf("<!-- %s%s -->", fpPrefix, fp) }

// ExistingFingerprints scans posted inline review comments for our hidden fp
// markers so PostReview can skip findings already commented in a prior run.
func ExistingFingerprints(ctx stdctx.Context, client Client, info *PRInfo) (map[string]bool, error) {
	fps := map[string]bool{}
	opts := &gh.PullRequestListCommentsOptions{ListOptions: gh.ListOptions{PerPage: 100}}
	for {
		comments, resp, err := client.ListReviewComments(ctx, info.Owner, info.Repo, info.Number, opts)
		if err != nil {
			return nil, mapWriteError("github.list_review_comments_failed", "listing review comments", err)
		}
		for _, c := range comments {
			for _, m := range fpMarkerRe.FindAllStringSubmatch(c.GetBody(), -1) {
				fps[m[1]] = true
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return fps, nil
}

// PostReviewOptions carries the opt-in write-action toggles for PostReview. Both
// actions default OFF; with the zero value PostReview behaves exactly as the M2
// comment-only path (modulo the latent unconditional-suggestion-fence fix).
type PostReviewOptions struct {
	Suggest       bool   // emit native single-line suggested-changes when proven clean
	ApproveClean  bool   // resolve Event=APPROVE when the PR is clean and all safety predicates hold
	Gate          string // gate severity used by the caller to compute GateClean
	GateClean     bool   // caller-computed !engine.GateFailed(findings, Gate)
	ReviewedFiles int    // count of files actually reviewed; APPROVE requires >0
}

// PostReviewResult reports what PostReview did: inline comments posted, comments
// omitted by the cap, and (for --approve-clean) the resolved review Event and the
// reason it was chosen. Event is "COMMENT" unless every approve predicate held.
type PostReviewResult struct {
	Posted      int
	Omitted     int
	Suggestions int // native one-click suggestions emitted this run (subset of Posted)
	Event       string
	Reason      string
}

// PostReview filters findings to the diff hunks, skips any whose fingerprint is
// already posted, caps the result at maxInlineComments (highest severity first so a
// 422-triggering oversized review can't happen), then submits ONE review anchored to
// the head SHA with comfort-fade inline comments (Side=RIGHT/Line only, never
// Position). The Event is COMMENT unless opts.ApproveClean and every safety
// predicate holds (resolveEvent), in which case it is APPROVE. A failed APPROVE
// degrades to COMMENT when the cause is a 422 precondition miss — self_approve_forbidden
// (the bot is the author) or approve_rejected (any other 422: stale head, branch
// protection, …) — never an error. A non-422 API failure surfaces as an error and
// never reports a phantom approval.
func PostReview(ctx stdctx.Context, client Client, info *PRInfo, findings []engine.Finding, diffs []diff.Diff, summary string, existingFPs map[string]bool, opts PostReviewOptions) (PostReviewResult, error) {
	newFileContent := make(map[string]string, len(diffs))
	for i := range diffs {
		if diffs[i].NewPath != "" {
			newFileContent[diffs[i].NewPath] = diffs[i].NewFileContent
		}
	}

	inHunk := filterToDiffHunks(findings, diffs)

	toPost := make([]engine.Finding, 0, len(inHunk))
	for _, f := range inHunk {
		if existingFPs[fingerprint(f)] {
			continue
		}
		toPost = append(toPost, f)
	}

	omitted := 0
	if len(toPost) > maxInlineComments {
		sort.SliceStable(toPost, func(i, j int) bool {
			return severityRank(toPost[i].Severity) < severityRank(toPost[j].Severity)
		})
		omitted = len(toPost) - maxInlineComments
		toPost = toPost[:maxInlineComments]
	}

	comments := make([]*gh.DraftReviewComment, 0, len(toPost))
	suggestions := 0
	for _, f := range toPost {
		rendered, native := commentBody(f, newFileContent[f.File], opts)
		if native {
			suggestions++
		}
		body := rendered + "\n\n" + fpMarker(fingerprint(f))
		comments = append(comments, &gh.DraftReviewComment{
			Path: gh.Ptr(f.File),
			Body: gh.Ptr(body),
			Side: gh.Ptr("RIGHT"),
			Line: gh.Ptr(f.Line),
		})
	}

	event, reason := resolveApproveEvent(ctx, client, info, opts)
	result := PostReviewResult{Posted: len(comments), Omitted: omitted, Suggestions: suggestions, Event: event, Reason: reason}

	// Nothing to say AND not approving: don't create an empty review.
	if len(comments) == 0 && strings.TrimSpace(summary) == "" && event != "APPROVE" {
		result.Posted = 0
		return result, nil
	}

	req := &gh.PullRequestReviewRequest{
		CommitID: gh.Ptr(info.HeadSHA),
		Event:    gh.Ptr(event),
		Comments: comments,
	}
	if strings.TrimSpace(summary) != "" {
		req.Body = gh.Ptr(summary)
	}

	if _, err := client.CreateReview(ctx, info.Owner, info.Repo, info.Number, req); err != nil {
		if event != "APPROVE" {
			return PostReviewResult{Omitted: omitted, Event: "COMMENT"}, mapWriteError("github.create_review_failed", "creating review", err)
		}
		// APPROVE failed. A 422 is a precondition miss: self-approve (bot==author)
		// or any other 422 (stale head, branch protection, …) → degrade to COMMENT,
		// never a run failure. A non-422 error is a real API failure: surface it,
		// and never claim a phantom approval (returned Event stays COMMENT).
		switch {
		case isSelfApprove422(err):
			result.Event, result.Reason = "COMMENT", approveReasonSelfForbidden
		case is422(err):
			result.Event, result.Reason = "COMMENT", approveReasonRejected
		default:
			return PostReviewResult{Omitted: omitted, Event: "COMMENT"}, mapWriteError("github.create_review_failed", "creating review", err)
		}
		// Re-apply the empty-review guard once the event is COMMENT: don't submit a
		// review with no inline comments and no body — GitHub 422s an empty COMMENT.
		if len(comments) == 0 && strings.TrimSpace(summary) == "" {
			result.Posted = 0
			return result, nil
		}
		req.Event = gh.Ptr("COMMENT")
		if _, rerr := client.CreateReview(ctx, info.Owner, info.Repo, info.Number, req); rerr != nil {
			return PostReviewResult{Omitted: omitted, Event: "COMMENT"}, mapWriteError("github.create_review_failed", "creating review", rerr)
		}
		return result, nil
	}
	return result, nil
}

// resolveApproveEvent runs the approve-clean idempotency + head-race guards, then
// resolveEvent. It performs no writes; it returns the Event/reason PostReview
// submits. Any read error degrades to COMMENT (never an error) so a precondition
// check can't fail a run; the self-approve 422 is handled reactively in PostReview.
func resolveApproveEvent(ctx stdctx.Context, client Client, info *PRInfo, opts PostReviewOptions) (event, reason string) {
	if !opts.ApproveClean {
		return "COMMENT", approveReasonNotRequested
	}

	// Idempotency guard. If the dedupe read itself fails we can't confirm there
	// isn't already an APPROVE — degrade rather than risk a duplicate APPROVE.
	done, err := alreadyApproved(ctx, client, info)
	if err != nil {
		return "COMMENT", approveReasonIdempotencyUnverified
	}
	if done {
		return "COMMENT", approveReasonAlreadyDone
	}

	// Re-fetch the head SHA right before deciding: the LLM pass can take long
	// enough for a new push to land; an APPROVE on a stale head is unsafe.
	headUnchanged := false
	if fresh, err := client.GetPR(ctx, info.Owner, info.Repo, info.Number); err == nil && fresh != nil && fresh.GetHead() != nil {
		headUnchanged = fresh.GetHead().GetSHA() == info.HeadSHA
	}

	return resolveEvent(opts, *info, opts.GateClean, opts.ReviewedFiles, headUnchanged)
}

// isSelfApprove422 reports whether err is a GitHub 422 specifically from approving
// one's own PR (the bot PAT identity == the PR author). Matched reactively at the
// CreateReview call — there is no proactive bot-identity lookup. It inspects the
// error message (top-level and nested errors[]) so unrelated 422s (stale head,
// branch protection, invalid line) are NOT misclassified as self-approve.
func isSelfApprove422(err error) bool {
	var er *gh.ErrorResponse
	if !errors.As(err, &er) || er.Response == nil || er.Response.StatusCode != 422 {
		return false
	}
	msg := strings.ToLower(er.Message)
	for _, e := range er.Errors {
		msg += " " + strings.ToLower(e.Message)
	}
	return strings.Contains(msg, "own pull request")
}

// is422 reports whether err is any GitHub 422 (Unprocessable Entity).
func is422(err error) bool {
	var er *gh.ErrorResponse
	return errors.As(err, &er) && er.Response != nil && er.Response.StatusCode == 422
}

// commentBody renders one inline comment and reports whether it emitted a native
// one-click ```suggestion fence. The fence is emitted ONLY when opts.Suggest AND
// isCleanReplacement proves a verbatim single-line replacement of the raw new-file
// line AND the severity meets the floor; otherwise (and whenever there's a patch
// but the gate isn't met) the patch is shown as a plain fenced hint, never a
// one-click suggestion. This is also the fix for the latent M2 bug where a
// ```suggestion fence was emitted unconditionally — one-click-applying an
// unverified, possibly multi-line patch. Returning the native flag lets PostReview
// count suggestions from this single render pass (no second isCleanReplacement /
// file split per finding).
func commentBody(f engine.Finding, newFileContent string, opts PostReviewOptions) (string, bool) {
	var b strings.Builder
	sev := strings.ToUpper(f.Severity)
	if sev == "" {
		sev = "NOTE"
	}
	cat := f.Category
	if cat != "" {
		fmt.Fprintf(&b, "**%s** (%s)\n\n", sev, cat)
	} else {
		fmt.Fprintf(&b, "**%s**\n\n", sev)
	}
	b.WriteString(f.Rationale)

	patch := strings.TrimSpace(f.SuggestedPatch)
	if patch == "" {
		return b.String(), false
	}

	if opts.Suggest && meetsSuggestionFloor(f.Severity) {
		if sug, ok := isCleanReplacement(f, newFileContent); ok {
			fmt.Fprintf(&b, "\n\n%ssuggestion\n%s\n%s", fenceFor(sug), sug, fenceFor(sug))
			return b.String(), true
		}
	}

	fmt.Fprintf(&b, "\n\n%s\n%s\n%s", fenceFor(patch), patch, fenceFor(patch))
	return b.String(), false
}

// fenceFor grows a code fence past any backtick run in s so an embedded ``` can't
// terminate the block early.
func fenceFor(s string) string {
	fence := "```"
	for strings.Contains(s, fence) {
		fence += "`"
	}
	return fence
}

// UpsertSummaryComment ensures exactly one sentinel-headed summary issue comment:
// it edits ours if an existing comment carries SummarySentinel, else creates one.
// Returns "edited" or "created".
func UpsertSummaryComment(ctx stdctx.Context, client Client, info *PRInfo, body string) (string, error) {
	full := SummarySentinel + "\n" + body

	opts := &gh.IssueListCommentsOptions{ListOptions: gh.ListOptions{PerPage: 100}}
	for {
		comments, resp, err := client.ListIssueComments(ctx, info.Owner, info.Repo, info.Number, opts)
		if err != nil {
			return "", mapWriteError("github.list_issue_comments_failed", "listing issue comments", err)
		}
		for _, c := range comments {
			if strings.Contains(c.GetBody(), SummarySentinel) {
				if _, eerr := client.EditIssueComment(ctx, info.Owner, info.Repo, c.GetID(), &gh.IssueComment{Body: gh.Ptr(full)}); eerr != nil {
					return "", mapWriteError("github.edit_comment_failed", "editing summary comment", eerr)
				}
				return "edited", nil
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	if _, err := client.CreateIssueComment(ctx, info.Owner, info.Repo, info.Number, &gh.IssueComment{Body: gh.Ptr(full)}); err != nil {
		return "", mapWriteError("github.create_comment_failed", "creating summary comment", err)
	}
	return "created", nil
}

// mapWriteError maps go-github rate-limit errors to a retryable github.rate_limited
// CLIError (carrying Retry-After when known) and falls back to a generic typed error.
func mapWriteError(code, stage string, err error) error {
	var rle *gh.RateLimitError
	var arle *gh.AbuseRateLimitError
	if errors.As(err, &rle) {
		ce := &clierr.CLIError{
			Code:      "github.rate_limited",
			Message:   "GitHub rate limit exceeded",
			Hint:      "wait for the rate limit to reset, then re-run",
			Exit:      1,
			Retry:     true,
			SafeRetry: true,
		}
		if !rle.Rate.Reset.IsZero() {
			ce.Details = map[string]any{"retry_after_seconds": int(time.Until(rle.Rate.Reset.Time).Seconds())}
		}
		return ce
	}
	if errors.As(err, &arle) {
		ce := &clierr.CLIError{
			Code:      "github.rate_limited",
			Message:   "GitHub secondary (abuse) rate limit exceeded",
			Hint:      "wait before retrying",
			Exit:      1,
			Retry:     true,
			SafeRetry: true,
		}
		if arle.RetryAfter != nil {
			ce.Details = map[string]any{"retry_after_seconds": int(arle.RetryAfter.Seconds())}
		}
		return ce
	}
	return ghWriteError(code, stage, err)
}

func ghWriteError(code, stage string, err error) error {
	return &clierr.CLIError{
		Code:    code,
		Message: config.RedactString(fmt.Sprintf("%s: %v", stage, err)),
		Exit:    1,
	}
}
