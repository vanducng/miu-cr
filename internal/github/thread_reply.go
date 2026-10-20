package github

import (
	stdctx "context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	gh "github.com/google/go-github/v84/github"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine/diff"
)

const (
	botMarker       = "<!-- miu-cr-bot -->"
	offDiffMarker   = "<!-- miu-cr-offdiff -->"
	noticeMarker    = "<!-- miu-cr-notice -->"
	threadReplyHint = "Reply `Fixed in <sha>: ...`, `Deferred: ...`, or `Not applicable: ...`."

	replyNotReadyWait = 15 * time.Minute
	replyFailureWait  = time.Hour

	replyFixNotOnPR   = "fix_not_on_pr"
	replyFixUnchanged = "fix_file_unchanged"
	replyFixLine      = "fix_line_untouched"
	replyFixChanged   = "fix_changed"

	// fixPatchOtherCap stays reserved so a long deletion cannot hide a moved check.
	fixPatchCap      = 6000
	fixPatchOtherCap = 2500
	replyAlready     = "already_handled"
	replyReopened    = "reopened"
)

// ErrUnparseableVerdict means the reply judge did not return a verdict object.
var ErrUnparseableVerdict = errors.New("unparseable reply verdict")

// IsBotBody reports whether body is a comment miu-cr itself posted. Developer
// replies that quote a marker later in the body still parse; only a leading
// marker counts, so a reply cannot loop the bot.
func IsBotBody(body string) bool {
	trimmed := strings.TrimSpace(body)
	return strings.HasPrefix(trimmed, botMarker) ||
		strings.HasPrefix(trimmed, "<!-- miu-cr-reply:") ||
		strings.HasPrefix(trimmed, ReviewMarker) ||
		strings.HasPrefix(trimmed, noticeMarker) ||
		strings.HasPrefix(trimmed, offDiffMarker)
}

func replyToMarker(id int64) string {
	return fmt.Sprintf("<!-- miu-cr-reply:%d -->", id)
}

type ReplyIntent string

const (
	ReplyFix           ReplyIntent = "fix"
	ReplyDefer         ReplyIntent = "defer"
	ReplyNotApplicable ReplyIntent = "not_applicable"
)

type ParsedReply struct {
	Intent ReplyIntent
	SHA    string
	Reason string
}

var (
	fixReplyRe   = regexp.MustCompile(`(?is)^\s*fixed in\s+([0-9a-f]{7,40})\b[:\s-]*(.*)$`)
	deferReplyRe = regexp.MustCompile(`(?is)^\s*defer(?:red)?\s*:\s*(.*)$`)
	naReplyRe    = regexp.MustCompile(`(?is)^\s*(?:not applicable|n/a|na)\s*:\s*(.*)$`)
)

// ParseThreadReply classifies a developer reply. Unknown text is ignored so a
// comment that is not a verdict never starts a model call.
func ParseThreadReply(body string) ParsedReply {
	body = strings.TrimSpace(body)
	if body == "" || IsBotBody(body) {
		return ParsedReply{}
	}
	if m := fixReplyRe.FindStringSubmatch(body); m != nil {
		return ParsedReply{Intent: ReplyFix, SHA: strings.ToLower(m[1]), Reason: strings.TrimSpace(m[2])}
	}
	if m := deferReplyRe.FindStringSubmatch(body); m != nil {
		return ParsedReply{Intent: ReplyDefer, Reason: strings.TrimSpace(m[1])}
	}
	if m := naReplyRe.FindStringSubmatch(body); m != nil {
		return ParsedReply{Intent: ReplyNotApplicable, Reason: strings.TrimSpace(m[1])}
	}
	return ParsedReply{}
}

// ThreadReplyRequest is one human comment the reply handler should judge.
type ThreadReplyRequest struct {
	CommentID         int64
	Body              string
	UserLogin         string
	AuthorAssociation string
	InReplyTo         int64
	Kind              string // review_comment or issue_comment
	// HostRetry is set when the host poll will try this comment again. A webhook
	// delivery will not, so a failure there must leave an answer on the thread.
	HostRetry bool
	// AroundWrite runs the summary update. The judge stays outside it so a model
	// call does not block other writers for this pull request.
	AroundWrite func(func() error) error
}

// ReplyAuthorized reports whether this commenter may close or defer a finding.
// The pull request author may always reply. Anyone else must be an owner,
// member, or collaborator. An empty association is not trusted.
func ReplyAuthorized(login, association, authorLogin string) bool {
	if authorLogin != "" && strings.EqualFold(strings.TrimSpace(login), authorLogin) {
		return true
	}
	return trustedAssociations[association]
}

// ThreadReplyJudge judges one finding on one reply. Implementations must not
// review the rest of the pull request.
type ThreadReplyJudge interface {
	JudgeThreadReply(ctx stdctx.Context, in ThreadReplyJudgeInput) (ThreadReplyVerdict, error)
}

// ThreadReplyJudgeInput is the only context a reply judgment may see.
type ThreadReplyJudgeInput struct {
	Intent      ReplyIntent
	Path        string
	Line        int
	Title       string
	Severity    string
	FindingBody string
	Reply       string
	Reason      string
	CommitSHA   string
	CommitPatch string
	LineInPatch bool
}

// ThreadReplyVerdict is the judge's accept/reject plus a one-line explanation.
// reason is a renderer hint for deterministic checks. The model leaves it empty.
type ThreadReplyVerdict struct {
	Accept      bool
	Explanation string
	reason      string
}

// ThreadReplyResult is the outcome of handling one comment.
type ThreadReplyResult struct {
	Action        string
	Reason        string
	Accepted      int
	Rejected      int
	Approved      bool
	ApproveReason string
	// CoolDown asks the host poll to wait before trying this comment again.
	// It is set when the verdict was not saved and a marker would drop it.
	CoolDown time.Duration
}

type fixCommitFile struct {
	Path     string
	Previous string
	Status   string
	Patch    string
}

type commitInspector interface {
	PullRequestCommitSHAs(ctx stdctx.Context, owner, repo string, number int) ([]string, error)
	CommitFiles(ctx stdctx.Context, owner, repo, sha string) ([]fixCommitFile, error)
}

type reviewThreadWriter interface {
	ReplyToReviewComment(ctx stdctx.Context, owner, repo string, number int, commentID int64, body string) (string, error)
	ResolveReviewThread(ctx stdctx.Context, threadID string) error
}

type replyTarget struct {
	Index  int
	Entry  LedgerEntry
	Body   string
	Thread string
	RootID int64
}

