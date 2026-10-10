package github

import (
	stdctx "context"
	"strings"
	"testing"

	gh "github.com/google/go-github/v84/github"
)

func TestRenderResponseNoticeMentionsAuthor(t *testing.T) {
	body := RenderResponseNotice("dev", []NoticeItem{{Path: "a.go", Line: 4, Title: "bounds", URL: "https://github.com/acme/app/pull/1#discussion_r1"}})
	if !strings.HasPrefix(body, noticeMarker) || !strings.Contains(body, "@dev ") {
		t.Fatalf("notice = %s", body)
	}
	if strings.Contains(RenderResponseNotice("bad login", []NoticeItem{{Path: "a.go", Line: 1}}), "@") {
		t.Fatal("invalid login must not be mentioned")
	}
	clear := RenderResponseNotice("dev", nil)
	if strings.Contains(clear, "@") || !strings.Contains(clear, "No open findings") {
		t.Fatalf("clear notice = %s", clear)
	}
}

func TestUpsertResponseNoticeEditsInPlace(t *testing.T) {
	info := &PRInfo{Owner: "acme", Repo: "app", Number: 1, AuthorLogin: "dev"}
	client := &recordClient{login: "reviewer", issueStore: []*gh.IssueComment{{
		ID: gh.Ptr(int64(3)), User: &gh.User{Login: gh.Ptr("reviewer")},
		Body: gh.Ptr(noticeMarker + "\n" + botMarker + "\n\nold"),
	}}}
	if _, err := UpsertResponseNotice(stdctx.Background(), client, info, "reviewer", []NoticeItem{{Path: "a.go", Line: 2, Title: "bounds"}}); err != nil {
		t.Fatal(err)
	}
	if client.createIssueN != 0 || client.editN != 1 {
		t.Fatalf("create=%d edit=%d", client.createIssueN, client.editN)
	}
	if !strings.Contains(client.editedBody, "@dev ") || !strings.Contains(client.editedBody, noticeMarker) {
		t.Fatalf("edited = %s", client.editedBody)
	}
	if _, err := UpsertResponseNotice(stdctx.Background(), client, info, "reviewer", nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(client.editedBody, "@") {
		t.Fatalf("cleared notice still mentions author:\n%s", client.editedBody)
	}
}

func TestUpsertResponseNoticeSkipsCleanCreate(t *testing.T) {
	info := &PRInfo{Owner: "acme", Repo: "app", Number: 1}
	client := &recordClient{login: "reviewer"}
	if _, err := UpsertResponseNotice(stdctx.Background(), client, info, "reviewer", nil); err != nil {
		t.Fatal(err)
	}
	if client.createIssueN != 0 {
		t.Fatal("created a notice with nothing open")
	}
}

func TestDeferredResultLine(t *testing.T) {
	line := ledgerResultLine([]LedgerEntry{{Status: statusDeferred}, {Status: statusResolved}}, 1, "abc", reviewChangeSize{})
	if !strings.HasPrefix(line, "Review passed! 1 finding deferred.") {
		t.Fatalf("line = %s", line)
	}
}
