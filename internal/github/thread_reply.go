package github

import (
	stdctx "context"
	"fmt"
	"regexp"
	"strings"
	"time"

	gh "github.com/google/go-github/v84/github"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine/diff"
)

const (
	botMarker       = "<!-- miu-cr-bot -->"
	offDiffMarker   = "<!-- miu-cr-offdiff -->"
	noticeMarker    = "<!-- miu-cr-notice -->"
	threadReplyHint = "Reply on this thread with `Fixed in <sha>: ...`, `Deferred: ...`, or `Not applicable: ...`."
)

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
type ThreadReplyVerdict struct {
	Accept      bool
	Explanation string
}

// ThreadReplyResult is the outcome of handling one comment.
type ThreadReplyResult struct {
	Action        string
	Reason        string
	Accepted      int
	Rejected      int
	Approved      bool
	ApproveReason string
}

type commitInspector interface {
	PullRequestCommitSHAs(ctx stdctx.Context, owner, repo string, number int) ([]string, error)
	CommitFilePatch(ctx stdctx.Context, owner, repo, sha, path string) (found bool, patch string, err error)
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

	reviewComments, err := listAllReviewComments(ctx, client, info)
	if err != nil {
		return ThreadReplyResult{Reason: "list_review_comments_failed"}, mapWriteError("github.thread_reply_failed", "listing review comments", err)
	}
	issueComments, err := listAllIssueComments(ctx, client, info)
	if err != nil {
		return ThreadReplyResult{Reason: "list_issue_comments_failed"}, mapWriteError("github.thread_reply_failed", "listing issue comments", err)
	}
	if replyAlreadyPosted(reviewComments, issueComments, req.CommentID) {
		return ThreadReplyResult{Action: "ignored", Reason: "duplicate"}, nil
	}

	if login == "" {
		return ThreadReplyResult{Reason: "comment_author_unverified"}, nil
	}
	summaryID, _, summaryBody, err := lowestMarkedComment(ctx, client, info, login)
	if err != nil {
		return ThreadReplyResult{Reason: "summary_fetch_failed"}, mapWriteError("github.thread_reply_failed", "listing summary", err)
	}
	if summaryID == 0 {
		return ThreadReplyResult{Action: "ignored", Reason: "no_summary"}, nil
	}
	ledger := ParseLedger(summaryBody)
	if ledger == nil {
		return ThreadReplyResult{Action: "ignored", Reason: "no_ledger"}, nil
	}

	threads, _ := loadReviewThreads(ctx, client, info)
	targets, clarify := selectReplyTargets(req, reviewComments, issueComments, ledger, threads)
	if clarify {
		body := botMarker + "\n" + replyToMarker(req.CommentID) + "\n\nName the finding as `path:line`, or reply on its thread. A pull request comment is used only when one finding is still open.\n"
		if err := postThreadReply(ctx, client, info, req, body); err != nil {
			return ThreadReplyResult{Reason: "reply_post_failed"}, mapWriteError("github.thread_reply_failed", "posting clarification", err)
		}
		return ThreadReplyResult{Action: "replied", Reason: "clarification"}, nil
	}
	if len(targets) == 0 {
		body := botMarker + "\n" + replyToMarker(req.CommentID) + "\n\nThis comment is not on a miu-cr finding thread.\n"
		if err := postThreadReply(ctx, client, info, req, body); err != nil {
			return ThreadReplyResult{Reason: "reply_post_failed"}, mapWriteError("github.thread_reply_failed", "posting reply", err)
		}
		return ThreadReplyResult{Action: "replied", Reason: "not_our_thread"}, nil
	}

	decisions := make([]replyDecision, 0, len(targets))
	for _, target := range targets {
		if !ledgerBlocksApproval(ledger[target.Index].Status) {
			decisions = append(decisions, replyDecision{target: target, skip: true, verdict: ThreadReplyVerdict{Accept: true, Explanation: "already handled"}})
			continue
		}
		verdict, fullSHA, err := decideReply(ctx, client, info, parsed, target, judge)
		if err != nil {
			return ThreadReplyResult{Reason: "judge_failed"}, err
		}
		decisions = append(decisions, replyDecision{target: target, verdict: verdict, fullSHA: fullSHA})
	}

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
		replyBody := renderThreadReply(req.CommentID, parsed, decisions)
		if err := postThreadReply(ctx, client, info, req, replyBody); err != nil {
			return ThreadReplyResult{Reason: "reply_post_failed"}, mapWriteError("github.thread_reply_failed", "posting reply", err)
		}
		return ThreadReplyResult{Action: "replied", Reason: "already_handled"}, nil
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
		return ThreadReplyResult{Reason: "summary_shape_unsupported"}, nil
	}
	if _, err := client.EditIssueComment(ctx, info.Owner, info.Repo, summaryID, &gh.IssueComment{Body: gh.Ptr(nextBody)}); err != nil {
		return ThreadReplyResult{Reason: "summary_edit_failed"}, mapWriteError("github.thread_reply_failed", "editing summary", err)
	}

	replyBody := renderThreadReply(req.CommentID, parsed, decisions)
	if err := postThreadReply(ctx, client, info, req, replyBody); err != nil {
		return ThreadReplyResult{Accepted: accepted, Rejected: rejected, Reason: "reply_post_failed"}, mapWriteError("github.thread_reply_failed", "posting reply", err)
	}
	resolveAcceptedThreads(ctx, client, decisions, next, reviewComments)

	result := ThreadReplyResult{Action: "replied", Reason: "replied", Accepted: accepted, Rejected: rejected}
	if rejected == 0 && LedgerClearForApproval(next) {
		if reviewedHead := parsePublishedCommit(summaryBody); reviewedHead != "" && strings.EqualFold(reviewedHead, info.HeadSHA) {
			result.Approved, result.ApproveReason = ApproveResolvedLedger(ctx, client, info, policy, summaryCommentURL(info, summaryID, ""))
		}
	}
	if _, nerr := UpsertResponseNotice(ctx, client, info, login, OpenNoticeItems(next, inlineURLs, nil)); nerr != nil {
		result.Reason = "notice_failed"
	}
	return result, nil
}