// ApplyThreadReply answers one developer reply on a miu-cr finding thread.
// It judges only that reply, updates the summary ledger, and resolves the
// GitHub thread when every finding on it is accepted.
func ApplyThreadReply(ctx stdctx.Context, client Client, info *PRInfo, req ThreadReplyRequest, judge ThreadReplyJudge, policy config.ApprovalPolicy, now time.Time) (ThreadReplyResult, error) {
	if info == nil || req.CommentID <= 0 {
		return ThreadReplyResult{Action: "ignored", Reason: "bad_request"}, nil
	}
	login, loginErr := client.CurrentLogin(ctx)
	if loginErr != nil {
		login = ""
	}
	if IsBotBody(req.Body) || (login != "" && strings.EqualFold(req.UserLogin, login)) {
		return ThreadReplyResult{Action: "ignored", Reason: "bot_comment"}, nil
	}
	parsed := ParseThreadReply(req.Body)
	if parsed.Intent == "" {
		return ThreadReplyResult{Action: "ignored", Reason: "unrecognized"}, nil
	}
	if !ReplyAuthorized(req.UserLogin, req.AuthorAssociation, info.AuthorLogin) {
		return ThreadReplyResult{Action: "ignored", Reason: "untrusted_comment"}, nil
	}

	var reviewComments []*gh.PullRequestComment
	var issueComments []*gh.IssueComment
	err := withReadRetry(ctx, func() error {
		var e error
		reviewComments, e = listAllReviewComments(ctx, client, info)
		return e
	})
	if err != nil {
		return ThreadReplyResult{Reason: "list_review_comments_failed"}, mapWriteError("github.thread_reply_failed", "listing review comments", err)
	}
	err = withReadRetry(ctx, func() error {
		var e error
		issueComments, e = listAllIssueComments(ctx, client, info)
		return e
	})
	if err != nil {
		return ThreadReplyResult{Reason: "list_issue_comments_failed"}, mapWriteError("github.thread_reply_failed", "listing issue comments", err)
	}
	if replyAlreadyPosted(reviewComments, issueComments, req.CommentID) {
		return ThreadReplyResult{Action: "ignored", Reason: "duplicate"}, nil
	}

	if login == "" {
		return ThreadReplyResult{Reason: "comment_author_unverified", CoolDown: replyNotReadyWait}, nil
	}
	summaryID, _, summaryBody, err := lowestMarkedComment(ctx, client, info, login)
	if err != nil {
		return ThreadReplyResult{Reason: "summary_fetch_failed"}, mapWriteError("github.thread_reply_failed", "listing summary", err)
	}
	if summaryID == 0 {
		return ThreadReplyResult{Action: "ignored", Reason: "no_summary", CoolDown: replyNotReadyWait}, nil
	}
	ledger := ParseLedger(summaryBody)
	if ledger == nil {
		return ThreadReplyResult{Action: "ignored", Reason: "no_ledger", CoolDown: replyNotReadyWait}, nil
	}

	threads, _ := loadReviewThreads(ctx, client, info)
	if req.Kind != "issue_comment" && threadHasFingerprint(reviewComments, req) && !threadOwnedBy(reviewComments, req, login) {
		return ThreadReplyResult{Action: "ignored", Reason: "other_reviewer"}, nil
	}
	targets, clarify, foreign := selectReplyTargets(req, reviewComments, issueComments, ledger, threads)
	if foreign {
		body := botMarker + "\n" + replyToMarker(req.CommentID) + "\n\nThis finding is not in the current review summary.\n"
		posted, err := postedThreadReply(ctx, client, info, req, body)
		if err != nil {
			return ThreadReplyResult{Reason: "reply_post_failed"}, mapWriteError("github.thread_reply_failed", "posting reply", err)
		}
		if !posted {
			return ThreadReplyResult{Action: "ignored", Reason: "duplicate"}, nil
		}
		return ThreadReplyResult{Action: "replied", Reason: "not_in_ledger"}, nil
	}
	if clarify {
		body := botMarker + "\n" + replyToMarker(req.CommentID) + "\n\nName the finding as `path:line`, or reply on its thread. A pull request comment is used only when one finding is still open.\n"
		posted, err := postedThreadReply(ctx, client, info, req, body)
		if err != nil {
			return ThreadReplyResult{Reason: "reply_post_failed"}, mapWriteError("github.thread_reply_failed", "posting clarification", err)
		}
		if !posted {
			return ThreadReplyResult{Action: "ignored", Reason: "duplicate"}, nil
		}
		return ThreadReplyResult{Action: "replied", Reason: "clarification"}, nil
	}
	if len(targets) == 0 {
		body := botMarker + "\n" + replyToMarker(req.CommentID) + "\n\nThis comment is not on a miu-cr finding thread.\n"
		posted, err := postedThreadReply(ctx, client, info, req, body)
		if err != nil {
			return ThreadReplyResult{Reason: "reply_post_failed"}, mapWriteError("github.thread_reply_failed", "posting reply", err)
		}
		if !posted {
			return ThreadReplyResult{Action: "ignored", Reason: "duplicate"}, nil
		}
		return ThreadReplyResult{Action: "replied", Reason: "not_our_thread"}, nil
	}

	decisions := make([]replyDecision, 0, len(targets))
	for _, target := range targets {
		if !ledgerBlocksApproval(ledger[target.Index].Status) {
			decisions = append(decisions, replyDecision{target: target, skip: true, verdict: ThreadReplyVerdict{Accept: true, Explanation: "already handled", reason: replyAlready}})
			continue
		}
		verdict, fullSHA, err := decideReply(ctx, client, info, parsed, target, judge)
		if err != nil {
			if errors.Is(err, ErrUnparseableVerdict) {
				body := botMarker + "\n" + replyToMarker(req.CommentID) + "\n\nThis reply could not be judged. Reply again with a clearer reason.\n"
				posted, perr := postedThreadReply(ctx, client, info, req, body)
				if perr != nil {
					return ThreadReplyResult{Reason: "reply_post_failed"}, mapWriteError("github.thread_reply_failed", "posting reply", perr)
				}
				if !posted {
					return ThreadReplyResult{Action: "ignored", Reason: "duplicate"}, nil
				}
				return ThreadReplyResult{Action: "replied", Reason: "judge_unparseable"}, nil
			}
			return ThreadReplyResult{Reason: "judge_failed"}, err
		}
		decisions = append(decisions, replyDecision{target: target, verdict: verdict, fullSHA: fullSHA})
	}

	var result ThreadReplyResult
	err = req.aroundWrite(func() error {
		visible, verr := replyMarkerVisible(ctx, client, info, req)
		if verr != nil {
			result = ThreadReplyResult{Reason: "reply_post_failed"}
			return mapWriteError("github.thread_reply_failed", "listing reply", verr)
		}
		if visible {
			result = ThreadReplyResult{Action: "ignored", Reason: "duplicate"}
			return nil
		}
		var werr error
		summaryID, _, summaryBody, werr = lowestMarkedComment(ctx, client, info, login)
		if werr != nil {
			result = ThreadReplyResult{Reason: "summary_fetch_failed"}
			return mapWriteError("github.thread_reply_failed", "listing summary", werr)
		}
		if summaryID == 0 {
			result = ThreadReplyResult{Action: "ignored", Reason: "no_summary", CoolDown: replyNotReadyWait}
			return nil
		}
		ledger = ParseLedger(summaryBody)
		if ledger == nil {
			result = ThreadReplyResult{Action: "ignored", Reason: "no_ledger", CoolDown: replyNotReadyWait}
			return nil
		}
		decisions = remapDecisions(decisions, ledger)
		next := append([]LedgerEntry(nil), ledger...)
		accepted, rejected := 0, 0
		for _, d := range decisions {
			if d.skip {
				continue
			}
			if !d.verdict.Accept {
				rejected++
				continue
			}
			accepted++
			applyAcceptedVerdict(&next[d.target.Index], parsed, d.fullSHA, d.verdict.Explanation, now)
		}
		if accepted == 0 && rejected == 0 {
			replyBody := renderThreadReply(info, req.CommentID, parsed, decisions)
			posted, perr := postedThreadReply(ctx, client, info, req, replyBody)
			if perr != nil {
				result = ThreadReplyResult{Reason: "reply_post_failed"}
				return mapWriteError("github.thread_reply_failed", "posting reply", perr)
			}
			if !posted {
				result = ThreadReplyResult{Action: "ignored", Reason: "duplicate"}
				return nil
			}
			result = ThreadReplyResult{Action: "replied", Reason: "already_handled"}
			if LedgerClearForApproval(ledger) {
				result.Approved, result.ApproveReason = approveClearedPublishedHead(ctx, client, info, policy, summaryID, summaryBody)
			}
			return nil
		}

		renderInfo := *info
		if reviewed := parseReviewedCommit(summaryBody); reviewed != "" {
			renderInfo.HeadSHA = reviewed
		}
		if runs := parseRunsCount(summaryBody); runs > 0 {
			renderInfo.ReviewCount = runs
		}
		inlineURLs, _ := ExistingFingerprints(ctx, client, info)
		nextBody, ok := replaceSummaryLedgerBody(summaryBody, &renderInfo, next, inlineURLs)
		if !ok {
			result = ThreadReplyResult{Reason: "summary_shape_unsupported", CoolDown: replyFailureWait}
			return nil
		}
		if _, eerr := client.EditIssueComment(ctx, info.Owner, info.Repo, summaryID, &gh.IssueComment{Body: gh.Ptr(nextBody)}); eerr != nil {
			result = ThreadReplyResult{Reason: "summary_edit_failed"}
			return mapWriteError("github.thread_reply_failed", "editing summary", eerr)
		}

		replyBody := renderThreadReply(info, req.CommentID, parsed, decisions)
		posted, perr := postedThreadReply(ctx, client, info, req, replyBody)
		if perr != nil {
			result = ThreadReplyResult{Accepted: accepted, Rejected: rejected, Reason: "reply_post_failed"}
			return mapWriteError("github.thread_reply_failed", "posting reply", perr)
		}
		if !posted {
			result = ThreadReplyResult{Action: "ignored", Reason: "duplicate", Accepted: accepted, Rejected: rejected}
			return nil
		}
		resolveAcceptedThreads(ctx, client, decisions, next, reviewComments)

		result = ThreadReplyResult{Action: "replied", Reason: "replied", Accepted: accepted, Rejected: rejected}
		if rejected == 0 && LedgerClearForApproval(next) {
			result.Approved, result.ApproveReason = approveClearedPublishedHead(ctx, client, info, policy, summaryID, nextBody)
		}
		if _, nerr := UpsertResponseNotice(ctx, client, info, login, OpenNoticeItems(next, inlineURLs, nil)); nerr != nil {
			result.Reason = "notice_failed"
		}
		return nil
	})
	return result, err
}

