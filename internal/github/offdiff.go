package github

import (
	stdctx "context"
	"fmt"
	"sort"
	"strings"

	gh "github.com/google/go-github/v84/github"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/diff"
)

const (
	offDiffInline = "inline"
	offDiffThread = "thread"
	offDiffNoise  = "noise"
)

// ClassifyOffDiff reports where a finding can be answered. inline means the
// line sits on a changed hunk. thread means it is still relevant but cannot be
// anchored on its own line. noise is an info-level note in a file this pull
// request does not change.
func ClassifyOffDiff(f engine.Finding, diffs []diff.Diff) string {
	if findingOnChangedLine(f, diffs) {
		return offDiffInline
	}
	if !fileInDiff(f.File, diffs) && severityLabel(f.Severity) == "info" {
		return offDiffNoise
	}
	return offDiffThread
}

// ApplyOffDiffDisposition marks info-level findings in untouched files as
// irrelevant and promotes a previously irrelevant finding once it is actionable
// again. Accepted deferrals and commit resolutions are left alone.
func ApplyOffDiffDisposition(entries []LedgerEntry, findings []engine.Finding, diffs []diff.Diff) []LedgerEntry {
	out := append([]LedgerEntry(nil), entries...)
	byFP := make(map[string]int, len(out))
	for i := range out {
		byFP[out[i].FP] = i
	}
	for _, f := range findings {
		i, ok := byFP[Fingerprint(f)]
		if !ok {
			continue
		}
		e := &out[i]
		if e.Status == statusDeferred || e.Status == statusResolved {
			continue
		}
		switch ClassifyOffDiff(f, diffs) {
		case offDiffNoise:
			if e.Status == statusOpen || e.Status == statusReopened {
				e.Status = statusIrrelevant
				e.Note = ""
			}
		default:
			if e.Status == statusIrrelevant {
				e.Status = statusOpen
				e.Note = ""
			}
		}
	}
	return out
}

// BlockingFindings drops accepted deferrals and irrelevant off-diff noise so
// they neither gate approval nor get posted again.
func BlockingFindings(findings []engine.Finding, ledger []LedgerEntry) []engine.Finding {
	status := make(map[string]string, len(ledger))
	for _, e := range ledger {
		status[e.FP] = e.Status
	}
	out := make([]engine.Finding, 0, len(findings))
	for _, f := range findings {
		if ledgerBlocksApproval(status[Fingerprint(f)]) {
			out = append(out, f)
		}
	}
	return out
}

// ActionableOffDiffFindings are relevant findings that cannot sit on their own
// changed line. They share one review thread.
func ActionableOffDiffFindings(findings []engine.Finding, diffs []diff.Diff, ledger []LedgerEntry) []engine.Finding {
	out := make([]engine.Finding, 0)
	for _, f := range BlockingFindings(findings, ledger) {
		if ClassifyOffDiff(f, diffs) == offDiffThread {
			out = append(out, f)
		}
	}
	return out
}

// NearestAnchor picks a RIGHT-side line that can hold one review comment for
// findings outside the hunks. It prefers the finding's own file.
func NearestAnchor(f engine.Finding, diffs []diff.Diff) (string, int, bool) {
	if path, line, ok := nearestInFile(f.File, f.Line, diffs); ok {
		return path, line, true
	}
	for i := range diffs {
		path := diffs[i].NewPath
		if path == "" || path == "/dev/null" || path == f.File {
			continue
		}
		if lines := rightSideLines(&diffs[i]); len(lines) > 0 {
			return path, lines[0], true
		}
	}
	return "", 0, false
}

func nearestInFile(path string, line int, diffs []diff.Diff) (string, int, bool) {
	for i := range diffs {
		if diffs[i].NewPath != path {
			continue
		}
		lines := rightSideLines(&diffs[i])
		if len(lines) == 0 {
			return "", 0, false
		}
		best := lines[0]
		bestDist := absInt(best - line)
		for _, ln := range lines[1:] {
			if d := absInt(ln - line); d < bestDist {
				best, bestDist = ln, d
			}
		}
		return path, best, true
	}
	return "", 0, false
}

func rightSideLines(d *diff.Diff) []int {
	var lines []int
	for _, h := range diff.ParseHunks(d.Diff) {
		n := h.NewStart
		for _, l := range h.Lines {
			switch l.Type {
			case diff.HunkContext, diff.HunkAdded:
				lines = append(lines, n)
				n++
			}
		}
	}
	return lines
}

func findingOnChangedLine(f engine.Finding, diffs []diff.Diff) bool {
	if f.Line <= 0 || f.File == "" {
		return false
	}
	sets := hunkRightSets(diffs)
	for _, set := range sets[f.File] {
		if set[f.Line] {
			return true
		}
	}
	return false
}

