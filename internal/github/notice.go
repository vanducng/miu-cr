package github

import (
	stdctx "context"
	"fmt"
	"strings"

	gh "github.com/google/go-github/v84/github"
)

// NoticeItem is one open finding a developer can answer.
type NoticeItem struct {
	Path  string
	Line  int
	Title string
	URL   string
}

func OpenNoticeItems(entries []LedgerEntry, inlineURLs, extra map[string]string) []NoticeItem {
	var items []NoticeItem
	for _, e := range entries {
		if !ledgerBlocksApproval(e.Status) {
			continue
		}
		url := ""
		if inlineURLs != nil {
			url = inlineURLs[e.FP]
		}
		if url == "" && extra != nil {
			url = extra[e.FP]
		}
		items = append(items, NoticeItem{Path: e.Path, Line: e.Line, Title: e.Title, URL: url})
	}
	return items
}

// RenderResponseNotice is the single per-run comment. author is mentioned only
// while findings are open, and only when the login is a plain GitHub login.
func RenderResponseNotice(author string, items []NoticeItem) string {
	var b strings.Builder
	b.WriteString(noticeMarker + "\n")
	b.WriteString(botMarker + "\n\n")
	if len(items) == 0 {
		b.WriteString("No open findings need a response.\n")
		return b.String()
	}
	if githubLogin(author) {
		fmt.Fprintf(&b, "@%s ", author)
	}
	fmt.Fprintf(&b, "%d open finding%s need a response:\n\n", len(items), plural(len(items)))
	for _, item := range items {
		loc := mdPathLabel(item.Path)
		if item.Line > 0 {
			loc = fmt.Sprintf("%s:%d", loc, item.Line)
		}
		title := mdInline(item.Title)
		if item.URL != "" {
			fmt.Fprintf(&b, "- [`%s`](<%s>)", loc, item.URL)
		} else {
			fmt.Fprintf(&b, "- `%s`", loc)
		}
		if title != "" {
			fmt.Fprintf(&b, " %s", title)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n" + threadReplyHint + "\n")
	return b.String()
}

func githubLogin(login string) bool {
	if login == "" || len(login) > 39 {
		return false
	}
	for _, r := range login {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

// UpsertResponseNotice edits the existing notice in place. It does not create a
// notice when nothing is open.
func UpsertResponseNotice(ctx stdctx.Context, client Client, info *PRInfo, login string, items []NoticeItem) (string, error) {
	if info == nil || strings.TrimSpace(login) == "" {
		return "", nil
	}
	comments, err := listAllIssueComments(ctx, client, info)
	if err != nil {
		return "", err
	}
	existing := findNoticeComment(comments, login)
	if len(items) == 0 && existing == nil {
		return "", nil
	}
	body := RenderResponseNotice(info.AuthorLogin, items)
	if existing != nil {
		edited, err := client.EditIssueComment(ctx, info.Owner, info.Repo, existing.GetID(), &gh.IssueComment{Body: gh.Ptr(body)})
		if err != nil {
			return "", err
		}
		if u := edited.GetHTMLURL(); u != "" {
			return u, nil
		}
		return existing.GetHTMLURL(), nil
	}
	created, err := client.CreateIssueComment(ctx, info.Owner, info.Repo, info.Number, &gh.IssueComment{Body: gh.Ptr(body)})
	if err != nil {
		return "", err
	}
	return created.GetHTMLURL(), nil
}

func findNoticeComment(comments []*gh.IssueComment, login string) *gh.IssueComment {
	var found *gh.IssueComment
	for _, c := range comments {
		if !strings.Contains(c.GetBody(), noticeMarker) {
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