func withReadRetry(ctx stdctx.Context, fn func() error) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = fn(); err == nil || ctx.Err() != nil || attempt == 2 || !readRetryable(err) {
			return err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
	}
	return err
}

func readRetryable(err error) bool {
	var ce *clierr.CLIError
	if !errors.As(ghAPIError("github.thread_reply_failed", "listing", err), &ce) || !ce.Retry {
		return false
	}
	return ce.Code != "github.rate_limited"
}

func (r ThreadReplyRequest) aroundWrite(fn func() error) error {
	if r.AroundWrite == nil {
		return fn()
	}
	return r.AroundWrite(fn)
}

func remapDecisions(decisions []replyDecision, ledger []LedgerEntry) []replyDecision {
	byFP := make(map[string]int, len(ledger))
	for i, e := range ledger {
		byFP[e.FP] = i
	}
	out := make([]replyDecision, 0, len(decisions))
	for _, d := range decisions {
		idx, ok := byFP[d.target.Entry.FP]
		if !ok {
			continue
		}
		d.target.Index = idx
		d.target.Entry = ledger[idx]
		if !ledgerBlocksApproval(ledger[idx].Status) {
			d.skip = true
			d.verdict = ThreadReplyVerdict{Accept: true, Explanation: "already handled", reason: replyAlready}
		} else if d.skip {
			d.skip = false
			d.verdict = ThreadReplyVerdict{Accept: false, Explanation: "this finding is open again", reason: replyReopened}
		}
		out = append(out, d)
	}
	return out
}

type replyDecision struct {
	target  replyTarget
	verdict ThreadReplyVerdict
	fullSHA string
	skip    bool
}