func fileInDiff(path string, diffs []diff.Diff) bool {
	if path == "" {
		return false
	}
	for i := range diffs {
		if diffs[i].NewPath == path {
			return true
		}
	}
	return false
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// RenderOffDiffComment is the shared thread body for relevant off-diff findings.
func RenderOffDiffComment(findings []engine.Finding) string {
	var b strings.Builder
	b.WriteString(offDiffMarker + "\n")
	b.WriteString(botMarker + "\n\n")
	b.WriteString("These findings sit outside the changed lines. ")
	b.WriteString(threadReplyHint)
	b.WriteString("\n\n")
	for _, f := range findings {
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", loc, f.Line)
		}
		fmt.Fprintf(&b, "- **%s** `%s`", severityLabel(f.Severity), mdPathLabel(loc))
		if title := mdInline(f.Title); title != "" {
			fmt.Fprintf(&b, " %s", title)
		}
		b.WriteString("\n\n")
		if rationale := mdInline(f.Rationale); rationale != "" {
			b.WriteString(rationale + "\n\n")
		}
		b.WriteString(fpMarker(Fingerprint(f)) + "\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

type reviewCommentEditor interface {
	EditReviewComment(ctx stdctx.Context, owner, repo string, commentID int64, body string) error
}

// UpsertOffDiffComment posts or edits the single off-diff thread. An empty
// finding list clears an existing thread and does not create one.
func UpsertOffDiffComment(ctx stdctx.Context, client Client, info *PRInfo, findings []engine.Finding, diffs []diff.Diff) (string, error) {
	if info == nil {
		return "", nil
	}
	login, err := client.CurrentLogin(ctx)
	if err != nil || strings.TrimSpace(login) == "" {
		return "", err
	}
	comments, err := listAllReviewComments(ctx, client, info)
	if err != nil {
		return "", err
	}
	issueComments, err := listAllIssueComments(ctx, client, info)
	if err != nil {
		return "", err
	}
	existing := findOffDiffComment(comments, login)
	issues := findOffDiffIssueComments(issueComments, login)
	if len(findings) == 0 {
		if existing == nil && len(issues) == 0 {
			return "", nil
		}
		body := offDiffMarker + "\n" + botMarker + "\n\nNo off-diff findings remain.\n"
		if existing != nil {
			if err := editOffDiffComment(ctx, client, info, existing, body); err != nil {
				return "", err
			}
		}
		if err := editOffDiffIssueComments(ctx, client, info, issues, body); err != nil {
			return "", err
		}
		return offDiffExistingURL(existing, issues), nil
	}
	body := RenderOffDiffComment(findings)
	if existing != nil || len(issues) > 0 {
		if existing != nil {
			if err := editOffDiffComment(ctx, client, info, existing, body); err != nil {
				return "", err
			}
		}
		if err := editOffDiffIssueComments(ctx, client, info, issues, body); err != nil {
			return "", err
		}
		return offDiffExistingURL(existing, issues), nil
	} else if path, line, ok := NearestAnchor(findings[0], diffs); ok {
		review := &gh.PullRequestReviewRequest{
			CommitID: gh.Ptr(info.HeadSHA),
			Event:    gh.Ptr("COMMENT"),
			Comments: []*gh.DraftReviewComment{{
				Path: gh.Ptr(path),
				Body: gh.Ptr(body),
				Side: gh.Ptr("RIGHT"),
				Line: gh.Ptr(line),
			}},
		}
		if _, cerr := client.CreateReview(ctx, info.Owner, info.Repo, info.Number, review); cerr == nil {
			return offDiffCommentURL(ctx, client, info)
		}
	}
	created, err := client.CreateIssueComment(ctx, info.Owner, info.Repo, info.Number, &gh.IssueComment{Body: gh.Ptr(body)})
	if err != nil {
		return "", err
	}
	return created.GetHTMLURL(), nil
}

func findOffDiffIssueComments(comments []*gh.IssueComment, login string) []*gh.IssueComment {
	var found []*gh.IssueComment
	for _, c := range comments {
		if !strings.Contains(c.GetBody(), offDiffMarker) {
			continue
		}
		if login != "" && !strings.EqualFold(c.GetUser().GetLogin(), login) {
			continue
		}
		found = append(found, c)
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].GetID() < found[j].GetID() })
	return found
}

func editOffDiffIssueComments(ctx stdctx.Context, client Client, info *PRInfo, comments []*gh.IssueComment, body string) error {
	for _, c := range comments {
		if _, err := client.EditIssueComment(ctx, info.Owner, info.Repo, c.GetID(), &gh.IssueComment{Body: gh.Ptr(body)}); err != nil {
			return err
		}
	}
	return nil
}

func offDiffExistingURL(review *gh.PullRequestComment, issues []*gh.IssueComment) string {
	if review != nil && review.GetHTMLURL() != "" {
		return review.GetHTMLURL()
	}
	if len(issues) > 0 && issues[0].GetHTMLURL() != "" {
		return issues[0].GetHTMLURL()
	}
	return ""
}

func findOffDiffComment(comments []*gh.PullRequestComment, login string) *gh.PullRequestComment {
	var found *gh.PullRequestComment
	for _, c := range comments {
		if c.GetInReplyTo() != 0 || !strings.Contains(c.GetBody(), offDiffMarker) {
			continue
		}
		if login != "" && !strings.EqualFold(c.GetUser().GetLogin(), login) {
			continue
		}
		if found == nil || c.GetID() < found.GetID() {
			found = c
		}
	}
	return found
}

func editOffDiffComment(ctx stdctx.Context, client Client, info *PRInfo, existing *gh.PullRequestComment, body string) error {
	editor, ok := client.(reviewCommentEditor)
	if !ok {
		return fmt.Errorf("github: client cannot edit review comments")
	}
	return editor.EditReviewComment(ctx, info.Owner, info.Repo, existing.GetID(), body)
}

func offDiffCommentURL(ctx stdctx.Context, client Client, info *PRInfo) (string, error) {
	comments, err := listAllReviewComments(ctx, client, info)
	if err != nil {
		return "", err
	}
	login, _ := client.CurrentLogin(ctx)
	if c := findOffDiffComment(comments, login); c != nil {
		return c.GetHTMLURL(), nil
	}
	return "", nil
}
