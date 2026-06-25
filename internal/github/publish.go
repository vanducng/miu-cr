package github

import (
	stdctx "context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
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

// ReviewMarker is the hidden HTML marker on the first line of our review body; its
// presence in a PR review identifies the review as miucr-authored so a same-SHA
// re-run can detect we already posted (alreadyPostedAtSHA) and skip.
const ReviewMarker = "<!-- miu-cr-review -->"

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

// FilterMode controls which findings are eligible for inline posting, mirroring
// reviewdog's diff knob. DiffContext (default) keeps findings on any RIGHT-side
// (added or context) diff line; Added keeps only findings on added lines; File
// keeps findings on any file present in the diff; NoFilter keeps everything.
// Findings outside the diff (File/NoFilter) are routed to summary/SARIF/local by
// the caller, never inline (GitHub 422s an off-diff inline comment).
type FilterMode string

const (
	FilterAdded       FilterMode = "added"
	FilterDiffContext FilterMode = "diff_context"
	FilterFile        FilterMode = "file"
	FilterNoFilter    FilterMode = "nofilter"
)

// ValidFilterMode reports whether s is a recognized --filter-mode value.
func ValidFilterMode(s string) bool {
	switch FilterMode(s) {
	case FilterAdded, FilterDiffContext, FilterFile, FilterNoFilter:
		return true
	}
	return false
}

// ValidMinSeverity reports whether s is a recognized --min-severity value
// (none disables the floor; the rest are the standard severities).
func ValidMinSeverity(s string) bool {
	switch s {
	case "none", "info", "low", "medium", "high", "critical":
		return true
	}
	return false
}

// minSeverityFloor keeps only findings whose severity reaches min (a high→low
// rank). An empty/"none" min is a no-op (current behavior). An unknown-severity
// finding (rank past the table) is dropped only when a real floor is set, so a
// floor never silently posts an ungraded finding inline.
func minSeverityFloor(findings []engine.Finding, min string) []engine.Finding {
	if min == "" || min == "none" {
		return findings
	}
	floor := severityRank(min)
	kept := make([]engine.Finding, 0, len(findings))
	for _, f := range findings {
		if severityRank(f.Severity) <= floor {
			kept = append(kept, f)
		}
	}
	return kept
}

// filterToDiffHunks keeps only findings inline-eligible under DiffContext: an
// anchored Line on a RIGHT-side (added or context) line inside one of the PR's
// diff hunks. It is filterFindings(FilterDiffContext) — kept as a named helper
// because it is the inline default and is referenced widely.
func filterToDiffHunks(findings []engine.Finding, diffs []diff.Diff) []engine.Finding {
	return filterFindings(findings, diffs, FilterDiffContext)
}

// filterFindings selects findings per mode. For Added/DiffContext a finding must
// anchor on the corresponding RIGHT-side line; for File the finding's file must be
// in the diff; for NoFilter all findings pass. Line==0 (drift) findings are kept
// only by File/NoFilter — they can never be inlined but must still reach SARIF/local.
func filterFindings(findings []engine.Finding, diffs []diff.Diff, mode FilterMode) []engine.Finding {
	if mode == FilterNoFilter {
		out := make([]engine.Finding, len(findings))
		copy(out, findings)
		return out
	}

	// File mode keys only on file presence — skip the per-line hunk maps entirely.
	if mode == FilterFile {
		filesInDiff := make(map[string]bool, len(diffs))
		for i := range diffs {
			path := diffs[i].NewPath
			if path == "" || path == "/dev/null" {
				continue
			}
			filesInDiff[path] = true
		}
		kept := make([]engine.Finding, 0, len(findings))
		for _, f := range findings {
			if filesInDiff[f.File] {
				kept = append(kept, f)
			}
		}
		return kept
	}

	addedLines := make(map[string]map[int]bool, len(diffs))
	contextLines := make(map[string]map[int]bool, len(diffs))
	ensure := func(m map[string]map[int]bool, p string) map[int]bool {
		if m[p] == nil {
			m[p] = map[int]bool{}
		}
		return m[p]
	}
	for i := range diffs {
		d := &diffs[i]
		path := d.NewPath
		if path == "" || path == "/dev/null" {
			continue
		}
		for _, h := range diff.ParseHunks(d.Diff) {
			newLine := h.NewStart
			for _, l := range h.Lines {
				switch l.Type {
				case diff.HunkContext:
					ensure(contextLines, path)[newLine] = true
					newLine++
				case diff.HunkAdded:
					ensure(addedLines, path)[newLine] = true
					newLine++
				case diff.HunkDeleted:
				}
			}
		}
	}

	kept := make([]engine.Finding, 0, len(findings))
	for _, f := range findings {
		switch mode {
		case FilterAdded:
			if f.Line != 0 && addedLines[f.File][f.Line] {
				kept = append(kept, f)
			}
		default: // FilterDiffContext
			if f.Line != 0 && (addedLines[f.File][f.Line] || contextLines[f.File][f.Line]) {
				kept = append(kept, f)
			}
		}
	}
	return kept
}

// inlineEligible selects findings postable as inline comments under mode. Added
// restricts to added lines; every other mode (including file/nofilter) is clamped
// to diff_context — a finding off a RIGHT-side diff line can never be inlined
// (GitHub 422), so file/nofilter only ever WIDEN the summary/SARIF/local set, never
// the inline set.
func inlineEligible(findings []engine.Finding, diffs []diff.Diff, mode FilterMode) []engine.Finding {
	if mode == FilterAdded {
		return filterFindings(findings, diffs, FilterAdded)
	}
	return filterFindings(findings, diffs, FilterDiffContext)
}

// hunkRightSets returns, per new-path, the RIGHT-side (added+context) line set of
// EACH hunk separately, so a multi-line range can be proven contiguous WITHIN one
// hunk (GitHub 422s a range spanning two hunks or off the diff).
func hunkRightSets(diffs []diff.Diff) map[string][]map[int]bool {
	out := make(map[string][]map[int]bool, len(diffs))
	for i := range diffs {
		d := &diffs[i]
		path := d.NewPath
		if path == "" || path == "/dev/null" {
			continue
		}
		for _, h := range diff.ParseHunks(d.Diff) {
			set := map[int]bool{}
			newLine := h.NewStart
			for _, l := range h.Lines {
				switch l.Type {
				case diff.HunkContext, diff.HunkAdded:
					set[newLine] = true
					newLine++
				case diff.HunkDeleted:
				}
			}
			if len(set) > 0 {
				out[path] = append(out[path], set)
			}
		}
	}
	return out
}

// rangeInOneHunk reports whether [start,end] is a contiguous RIGHT-side range fully
// contained in ONE hunk of path (every line start..end present in the same hunk
// set). It is the GitHub 422 guard for multi-line range comments.
func rangeInOneHunk(sets map[string][]map[int]bool, path string, start, end int) bool {
	if start <= 0 || end <= start {
		return false
	}
	for _, set := range sets[path] {
		all := true
		for ln := start; ln <= end; ln++ {
			if !set[ln] {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// normalizeForFingerprint produces the line-free content key fed into
// fingerprint. It is deliberately LESS lossy than the anchor's splitAndNormalize:
// per line it strips a single leading diff +/- marker and trailing whitespace and
// normalizes CRLF→LF, but PRESERVES leading indentation and blank lines so that
// findings differing only by indentation or blank-line structure keep distinct
// fingerprints (no over-dedup). It is NOT the anchor's matching normalize.
func normalizeForFingerprint(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	// Strip the leading diff column ONLY when the whole quote is a diff hunk:
	// every non-blank line starts with '+', '-', or ' ' AND at least one is a real
	// '+'/'-' change line. The marker requirement avoids treating ordinary
	// space-indented code (all lines start with ' ') as a diff and shaving its
	// indentation; the all-lines check avoids corrupting genuine code like "-1".
	isDiff, hasMarker := true, false
	for _, line := range lines {
		t := strings.TrimRight(line, " \t")
		if t == "" {
			continue
		}
		switch t[0] {
		case '+', '-':
			hasMarker = true
		case ' ':
		default:
			isDiff = false
		}
		if !isDiff {
			break
		}
	}
	for i, line := range lines {
		line = strings.TrimRight(line, " \t")
		if isDiff && hasMarker && len(line) > 0 {
			line = line[1:] // drop the +/-/space diff column
		}
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

// fingerprint is a stable, line-INDEPENDENT short hash over
// path|category|sha256(normalizeForFingerprint(QuotedCode)). Dropping Line makes a
// re-anchored finding map to the SAME hash, so the existing <!-- miucr:fp=hash -->
// markers dedupe across pushes with no DB. Dropping Rationale (LLM free-text)
// keeps the key from fragmenting. Best-effort: a re-quoted span for the same bug
// yields a different fp (under-dedup); semantic matching is M7.
func fingerprint(f engine.Finding) string {
	norm := normalizeForFingerprint(f.QuotedCode)
	var code [32]byte
	if norm == "" {
		// An empty normalized quote (empty or lone-marker QuotedCode) hashes to a
		// constant, collapsing every empty-quote finding on the same file+category
		// to one fp (silent over-dedup). Disambiguate with Line+Rationale so distinct
		// empty-quote findings keep distinct fingerprints; the non-empty path below is
		// byte-identical to before.
		code = sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s", f.Line, f.Rationale)))
	} else {
		code = sha256.Sum256([]byte(norm))
	}
	key := fmt.Sprintf("%s|%s|%x", f.File, f.Category, code[:])
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// Fingerprint exposes the content-stable, line-independent finding fingerprint to
// the wire layer so the PR-thread store keys rows on the SAME hash carried by the
// inline-comment markers (byte-identical to the marker contract).
func Fingerprint(f engine.Finding) string { return fingerprint(f) }

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
	Suggest       bool       // emit native single-line suggested-changes when proven clean
	ApproveClean  bool       // resolve Event=APPROVE when the PR is clean and all safety predicates hold
	Gate          string     // gate severity used by the caller to compute GateClean
	GateClean     bool       // caller-computed !engine.GateFailed(findings, Gate)
	ReviewedFiles int        // count of files actually reviewed; APPROVE requires >0
	FilterMode    FilterMode // inline-eligibility filter; empty = diff_context (default)
	// MinSeverity is the inline-posting floor: none|info|low|medium|high|critical.
	// Empty/"none" posts everything (current behavior); a real floor drops
	// below-threshold findings from INLINE only — they still reach the summary/SARIF.
	MinSeverity string
	// CategoryURLs maps a lowercased finding Category to a validated docs URL
	// (TRUSTED config only — never repo rules). When a finding's category matches,
	// commentBody renders it as a Markdown link; an empty/nil map = plain category.
	CategoryURLs map[string]string
	// RuleCitations maps a wire-validated rule stem to its citation (linkable repo
	// rule vs cite-only user/built-in). Built from the LOADED, fork-dropped rule set
	// in the wire layer; a finding citing a stem absent here is not grounded.
	RuleCitations map[string]RuleCitation
	// ActionsOut is where the fork-PR 403 fallback writes ::error:: workflow commands.
	// GitHub Actions parses workflow commands ONLY from the step's stdout, so this must
	// resolve to the same stream as the miucr.cli/v1 envelope (the command's stdout
	// writer, cmd.OutOrStdout()) — not os.Stderr, where the commands would be ignored.
	// The fallback writes these commands DURING the run and the envelope is emitted
	// LAST, so on the rare fork-fallback path stdout carries the ::error:: lines
	// followed by the single-line JSON envelope; `tail -1 | jq` still parses it. nil
	// falls back to os.Stdout; only used under Actions.
	ActionsOut io.Writer
}

// PostReviewResult reports what PostReview did: inline comments posted, comments
// omitted by the cap, and (for --approve-clean) the resolved review Event and the
// reason it was chosen. Event is "COMMENT" unless every approve predicate held.
type PostReviewResult struct {
	Posted      int
	Omitted     int
	Ranges      int // multi-line range comments emitted this run (subset of Posted)
	Suggestions int // native one-click suggestions emitted this run (subset of Posted)
	Event       string
	Reason      string
	// OmittedFindings are the capped (over-limit) findings NOT posted inline, in
	// the same severity order they were dropped — surfaced into the summary overflow
	// block so nothing is silently lost.
	OmittedFindings []engine.Finding
	// PostedFindings carries (fingerprint, path) for the inline comments in the
	// ACTUALLY-submitted review only — set after a successful submit, never on the
	// empty-guard / pre-submit path. The store records exactly these as posted.
	PostedFindings []PostedFinding
	// Fallback is the count of ::error:: workflow annotations emitted to stdout when
	// CreateReview 403'd under GitHub Actions (a fork PR without comment-write scope);
	// 0 on the normal path. When >0 the review did NOT hard-fail.
	Fallback int
}

// PostedFinding is the minimal (fingerprint, path) pair the store needs to track a
// posted finding; finding text never leaves the local process.
type PostedFinding struct {
	Fingerprint string
	Path        string
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
// summaryFn renders the review body given the inline-omitted set (known only after
// the cap is applied), so the body's overflow block lists the actually-omitted
// findings. A nil summaryFn means no body (e.g. --mode checks / approve-only paths).
func PostReview(ctx stdctx.Context, client Client, info *PRInfo, findings []engine.Finding, diffs []diff.Diff, summaryFn func(omitted int, omittedFindings []engine.Finding) string, existingFPs map[string]bool, opts PostReviewOptions) (PostReviewResult, error) {
	newFileContent := make(map[string]string, len(diffs))
	for i := range diffs {
		if diffs[i].NewPath != "" {
			newFileContent[diffs[i].NewPath] = diffs[i].NewFileContent
		}
	}

	inHunk := minSeverityFloor(inlineEligible(findings, diffs, opts.FilterMode), opts.MinSeverity)

	toPost := make([]engine.Finding, 0, len(inHunk))
	for _, f := range inHunk {
		if existingFPs[fingerprint(f)] {
			continue
		}
		toPost = append(toPost, f)
	}

	omitted := 0
	var omittedFindings []engine.Finding
	if len(toPost) > maxInlineComments {
		sort.SliceStable(toPost, func(i, j int) bool {
			return severityRank(toPost[i].Severity) < severityRank(toPost[j].Severity)
		})
		omitted = len(toPost) - maxInlineComments
		omittedFindings = append(omittedFindings, toPost[maxInlineComments:]...)
		toPost = toPost[:maxInlineComments]
	}

	summary := ""
	if summaryFn != nil {
		summary = summaryFn(omitted, omittedFindings)
	}

	hunkSets := hunkRightSets(diffs)
	comments := make([]*gh.DraftReviewComment, 0, len(toPost))
	submitted := make([]PostedFinding, 0, len(toPost))
	suggestions, ranges := 0, 0
	for _, f := range toPost {
		// Multi-line range ONLY when the anchored EndLine is past Line AND the whole
		// span is contiguous within one RIGHT-side hunk; otherwise single-line (the
		// GitHub 422 guard: start<line, same side, both in one hunk). This same proof
		// gates the native multi-line suggestion below: a multi-line ```suggestion may
		// be one-clicked only on a verified contiguous-one-hunk RIGHT range, else it
		// degrades to a plain fenced hint (a single-anchored multi-line suggestion
		// inserts instead of replaces — a broken patch the spec forbids).
		isRange := f.EndLine > f.Line && rangeInOneHunk(hunkSets, f.File, f.Line, f.EndLine)
		rendered, native := commentBody(info, f, newFileContent[f.File], opts, isRange)
		if native {
			suggestions++
		}
		fp := fingerprint(f)
		body := rendered + "\n\n" + fpMarker(fp)
		c := &gh.DraftReviewComment{
			Path: gh.Ptr(f.File),
			Body: gh.Ptr(body),
			Side: gh.Ptr("RIGHT"),
			Line: gh.Ptr(f.Line),
		}
		if isRange {
			c.StartLine = gh.Ptr(f.Line)
			c.StartSide = gh.Ptr("RIGHT")
			c.Line = gh.Ptr(f.EndLine)
			ranges++
		}
		comments = append(comments, c)
		submitted = append(submitted, PostedFinding{Fingerprint: fp, Path: f.File})
	}

	event, reason := resolveApproveEvent(ctx, client, info, opts)
	result := PostReviewResult{Posted: len(comments), Omitted: omitted, OmittedFindings: omittedFindings, Ranges: ranges, Suggestions: suggestions, Event: event, Reason: reason}

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
		// Fork-PR fallback: a 403 under GitHub Actions means the token lacks
		// comment-write scope (typical for fork PRs). Emit per-finding ::error::
		// workflow annotations to stdout instead of hard-failing.
		if is403(err) && inGitHubActions() {
			out := opts.ActionsOut
			if out == nil {
				out = os.Stdout
			}
			result.Fallback = emitWorkflowAnnotations(out, toPost)
			// A fork token that 403s on CreateReview also can't APPROVE (same call);
			// the fallback only emits annotations, so the resolved event degrades to
			// COMMENT regardless of opts.ApproveClean — intentional, not a dropped approval.
			result.Posted, result.Event, result.PostedFindings = 0, "COMMENT", nil
			return result, nil
		}
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
		result.PostedFindings = submitted
		return result, nil
	}
	result.PostedFindings = submitted
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

// is403 reports whether err is a GitHub 403 (Forbidden) — typically a fork PR
// whose Actions token lacks the write scope to post review comments.
func is403(err error) bool {
	var er *gh.ErrorResponse
	return errors.As(err, &er) && er.Response != nil && er.Response.StatusCode == 403
}

// inGitHubActions reports whether we're running inside a GitHub Actions runner.
func inGitHubActions() bool { return os.Getenv("GITHUB_ACTIONS") == "true" }

// maxWorkflowAnnotations caps the ::error:: workflow commands emitted on the fork
// fallback so a large finding set can't flood the Actions log.
const maxWorkflowAnnotations = 50

// emitWorkflowAnnotations writes per-finding `::error file=...,line=...,endLine=...::message`
// workflow commands to w (stdout under Actions — the runner only parses commands
// from stdout, never stderr), capped, so a fork PR that 403s on comment writes still
// surfaces findings as Actions annotations instead of hard-failing. The file path is
// property-escaped and the message is data-escaped (finding text only — never a
// token), so a path or rationale carrying ':'/','/newline can't break the command
// boundary or inject a fake annotation. A finding with Line<=0 (file-level/drift,
// not line-anchorable) emits a file-level annotation (no line/endLine), which the
// workflow-command grammar allows. Returns the count emitted.
func emitWorkflowAnnotations(w io.Writer, findings []engine.Finding) int {
	n := 0
	for _, f := range findings {
		if n >= maxWorkflowAnnotations {
			break
		}
		file := escapeWorkflowProperty(f.File)
		msg := escapeWorkflowMessage(f.Rationale)
		if f.Line <= 0 {
			fmt.Fprintf(w, "::error file=%s::%s\n", file, msg)
			n++
			continue
		}
		end := f.EndLine
		if end < f.Line {
			end = f.Line
		}
		fmt.Fprintf(w, "::error file=%s,line=%d,endLine=%d::%s\n", file, f.Line, end, msg)
		n++
	}
	return n
}

// escapeWorkflowMessage escapes the chars GitHub uses to delimit a workflow command's
// message/data segment (mirrors @actions/core escapeData): a multi-line rationale
// can't break the ::error:: command.
func escapeWorkflowMessage(s string) string {
	r := strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A")
	return r.Replace(strings.TrimSpace(s))
}

// escapeWorkflowProperty escapes a workflow command property value (mirrors
// @actions/core escapeProperty): the data escapes PLUS ':' and ',', which delimit
// properties — so a file path containing a colon/comma/newline can't terminate the
// `file=` property early or inject another `::error::` annotation.
func escapeWorkflowProperty(s string) string {
	r := strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", ":", "%3A", ",", "%2C")
	return r.Replace(s)
}

// commentBody renders one inline comment and reports whether it emitted a native
// one-click ```suggestion fence. The fence is emitted ONLY when opts.Suggest AND
// isCleanReplacement proves a safe replacement (a verbatim single-line replacement,
// or a multi-line wrap/guard on a QuotedCode-proven single-line anchor) of the raw
// new-file line AND the severity meets the floor; otherwise (and whenever there's a patch
// but the gate isn't met) the patch is shown as a plain fenced hint, never a
// one-click suggestion. This is also the fix for the latent M2 bug where a
// ```suggestion fence was emitted unconditionally — one-click-applying an
// unverified, possibly multi-line patch. Returning the native flag lets PostReview
// count suggestions from this single render pass (no second isCleanReplacement /
// file split per finding).
//
// isRange MUST be the caller's rangeInOneHunk(Line,EndLine) proof: a native
// multi-line suggestion (EndLine>Line) is emitted ONLY when isRange is true, so a
// span that fell back to a single-line comment never carries a one-click multi-line
// fence (which GitHub would INSERT, not replace — a broken unverified patch). A
// single-line finding (EndLine<=Line) ignores isRange.
func commentBody(info *PRInfo, f engine.Finding, newFileContent string, opts PostReviewOptions, isRange bool) (string, bool) {
	var b strings.Builder
	badge := priorityBadge(f.Severity)
	cite := ruleCitation(info, f.Rule, opts.RuleCitations)
	cat := f.Category
	if cat != "" {
		fmt.Fprintf(&b, "%s · %s%s\n\n", badge, categoryMarkdown(cat, opts.CategoryURLs), cite)
	} else {
		fmt.Fprintf(&b, "%s%s\n\n", badge, cite)
	}
	if t := mdInline(f.Title); t != "" {
		fmt.Fprintf(&b, "**%s**\n\n", t)
	}
	b.WriteString(mdProse(f.Rationale))

	patch := strings.TrimSpace(f.SuggestedPatch)
	if patch == "" {
		return b.String(), false
	}

	// A multi-line replacement is one-clickable only on a proven on-diff range; a
	// single-anchored multi-line suggestion would insert lines, not replace the span.
	multiLine := f.EndLine > f.Line
	if opts.Suggest && meetsSuggestionFloor(f.Severity) && (!multiLine || isRange) {
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

// alreadyPostedAtSHA reports whether a miucr-authored review (its body carries
// ReviewMarker) already exists at the current head SHA, so a same-commit re-run
// skips rather than posting a duplicate review (GitHub reviews aren't editable).
// Paginates (bounded) so our review is found even on a PR with many reviews — our
// own review is the most recent and lands on a later page. A reviewed-but-unposted
// prior leaves no such review, so it still posts.
func AlreadyPostedAtSHA(ctx stdctx.Context, client Client, info *PRInfo) (bool, error) {
	if info.HeadSHA == "" {
		return false, nil
	}
	opt := &gh.ListOptions{PerPage: 100}
	for page := 0; page < 10; page++ { // bounded at 1000 reviews — pathological PRs don't loop forever
		reviews, resp, err := client.ListReviews(ctx, info.Owner, info.Repo, info.Number, opt)
		if err != nil {
			return false, mapWriteError("github.list_reviews_failed", "listing reviews", err)
		}
		for _, r := range reviews {
			if r.GetCommitID() == info.HeadSHA && strings.Contains(r.GetBody(), ReviewMarker) {
				return true, nil
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}
	return false, nil
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