func decideReply(ctx stdctx.Context, client Client, info *PRInfo, parsed ParsedReply, target replyTarget, judge ThreadReplyJudge) (ThreadReplyVerdict, string, error) {
	if parsed.Intent == ReplyFix {
		check, explain, err := inspectFixCommit(ctx, client, info, parsed.SHA, parsed.Reason, target.Entry)
		if err != nil {
			return ThreadReplyVerdict{}, "", err
		}
		if explain != "" {
			return ThreadReplyVerdict{Accept: false, Explanation: explain}, "", nil
		}
		if check.reason != "" {
			return ThreadReplyVerdict{Accept: false, reason: check.reason}, check.fullSHA, nil
		}
		if judge == nil {
			return deterministicFixVerdict(check, target.Entry), check.fullSHA, nil
		}
		verdict, err := judge.JudgeThreadReply(ctx, ThreadReplyJudgeInput{
			Intent: parsed.Intent, Path: target.Entry.Path, Line: target.Entry.Line, Title: target.Entry.Title,
			Severity: target.Entry.Sev, FindingBody: clipText(target.Body, 2000), Reply: parsed.Reason,
			Reason: parsed.Reason, CommitSHA: check.fullSHA, CommitPatch: clipText(check.patch, fixPatchCap), LineInPatch: check.lineInPatch,
		})
		return verdict, check.fullSHA, err
	}
	if explain, ok := precheckReason(parsed); !ok {
		return ThreadReplyVerdict{Accept: false, Explanation: explain}, "", nil
	}
	if judge == nil {
		return deterministicReasonVerdict(parsed), "", nil
	}
	verdict, err := judge.JudgeThreadReply(ctx, ThreadReplyJudgeInput{
		Intent: parsed.Intent, Path: target.Entry.Path, Line: target.Entry.Line, Title: target.Entry.Title,
		Severity: target.Entry.Sev, FindingBody: clipText(target.Body, 2000), Reply: parsed.Reason, Reason: parsed.Reason,
	})
	return verdict, "", err
}

type fixCheck struct {
	fullSHA     string
	patch       string
	lineInPatch bool
	fileChanged bool
	reason      string
}

func inspectFixCommit(ctx stdctx.Context, client Client, info *PRInfo, sha, reason string, entry LedgerEntry) (fixCheck, string, error) {
	inspector, ok := client.(commitInspector)
	if !ok {
		return fixCheck{}, "this install cannot read commits, so the fix SHA cannot be checked", nil
	}
	shas, err := inspector.PullRequestCommitSHAs(ctx, info.Owner, info.Repo, info.Number)
	if err != nil {
		return fixCheck{}, "", err
	}
	full, matched := matchCommitSHA(sha, shas)
	if !matched {
		return fixCheck{reason: replyFixNotOnPR}, "", nil
	}
	files, err := inspector.CommitFiles(ctx, info.Owner, info.Repo, full)
	if err != nil {
		return fixCheck{}, "", err
	}
	finding, rest, found := splitFindingFile(files, entry.Path)
	if !found {
		return fixCheck{fullSHA: full, reason: replyFixUnchanged}, "", nil
	}
	lineInPatch := entry.Line <= 0 || strings.TrimSpace(finding.Patch) == "" || patchTouchesLine(finding.Patch, entry.Line)
	return fixCheck{
		fullSHA: full, patch: renderFixPatch(finding, rest, reason), lineInPatch: lineInPatch, fileChanged: true,
	}, "", nil
}

func splitFindingFile(files []fixCommitFile, path string) (fixCommitFile, []fixCommitFile, bool) {
	exact, previous := -1, -1
	for i := range files {
		if files[i].Path == path {
			exact = i
			break
		}
		if previous < 0 && files[i].Previous == path {
			previous = i
		}
	}
	idx := exact
	if idx < 0 {
		idx = previous
	}
	if idx < 0 {
		return fixCommitFile{}, nil, false
	}
	rest := make([]fixCommitFile, 0, len(files)-1)
	for i := range files {
		if i != idx {
			rest = append(rest, files[i])
		}
	}
	return files[idx], rest, true
}

func renderFixPatch(finding fixCommitFile, rest []fixCommitFile, reason string) string {
	sort.Slice(rest, func(i, j int) bool {
		im := reasonNamesPath(reason, rest[i].Path)
		jm := reasonNamesPath(reason, rest[j].Path)
		if im != jm {
			return im
		}
		return rest[i].Path < rest[j].Path
	})
	otherBudget := 0
	if len(rest) > 0 {
		otherBudget = fixPatchOtherCap
	}
	var b strings.Builder
	writeFilePatch(&b, finding, fixPatchCap-otherBudget)
	remaining := fixPatchCap - b.Len()
	omitted := false
	for _, f := range rest {
		if remaining < 40 {
			omitted = true
			break
		}
		before := b.Len()
		writeFilePatch(&b, f, remaining)
		used := b.Len() - before
		if used == 0 {
			omitted = true
			continue
		}
		remaining -= used
	}
	if omitted {
		const note = "... other files omitted\n"
		if b.Len()+len(note) > fixPatchCap {
			s := b.String()
			keep := fixPatchCap - len(note)
			if keep < 0 {
				keep = 0
			}
			needNL := false
			if keep > 0 && s[keep-1] != '\n' {
				needNL = true
				keep--
			}
			for keep > 0 && !utf8.ValidString(s[:keep]) {
				keep--
			}
			b.Reset()
			b.WriteString(s[:keep])
			if needNL {
				b.WriteByte('\n')
			}
		}
		b.WriteString(note)
	}
	return b.String()
}

func writeFilePatch(b *strings.Builder, f fixCommitFile, budget int) {
	header := "=== " + f.Path + " (" + fileStatusLabel(f) + ") ===\n"
	body := f.Patch
	if strings.TrimSpace(body) == "" {
		body = "(no text patch)\n"
	} else if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	if len(header) >= budget {
		return
	}
	room := budget - len(header)
	if len(body) > room {
		body = clipText(body, room)
		if body == "" {
			return
		}
		if !strings.HasSuffix(body, "\n") && len(body)+1 <= room {
			body += "\n"
		}
	}
	b.WriteString(header)
	b.WriteString(body)
}

func fileStatusLabel(f fixCommitFile) string {
	status := f.Status
	if status == "" {
		status = "modified"
	}
	if f.Previous != "" && f.Previous != f.Path {
		return status + " from " + f.Previous
	}
	return status
}

func reasonNamesPath(reason, path string) bool {
	if reason == "" || path == "" {
		return false
	}
	reason = strings.ToLower(reason)
	path = strings.ToLower(path)
	rest := reason
	for {
		i := strings.Index(rest, path)
		if i < 0 {
			return false
		}
		before := i == 0 || !isPathByte(rest[i-1])
		end := i + len(path)
		after := end == len(rest) || !isPathByte(rest[end])
		if before && after {
			return true
		}
		rest = rest[i+1:]
	}
}