type replyDecision struct {
	target  replyTarget
	verdict ThreadReplyVerdict
	fullSHA string
	skip    bool
}

func decideReply(ctx stdctx.Context, client Client, info *PRInfo, parsed ParsedReply, target replyTarget, judge ThreadReplyJudge) (ThreadReplyVerdict, string, error) {
	if parsed.Intent == ReplyFix {
		check, explain, err := inspectFixCommit(ctx, client, info, parsed.SHA, target.Entry)
		if err != nil {
			return ThreadReplyVerdict{}, "", err
		}
		if explain != "" {
			return ThreadReplyVerdict{Accept: false, Explanation: explain}, "", nil
		}
		if judge == nil {
			return deterministicFixVerdict(check, target.Entry), check.fullSHA, nil
		}
		verdict, err := judge.JudgeThreadReply(ctx, ThreadReplyJudgeInput{
			Intent: parsed.Intent, Path: target.Entry.Path, Line: target.Entry.Line, Title: target.Entry.Title,
			Severity: target.Entry.Sev, FindingBody: clipText(target.Body, 2000), Reply: parsed.Reason,
			Reason: parsed.Reason, CommitSHA: check.fullSHA, CommitPatch: clipText(check.patch, 6000), LineInPatch: check.lineInPatch,
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
}

func inspectFixCommit(ctx stdctx.Context, client Client, info *PRInfo, sha string, entry LedgerEntry) (fixCheck, string, error) {
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
		return fixCheck{}, fmt.Sprintf("commit `%s` is not on this pull request", shortSHA(sha)), nil
	}
	found, patch, err := inspector.CommitFilePatch(ctx, info.Owner, info.Repo, full, entry.Path)
	if err != nil {
		return fixCheck{}, "", err
	}
	if !found {
		return fixCheck{}, fmt.Sprintf("commit `%s` does not change `%s`", shortSHA(full), entry.Path), nil
	}
	lineInPatch := entry.Line <= 0 || strings.TrimSpace(patch) == "" || patchTouchesLine(patch, entry.Line)
	return fixCheck{fullSHA: full, patch: patch, lineInPatch: lineInPatch, fileChanged: true}, "", nil
}

func deterministicFixVerdict(check fixCheck, entry LedgerEntry) ThreadReplyVerdict {
	if entry.Line > 0 && strings.TrimSpace(check.patch) != "" && !check.lineInPatch {
		return ThreadReplyVerdict{Accept: false, Explanation: fmt.Sprintf("commit `%s` changes `%s` but not around line %d", shortSHA(check.fullSHA), entry.Path, entry.Line)}
	}
	return ThreadReplyVerdict{Accept: true, Explanation: fmt.Sprintf("commit `%s` changes `%s`", shortSHA(check.fullSHA), entry.Path)}
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

func renderThreadReply(commentID int64, parsed ParsedReply, decisions []replyDecision) string {
	var b strings.Builder
	b.WriteString(botMarker + "\n")
	b.WriteString(replyToMarker(commentID) + "\n\n")
	for _, d := range decisions {
		loc := d.target.Entry.Path
		if d.target.Entry.Line > 0 {
			loc = fmt.Sprintf("%s:%d", loc, d.target.Entry.Line)
		}
		explain := safeExplain(d.verdict.Explanation)
		switch {
		case d.skip:
			fmt.Fprintf(&b, "- `%s` is already handled.\n", mdPathLabel(loc))
		case d.verdict.Accept && parsed.Intent == ReplyDefer:
			fmt.Fprintf(&b, "- Deferred `%s`: %s\n", mdPathLabel(loc), explain)
		case d.verdict.Accept:
			fmt.Fprintf(&b, "- Resolved `%s`: %s\n", mdPathLabel(loc), explain)
		default:
			fmt.Fprintf(&b, "- Still open `%s`: %s\n", mdPathLabel(loc), explain)
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
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

func postThreadReply(ctx stdctx.Context, client Client, info *PRInfo, req ThreadReplyRequest, body string) error {
	if req.Kind == "issue_comment" {
		_, err := client.CreateIssueComment(ctx, info.Owner, info.Repo, info.Number, &gh.IssueComment{Body: gh.Ptr(body)})
		return err
	}
	writer, ok := client.(reviewThreadWriter)
	if !ok {
		return fmt.Errorf("github: client cannot reply on review threads")
	}
	_, err := writer.ReplyToReviewComment(ctx, info.Owner, info.Repo, info.Number, req.CommentID, body)
	return err
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

func selectReplyTargets(req ThreadReplyRequest, reviewComments []*gh.PullRequestComment, issueComments []*gh.IssueComment, ledger []LedgerEntry, threads []ReviewThread) ([]replyTarget, bool) {
	if req.Kind == "issue_comment" {
		return selectIssueReplyTargets(req.Body, reviewComments, issueComments, ledger)
	}
	byFP := map[string]int{}
	for i, e := range ledger {
		byFP[e.FP] = i
	}
	root := threadRootID(reviewComments, req.CommentID, req.InReplyTo)
	bodies, threadID := threadBodies(reviewComments, threads, root, req.CommentID)
	fps := fpsInBodies(bodies)
	if len(fps) == 0 {
		return nil, false
	}
	var targets []replyTarget
	for _, fp := range fps {
		idx, ok := byFP[fp]
		if !ok {
			continue
		}
		targets = append(targets, replyTarget{Index: idx, Entry: ledger[idx], Body: findingBody(bodies, fp), Thread: threadID, RootID: root})
	}
	return filterTargetsByReply(req.Body, targets), false
}

func selectIssueReplyTargets(body string, reviewComments []*gh.PullRequestComment, issueComments []*gh.IssueComment, ledger []LedgerEntry) ([]replyTarget, bool) {
	bodies := offDiffBodies(issueComments, reviewComments)
	known := map[string]bool{}
	for _, fp := range fpsInBodies(bodies) {
		known[fp] = true
	}
	var open []replyTarget
	lower := strings.ToLower(body)
	var named []replyTarget
	for i, e := range ledger {
		if !ledgerBlocksApproval(e.Status) {
			continue
		}
		if len(known) > 0 && !known[e.FP] {
			continue
		}
		target := replyTarget{Index: i, Entry: e, Body: findingBody(bodies, e.FP)}
		open = append(open, target)
		label := strings.ToLower(fmt.Sprintf("%s:%d", e.Path, e.Line))
		if strings.Contains(lower, e.FP) || (e.Line > 0 && strings.Contains(lower, label)) {
			named = append(named, target)
		}
	}
	if len(named) > 0 {
		return named, false
	}
	if len(open) == 1 {
		return open, false
	}
	if len(open) == 0 {
		return nil, false
	}
	return nil, true
}

func filterTargetsByReply(body string, targets []replyTarget) []replyTarget {
	if len(targets) <= 1 {
		return targets
	}
	lower := strings.ToLower(body)
	var hit []replyTarget
	seen := map[string]bool{}
	for _, t := range targets {
		label := strings.ToLower(fmt.Sprintf("%s:%d", t.Entry.Path, t.Entry.Line))
		if strings.Contains(lower, t.Entry.FP) || (t.Entry.Line > 0 && strings.Contains(lower, label)) {
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
func ListActionableReplies(ctx stdctx.Context, client Client, info *PRInfo) ([]ThreadReplyRequest, error) {
	if info == nil {
		return nil, nil
	}
	login, _ := client.CurrentLogin(ctx)
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
	return s[:n]
}
