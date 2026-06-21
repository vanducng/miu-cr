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

// PostReview filters findings to the diff hunks, skips any whose fingerprint is
// already posted, caps the result at maxInlineComments (highest severity first so a
// 422-triggering oversized review can't happen), then submits ONE Event=COMMENT
// review anchored to the head SHA with comfort-fade inline comments (Side=RIGHT/Line
// only, never Position). Returns the count posted and the count omitted by the cap.
func PostReview(ctx stdctx.Context, client Client, info *PRInfo, findings []engine.Finding, diffs []diff.Diff, summary string, existingFPs map[string]bool) (int, int, error) {
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
	for _, f := range toPost {
		body := commentBody(f) + "\n\n" + fpMarker(fingerprint(f))
		comments = append(comments, &gh.DraftReviewComment{
			Path: gh.Ptr(f.File),
			Body: gh.Ptr(body),
			Side: gh.Ptr("RIGHT"),
			Line: gh.Ptr(f.Line),
		})
	}

	if len(comments) == 0 && strings.TrimSpace(summary) == "" {
		return 0, omitted, nil
	}

	req := &gh.PullRequestReviewRequest{
		CommitID: gh.Ptr(info.HeadSHA),
		Event:    gh.Ptr("COMMENT"),
		Comments: comments,
	}
	if strings.TrimSpace(summary) != "" {
		req.Body = gh.Ptr(summary)
	}

	if _, err := client.CreateReview(ctx, info.Owner, info.Repo, info.Number, req); err != nil {
		return 0, omitted, mapWriteError("github.create_review_failed", "creating review", err)
	}
	return len(comments), omitted, nil
}

func commentBody(f engine.Finding) string {
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
	if patch := strings.TrimSpace(f.SuggestedPatch); patch != "" {
		// Grow the fence past any backtick run in the patch so an embedded ``` can't
		// terminate the suggestion block early.
		fence := "```"
		for strings.Contains(patch, fence) {
			fence += "`"
		}
		fmt.Fprintf(&b, "\n\n%ssuggestion\n%s\n%s", fence, patch, fence)
	}
	return b.String()
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