func deterministicFixVerdict(check fixCheck, entry LedgerEntry) ThreadReplyVerdict {
	if entry.Line > 0 && strings.TrimSpace(check.patch) != "" && !check.lineInPatch {
		return ThreadReplyVerdict{Accept: false, reason: replyFixLine}
	}
	return ThreadReplyVerdict{Accept: true, reason: replyFixChanged}
}

func precheckReason(parsed ParsedReply) (string, bool) {
	reason := strings.TrimSpace(parsed.Reason)
	if parsed.Intent == ReplyNotApplicable && len(reason) < 40 {
		return "not applicable needs a short reason that says why the finding does not apply", false
	}
	if parsed.Intent == ReplyDefer && len(reason) < 20 {
		return "a deferral needs a specific reason and where it is tracked", false
	}
	if parsed.Intent == ReplyDefer && deferralBlocked(reason) {
		return "the deferral is too vague; name what is deferred, why it is safe, and where it is tracked", false
	}
	return "", true
}

func deferralBlocked(reason string) bool {
	flat := strings.ToLower(strings.Join(strings.Fields(reason), " "))
	switch flat {
	case "later", "no", "nope", "skip", "wontfix", "won't fix", "wip", "todo", "not now":
		return true
	default:
		return false
	}
}

func deterministicReasonVerdict(parsed ParsedReply) ThreadReplyVerdict {
	if parsed.Intent == ReplyDefer && !deferralReasonSound(parsed.Reason) {
		return ThreadReplyVerdict{Accept: false, Explanation: "the deferral is too vague; name what is deferred, why it is safe, and where it is tracked"}
	}
	if parsed.Intent == ReplyDefer {
		return ThreadReplyVerdict{Accept: true, Explanation: "deferral accepted"}
	}
	return ThreadReplyVerdict{Accept: true, Explanation: "not applicable accepted"}
}

func deferralReasonSound(reason string) bool {
	flat := strings.ToLower(strings.Join(strings.Fields(reason), " "))
	if len(flat) >= 80 {
		return true
	}
	if strings.Contains(flat, "tracked") || strings.Contains(flat, "http://") || strings.Contains(flat, "https://") {
		return true
	}
	return regexp.MustCompile(`#\d+`).MatchString(flat)
}

func applyAcceptedVerdict(e *LedgerEntry, parsed ParsedReply, fullSHA, explanation string, now time.Time) {
	nowStr := now.UTC().Format(time.RFC3339)
	e.Note = clipText(strings.Join(strings.Fields(explanation), " "), 160)
	e.ResAt = nowStr
	switch parsed.Intent {
	case ReplyDefer:
		e.Status = statusDeferred
		e.ResKind = resolutionDeferral
		e.ResSHA = ""
		if e.Note == "" {
			e.Note = clipText(strings.Join(strings.Fields(parsed.Reason), " "), 160)
		}
	case ReplyNotApplicable:
		e.Status = statusResolved
		e.ResKind = resolutionNotApplicable
		e.ResSHA = ""
	default:
		e.Status = statusResolved
		e.ResKind = ""
		if fullSHA != "" {
			e.ResSHA = fullSHA
		} else {
			e.ResSHA = parsed.SHA
		}
		e.Note = ""
	}
}

func renderThreadReply(info *PRInfo, commentID int64, parsed ParsedReply, decisions []replyDecision) string {
	var b strings.Builder
	b.WriteString(botMarker + "\n")
	b.WriteString(replyToMarker(commentID) + "\n\n")
	for _, d := range decisions {
		fmt.Fprintf(&b, "%s\n", replyLine(info, parsed, d))
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func replyLine(info *PRInfo, parsed ParsedReply, d replyDecision) string {
	file := replyFileLink(info, d.target.Entry.Path, d.target.Entry.Line)
	sha := replySHA(info, d, parsed)
	switch {
	case d.skip || d.verdict.reason == replyAlready:
		return file + " is already handled."
	case d.verdict.reason == replyReopened && parsed.Intent == ReplyFix:
		return "**Fix is not correct.** " + file + " is open again."
	case d.verdict.reason == replyReopened:
		return file + " is open again."
	case parsed.Intent == ReplyFix && d.verdict.Accept:
		return fixAcceptLine(sha, file)
	case parsed.Intent == ReplyFix:
		return fixRejectLine(sha, file, d.verdict)
	case parsed.Intent == ReplyDefer && d.verdict.Accept:
		return "Deferral accepted for " + file + "."
	case parsed.Intent == ReplyDefer:
		return withExtra("Deferral not accepted for "+file+".", d.verdict.Explanation)
	case d.verdict.Accept:
		return "Not applicable for " + file + "."
	default:
		return withExtra("This finding still applies to "+file+".", d.verdict.Explanation)
	}
}

func fixAcceptLine(sha, file string) string {
	if sha == "" {
		return "**Fix is correct.** " + file + "."
	}
	return "**Fix is correct.** " + file + " in " + sha + "."
}

func fixRejectLine(sha, file string, v ThreadReplyVerdict) string {
	switch v.reason {
	case replyFixNotOnPR:
		if sha == "" {
			return "**Fix is not correct.** " + file + ". That commit is not on this pull request."
		}
		return "**Fix is not correct.** " + file + ". " + sha + " is not on this pull request."
	case replyFixUnchanged, replyFixLine:
		return "**Fix is not correct.** " + joinCommit(sha, "does not change", file) + "."
	default:
		if strings.TrimSpace(v.Explanation) != "" && sha == "" {
			return withExtra("**Fix is not correct.** "+file+".", v.Explanation)
		}
		return withExtra("**Fix is not correct.** "+joinCommit(sha, "does not fix", file)+".", v.Explanation)
	}
}

func joinCommit(sha, verb, file string) string {
	if sha == "" {
		return "The commit " + verb + " " + file
	}
	return sha + " " + verb + " " + file
}

func withExtra(lead, explanation string) string {
	extra := replyExtra(explanation)
	if extra == "" {
		return lead
	}
	return lead + " " + extra
}

func replyExtra(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = safeExplain(s)
	if s == "no further detail" {
		return ""
	}
	low := strings.ToLower(s)
	switch low {
	case "fix is correct", "fix is not correct", "the fix is correct", "the fix is not correct", "deferral accepted", "not applicable accepted":
		return ""
	default:
		return s
	}
}

func replySHA(info *PRInfo, d replyDecision, parsed ParsedReply) string {
	if d.fullSHA != "" {
		return shaLink(info, d.fullSHA)
	}
	if parsed.SHA == "" {
		return ""
	}
	return "`" + mdPathLabel(shortSHA(parsed.SHA)) + "`"
}

func replyFileLink(info *PRInfo, path string, line int) string {
	label := shortPathLabel(path, line)
	if !replyFileLinkable(info, path) {
		return "`" + label + "`"
	}
	if u := blobURL(info, path, line, 0); u != "" {
		return fmt.Sprintf("[`%s`](<%s>)", label, u)
	}
	return "`" + label + "`"
}

func replyFileLinkable(info *PRInfo, path string) bool {
	if info == nil || info.LinkFiles == nil {
		return true
	}
	for _, f := range info.LinkFiles {
		if f == path {
			return true
		}
	}
	return false
}

func shortPathLabel(path string, line int) string {
	label := path
	if strings.Contains(path, "/") {
		label = pathBase(path)
	}
	label = mdPathLabel(label)
	if line > 0 {
		label = fmt.Sprintf("%s:%d", label, line)
	}
	return label
}

func pathBase(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 && i < len(p)-1 {
		return p[i+1:]
	}
	return p
}

func safeExplain(s string) string {
	s = strings.ReplaceAll(s, "<!--", "")
	s = strings.ReplaceAll(s, "-->", "")
	s = mdInline(strings.TrimSpace(s))
	if s == "" {
		return "no further detail"
	}
	return s
}

// ReplyFailureNote is the one answer a webhook leaves when a judgment cannot be retried.
func ReplyFailureNote(commentID int64) string {
	return botMarker + "\n" + replyToMarker(commentID) + "\n\nThis reply could not be judged. Reply again in a little while.\n"
}

var errReplyDuplicate = errors.New("github: thread reply already posted")

// PostThreadReply posts one answer on the same thread as the developer comment.
func PostThreadReply(ctx stdctx.Context, client Client, info *PRInfo, req ThreadReplyRequest, body string) error {
	_, err := postedThreadReply(ctx, client, info, req, body)
	return err
}

func postedThreadReply(ctx stdctx.Context, client Client, info *PRInfo, req ThreadReplyRequest, body string) (bool, error) {
	err := postThreadReply(ctx, client, info, req, body)
	if errors.Is(err, errReplyDuplicate) {
		return false, nil
	}
	return err == nil, err
}

type replySlot struct {
	mu sync.Mutex
	n  int
}

var (
	replySlotsMu sync.Mutex
	replySlots   = map[string]*replySlot{}

	replyPostMu sync.Mutex
	replyPosted = map[string]time.Time{}
)

// replyClaimTTL keeps a fresh post from being repeated while a comment list is still stale.
const replyClaimTTL = 2 * time.Minute

// withReplySlot serializes the GitHub write for one human comment so a second judgment posts once.
func withReplySlot(key string, fn func() error) error {
	replySlotsMu.Lock()
	slot := replySlots[key]
	if slot == nil {
		slot = &replySlot{}
		replySlots[key] = slot
	}
	slot.n++
	replySlotsMu.Unlock()

	slot.mu.Lock()
	defer func() {
		slot.mu.Unlock()
		replySlotsMu.Lock()
		slot.n--
		if slot.n == 0 && replySlots[key] == slot {
			delete(replySlots, key)
		}
		replySlotsMu.Unlock()
	}()
	return fn()
}

func claimReplyPost(key string) bool {
	now := time.Now()
	replyPostMu.Lock()
	defer replyPostMu.Unlock()
	for k, until := range replyPosted {
		if !now.Before(until) {
			delete(replyPosted, k)
		}
	}
	if until, ok := replyPosted[key]; ok && now.Before(until) {
		return false
	}
	replyPosted[key] = now.Add(replyClaimTTL)
	return true
}

func unclaimReplyPost(key string) {
	replyPostMu.Lock()
	delete(replyPosted, key)
	replyPostMu.Unlock()
}

func postThreadReply(ctx stdctx.Context, client Client, info *PRInfo, req ThreadReplyRequest, body string) error {
	if info == nil || req.CommentID <= 0 {
		return fmt.Errorf("github: missing reply target")
	}
	key := fmt.Sprintf("%s/%s#%d:%d", info.Owner, info.Repo, info.Number, req.CommentID)
	return withReplySlot(key, func() error {
		visible, err := replyMarkerVisible(ctx, client, info, req)
		if err != nil {
			slog.Warn("thread reply list failed",
				"repo", info.Owner+"/"+info.Repo, "pr", info.Number,
				"comment_id", req.CommentID, "reason", "list_failed")
			visible = false
		}
		if visible || !claimReplyPost(key) {
			if visible {
				claimReplyPost(key)
			}
			slog.Info("thread reply already posted",
				"repo", info.Owner+"/"+info.Repo, "pr", info.Number,
				"comment_id", req.CommentID, "reason", "duplicate")
			return errReplyDuplicate
		}
		if req.Kind == "issue_comment" {
			_, err = client.CreateIssueComment(ctx, info.Owner, info.Repo, info.Number, &gh.IssueComment{Body: gh.Ptr(body)})
		} else {
			writer, ok := client.(reviewThreadWriter)
			if !ok {
				err = fmt.Errorf("github: client cannot reply on review threads")
			} else {
				_, err = writer.ReplyToReviewComment(ctx, info.Owner, info.Repo, info.Number, req.CommentID, body)
			}
		}
		if err != nil {
			unclaimReplyPost(key)
		}
		return err
	})
}

func replyMarkerVisible(ctx stdctx.Context, client Client, info *PRInfo, req ThreadReplyRequest) (bool, error) {
	if req.Kind == "issue_comment" {
		var comments []*gh.IssueComment
		err := withReadRetry(ctx, func() error {
			var e error
			comments, e = listAllIssueComments(ctx, client, info)
			return e
		})
		if err != nil {
			return false, err
		}
		return replyAlreadyPosted(nil, comments, req.CommentID), nil
	}
	var comments []*gh.PullRequestComment
	err := withReadRetry(ctx, func() error {
		var e error
		comments, e = listAllReviewComments(ctx, client, info)
		return e
	})
	if err != nil {
		return false, err
	}
	return replyAlreadyPosted(comments, nil, req.CommentID), nil
}

func resolveAcceptedThreads(ctx stdctx.Context, client Client, decisions []replyDecision, ledger []LedgerEntry, reviewComments []*gh.PullRequestComment) {
	writer, ok := client.(reviewThreadWriter)
	if !ok {
		return
	}
	seen := map[string]bool{}
	for _, d := range decisions {
		if d.target.Thread == "" || seen[d.target.Thread] {
			continue
		}
		if !threadClear(d.target.RootID, reviewComments, ledger) {
			continue
		}
		seen[d.target.Thread] = true
		_ = writer.ResolveReviewThread(ctx, d.target.Thread)
	}
}

func threadClear(root int64, comments []*gh.PullRequestComment, ledger []LedgerEntry) bool {
	bodies, _ := threadBodies(comments, nil, root, root)
	fps := fpsInBodies(bodies)
	if len(fps) == 0 {
		return false
	}
	status := make(map[string]string, len(ledger))
	for _, e := range ledger {
		status[e.FP] = e.Status
	}
	for _, fp := range fps {
		if ledgerBlocksApproval(status[fp]) {
			return false
		}
	}
	return true
}

func selectReplyTargets(req ThreadReplyRequest, reviewComments []*gh.PullRequestComment, issueComments []*gh.IssueComment, ledger []LedgerEntry, threads []ReviewThread) ([]replyTarget, bool, bool) {
	if req.Kind == "issue_comment" {
		targets, clarify := selectIssueReplyTargets(req.Body, reviewComments, issueComments, ledger)
		return targets, clarify, false
	}
	byFP := map[string]int{}
	for i, e := range ledger {
		byFP[e.FP] = i
	}
	root := threadRootID(reviewComments, req.CommentID, req.InReplyTo)
	bodies, threadID := threadBodies(reviewComments, threads, root, req.CommentID)
	fps := fpsInBodies(bodies)
	if len(fps) == 0 {
		return nil, false, false
	}
	var targets []replyTarget
	for _, fp := range fps {
		idx, ok := byFP[fp]
		if !ok {
			continue
		}
		targets = append(targets, replyTarget{Index: idx, Entry: ledger[idx], Body: findingBody(bodies, fp), Thread: threadID, RootID: root})
	}
	if len(targets) == 0 {
		return nil, false, true
	}
	return filterTargetsByReply(req.Body, targets), false, false
}

func selectIssueReplyTargets(body string, reviewComments []*gh.PullRequestComment, issueComments []*gh.IssueComment, ledger []LedgerEntry) ([]replyTarget, bool) {
	bodies := offDiffBodies(issueComments, reviewComments)
	known := map[string]bool{}
	for _, fp := range fpsInBodies(bodies) {
		known[fp] = true
	}
	var posted, named, blocking []replyTarget
	for i, e := range ledger {
		if !ledgerBlocksApproval(e.Status) {
			continue
		}
		target := replyTarget{Index: i, Entry: e, Body: findingBody(bodies, e.FP)}
		blocking = append(blocking, target)
		if len(known) == 0 || known[e.FP] {
			posted = append(posted, target)
		}
		if replyNamesTarget(body, e.FP, e.Path, e.Line) {
			named = append(named, target)
		}
	}
	if len(named) > 0 {
		return named, false
	}
	if len(posted) == 1 {
		return posted, false
	}
	if len(posted) == 0 && len(blocking) == 1 {
		return blocking, false
	}
	if len(blocking) == 0 {
		if len(ledger) == 0 {
			return nil, false
		}
		handled := make([]replyTarget, len(ledger))
		for i, e := range ledger {
			handled[i] = replyTarget{Index: i, Entry: e}
		}
		return handled, false
	}
	return nil, true
}

func replyNamesTarget(body, fp, path string, line int) bool {
	lower := strings.ToLower(body)
	if fp != "" && strings.Contains(lower, strings.ToLower(fp)) {
		return true
	}
	if line <= 0 || path == "" {
		return false
	}
	label := strings.ToLower(fmt.Sprintf("%s:%d", path, line))
	for from := 0; from < len(lower); {
		rel := strings.Index(lower[from:], label)
		if rel < 0 {
			return false
		}
		i := from + rel
		end := i + len(label)
		rightOK := end >= len(lower) || lower[end] < '0' || lower[end] > '9'
		leftOK := i == 0 || !isPathByte(lower[i-1])
		if rightOK && leftOK {
			return true
		}
		from = i + 1
	}
	return false
}

func isPathByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '/' || b == '.' || b == '_' || b == '-'
}

func filterTargetsByReply(body string, targets []replyTarget) []replyTarget {
	if len(targets) <= 1 {
		return targets
	}
	lower := strings.ToLower(body)
	var hit []replyTarget
	seen := map[string]bool{}
	for _, t := range targets {
		if replyNamesTarget(lower, t.Entry.FP, t.Entry.Path, t.Entry.Line) {
			if !seen[t.Entry.FP] {
				seen[t.Entry.FP] = true
				hit = append(hit, t)
			}
		}
	}
	if len(hit) > 0 {
		return hit
	}
	return targets
}

func offDiffBodies(issueComments []*gh.IssueComment, reviewComments []*gh.PullRequestComment) []string {
	var bodies []string
	for _, c := range reviewComments {
		if strings.Contains(c.GetBody(), offDiffMarker) || strings.Contains(c.GetBody(), fpPrefix) {
			bodies = append(bodies, c.GetBody())
		}
	}
	for _, c := range issueComments {
		if strings.Contains(c.GetBody(), offDiffMarker) || strings.Contains(c.GetBody(), fpPrefix) {
			bodies = append(bodies, c.GetBody())
		}
	}
	return bodies
}

func threadBodies(comments []*gh.PullRequestComment, threads []ReviewThread, root, commentID int64) ([]string, string) {
	var bodies []string
	for _, c := range comments {
		id := c.GetID()
		if id == root || id == commentID || c.GetInReplyTo() == root || threadRootID(comments, id, c.GetInReplyTo()) == root {
			bodies = append(bodies, c.GetBody())
		}
	}
	threadID := ""
	for _, th := range threads {
		for _, c := range th.Comments {
			if c.ID == root || c.ID == commentID {
				threadID = th.ID
			}
		}
	}
	return bodies, threadID
}

func threadRootID(comments []*gh.PullRequestComment, id, parent int64) int64 {
	byID := map[int64]*gh.PullRequestComment{}
	for _, c := range comments {
		byID[c.GetID()] = c
	}
	cur := id
	if cur == 0 || byID[cur] == nil {
		cur = parent
	}
	seen := map[int64]bool{}
	for cur > 0 && !seen[cur] {
		seen[cur] = true
		c := byID[cur]
		if c == nil || c.GetInReplyTo() == 0 {
			return cur
		}
		cur = c.GetInReplyTo()
	}
	if parent > 0 {
		return parent
	}
	return id
}

func fpsInBodies(bodies []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, body := range bodies {
		for _, m := range fpMarkerRe.FindAllStringSubmatch(body, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				out = append(out, m[1])
			}
		}
	}
	return out
}

func findingBody(bodies []string, fp string) string {
	needle := fpMarker(fp)
	for _, body := range bodies {
		if strings.Contains(body, needle) {
			return body
		}
	}
	return ""
}

func replyAlreadyPosted(reviewComments []*gh.PullRequestComment, issueComments []*gh.IssueComment, commentID int64) bool {
	needle := replyToMarker(commentID)
	for _, c := range reviewComments {
		if strings.Contains(c.GetBody(), needle) {
			return true
		}
	}
	for _, c := range issueComments {
		if strings.Contains(c.GetBody(), needle) {
			return true
		}
	}
	return false
}

func loadReviewThreads(ctx stdctx.Context, client Client, info *PRInfo) ([]ReviewThread, error) {
	rtc, ok := client.(reviewThreadsClient)
	if !ok {
		return nil, nil
	}
	return rtc.ReviewThreads(ctx, info.Owner, info.Repo, info.Number)
}

// ListActionableReplies returns human verdict replies that do not yet have a
// miu-cr answer. The host poll uses this so a reply is handled without waiting
// for the next full review.
func ListActionableReplies(ctx stdctx.Context, client Client, info *PRInfo, botLogin string) ([]ThreadReplyRequest, error) {
	if info == nil {
		return nil, nil
	}
	login := botLogin
	if login == "" {
		login, _ = client.CurrentLogin(ctx)
	}
	reviewComments, err := listAllReviewComments(ctx, client, info)
	if err != nil {
		return nil, err
	}
	issueComments, err := listAllIssueComments(ctx, client, info)
	if err != nil {
		return nil, err
	}
	var out []ThreadReplyRequest
	for _, c := range reviewComments {
		if len(out) >= 8 {
			break
		}
		req := ThreadReplyRequest{CommentID: c.GetID(), Body: c.GetBody(), UserLogin: c.GetUser().GetLogin(), AuthorAssociation: c.GetAuthorAssociation(), InReplyTo: c.GetInReplyTo(), Kind: "review_comment"}
		if !actionableReply(req, login, info.AuthorLogin, reviewComments, issueComments) {
			continue
		}
		if !threadHasFingerprint(reviewComments, req) {
			continue
		}
		if !threadOwnedBy(reviewComments, req, login) {
			continue
		}
		out = append(out, req)
	}
	for _, c := range issueComments {
		if len(out) >= 8 {
			break
		}
		req := ThreadReplyRequest{CommentID: c.GetID(), Body: c.GetBody(), UserLogin: c.GetUser().GetLogin(), AuthorAssociation: c.GetAuthorAssociation(), Kind: "issue_comment"}
		if !actionableReply(req, login, info.AuthorLogin, reviewComments, issueComments) {
			continue
		}
		out = append(out, req)
	}
	return out, nil
}

func actionableReply(req ThreadReplyRequest, botLogin, authorLogin string, reviewComments []*gh.PullRequestComment, issueComments []*gh.IssueComment) bool {
	if req.CommentID <= 0 || IsBotBody(req.Body) {
		return false
	}
	if botLogin != "" && strings.EqualFold(req.UserLogin, botLogin) {
		return false
	}
	if ParseThreadReply(req.Body).Intent == "" {
		return false
	}
	if !ReplyAuthorized(req.UserLogin, req.AuthorAssociation, authorLogin) {
		return false
	}
	return !replyAlreadyPosted(reviewComments, issueComments, req.CommentID)
}

func threadHasFingerprint(comments []*gh.PullRequestComment, req ThreadReplyRequest) bool {
	root := threadRootID(comments, req.CommentID, req.InReplyTo)
	bodies, _ := threadBodies(comments, nil, root, req.CommentID)
	return len(fpsInBodies(bodies)) > 0
}

func threadOwnedBy(comments []*gh.PullRequestComment, req ThreadReplyRequest, login string) bool {
	if strings.TrimSpace(login) == "" {
		return true
	}
	root := threadRootID(comments, req.CommentID, req.InReplyTo)
	for _, c := range comments {
		if c.GetID() != root {
			continue
		}
		author := c.GetUser().GetLogin()
		if author == "" {
			return true
		}
		return strings.EqualFold(author, login)
	}
	return true
}

func listAllReviewComments(ctx stdctx.Context, client Client, info *PRInfo) ([]*gh.PullRequestComment, error) {
	var all []*gh.PullRequestComment
	opts := &gh.PullRequestListCommentsOptions{ListOptions: gh.ListOptions{PerPage: 100}}
	for page := 0; page < maxConvPages; page++ {
		comments, resp, err := client.ListReviewComments(ctx, info.Owner, info.Repo, info.Number, opts)
		if err != nil {
			return nil, err
		}
		all = append(all, comments...)
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return all, nil
}

func listAllIssueComments(ctx stdctx.Context, client Client, info *PRInfo) ([]*gh.IssueComment, error) {
	var all []*gh.IssueComment
	opts := &gh.IssueListCommentsOptions{ListOptions: gh.ListOptions{PerPage: 100}}
	for page := 0; page < maxConvPages; page++ {
		comments, resp, err := client.ListIssueComments(ctx, info.Owner, info.Repo, info.Number, opts)
		if err != nil {
			return nil, err
		}
		all = append(all, comments...)
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return all, nil
}

func matchCommitSHA(want string, shas []string) (string, bool) {
	want = strings.ToLower(strings.TrimSpace(want))
	var hit string
	for _, sha := range shas {
		sha = strings.ToLower(sha)
		if sha == want || (len(want) >= 7 && strings.HasPrefix(sha, want)) {
			if hit != "" && hit != sha {
				return "", false
			}
			hit = sha
		}
	}
	return hit, hit != ""
}

func patchTouchesLine(patch string, line int) bool {
	if line <= 0 {
		return true
	}
	for _, h := range diff.ParseHunks(patch) {
		if h.NewCount == 0 {
			continue
		}
		end := h.NewStart + h.NewCount - 1
		if line >= h.NewStart && line <= end {
			return true
		}
	}
	return false
}

func shortSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func clipText(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
