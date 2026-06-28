package github

import (
	stdctx "context"
	"strings"
	"testing"
	"time"

	gh "github.com/google/go-github/v84/github"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/diff"
)

type syncRecordClient struct {
	recordClient
	threads []ReviewThread
}

func (c *syncRecordClient) ReviewThreads(stdctx.Context, string, string, int) ([]ReviewThread, error) {
	return c.threads, nil
}

type syncRecordClientWithExistingError struct {
	syncRecordClient
}

func (c *syncRecordClientWithExistingError) ListReviewComments(stdctx.Context, string, string, int, *gh.PullRequestListCommentsOptions) ([]*gh.PullRequestComment, *gh.Response, error) {
	return nil, nil, stdctx.DeadlineExceeded
}

func TestSyncLedgerConversationResolvedMarksOpen(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	entries := []LedgerEntry{{FP: fpStr(1), Path: "a.go", Status: statusOpen, Sev: "low", FirstSev: "low", OpenSHA: "aaaaaa1"}}

	got, delta := SyncLedgerConversationResolved(entries, map[string]bool{fpStr(1): true}, "bbbbbb2", now)
	if delta.Resolved != 1 || delta.Reopened != 0 {
		t.Fatalf("delta = %+v, want one resolved", delta)
	}
	if got[0].Status != statusResolved || got[0].ResKind != resolutionConversation || got[0].ResSHA != "bbbbbb2" {
		t.Fatalf("entry not conversation-resolved: %+v", got[0])
	}
}

func TestSyncLedgerConversationResolvedReopensOnlyConversationRows(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	entries := []LedgerEntry{
		{FP: fpStr(1), Path: "a.go", Status: statusResolved, Sev: "low", FirstSev: "low", OpenSHA: "aaaaaa1", ResSHA: "bbbbbb2", ResKind: resolutionConversation, ResAt: now.Format(time.RFC3339)},
		{FP: fpStr(2), Path: "b.go", Status: statusResolved, Sev: "low", FirstSev: "low", OpenSHA: "aaaaaa1", ResSHA: "bbbbbb2", ResAt: now.Format(time.RFC3339)},
	}

	got, delta := SyncLedgerConversationResolved(entries, nil, "cccccc3", now.Add(time.Hour))
	if delta.Resolved != 0 || delta.Reopened != 1 {
		t.Fatalf("delta = %+v, want one reopen", delta)
	}
	if got[0].Status != statusReopened || got[0].ResKind != "" || got[0].ResSHA != "" || got[0].Reopens != 1 {
		t.Fatalf("conversation row not reopened: %+v", got[0])
	}
	if got[1].Status != statusResolved || got[1].ResKind != "" || got[1].ResSHA != "bbbbbb2" {
		t.Fatalf("commit-resolved row changed unexpectedly: %+v", got[1])
	}
}

func TestReplaceSummaryLedgerBodyPreservesSummarySections(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "bbbbbb2", HTMLBase: "https://github.com/o/r", ReviewCount: 3}
	f := engine.Finding{File: "a.go", Line: 5, Severity: "low", Category: "bug", Title: "thing", QuotedCode: "x"}
	ledger := MergeLedger(nil, []engine.Finding{f}, "aaaaaa1", map[string]bool{"a.go": true}, now)
	body := RenderSummaryFull(info, []engine.Finding{f}, map[string]any{"truncation_level": "full"}, 0, nil, nil, SummaryOptions{
		Diffs:       []diff.Diff{{NewPath: "a.go", Insertions: 1}},
		Walkthrough: "- changed a thing",
		Ledger:      ledger,
		InlineURLs:  map[string]string{Fingerprint(f): "https://github.com/o/r/pull/1#discussion_r1"},
		Published:   true,
		PublishKey:  "0123456789abcdef",
	})
	next := append([]LedgerEntry(nil), ledger...)
	next[0].Status = statusResolved
	next[0].ResSHA = "bbbbbb2"
	next[0].ResKind = resolutionConversation
	next[0].ResAt = now.Add(time.Hour).Format(time.RFC3339)

	out, ok := replaceSummaryLedgerBody(body, info, next, map[string]string{Fingerprint(f): "https://github.com/o/r/pull/1#discussion_r1"})
	if !ok {
		t.Fatal("replaceSummaryLedgerBody returned false")
	}
	for _, want := range []string{"**What changed:**", "changed a thing", "conversation resolved", "<summary>Important Files Changed", "<summary>Review reference", "Last reviewed commit", "miu-cr-published:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("want %q preserved in body:\n%s", want, out)
		}
	}
	if strings.Contains(out, "⚠️ Open") {
		t.Fatalf("open table should be gone after conversation resolve:\n%s", out)
	}
	parsed := ParseLedger(out)
	if len(parsed) != 1 || parsed[0].ResKind != resolutionConversation {
		t.Fatalf("ledger marker not updated: %+v", parsed)
	}
}

func TestReplaceSummaryLedgerBodyAcceptsResultLineWithoutSpace(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "bbbbbb2", HTMLBase: "https://github.com/o/r"}
	f := engine.Finding{File: "a.go", Line: 5, Severity: "low", Category: "bug", Title: "thing", QuotedCode: "x"}
	ledger := MergeLedger(nil, []engine.Finding{f}, "aaaaaa1", map[string]bool{"a.go": true}, now)
	body := RenderSummaryFull(info, []engine.Finding{f}, nil, 0, nil, nil, SummaryOptions{Ledger: ledger})
	body = strings.Replace(body, "**Result:** ", "**Result:**", 1)
	next := append([]LedgerEntry(nil), ledger...)
	next[0].Status = statusResolved
	next[0].ResSHA = "bbbbbb2"
	next[0].ResKind = resolutionConversation
	next[0].ResAt = now.Add(time.Hour).Format(time.RFC3339)

	out, ok := replaceSummaryLedgerBody(body, info, next, nil)
	if !ok {
		t.Fatal("replaceSummaryLedgerBody returned false")
	}
	if !strings.Contains(out, "conversation resolved") {
		t.Fatalf("body missing conversation resolution:\n%s", out)
	}
}

func TestReplaceSummaryLedgerBodyIgnoresInlineResultText(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "bbbbbb2", HTMLBase: "https://github.com/o/r"}
	f := engine.Finding{File: "a.go", Line: 5, Severity: "low", Category: "bug", Title: "thing", QuotedCode: "x"}
	ledger := MergeLedger(nil, []engine.Finding{f}, "aaaaaa1", map[string]bool{"a.go": true}, now)
	body := "model text mentions **Result:** before the summary\n" + RenderSummaryFull(info, []engine.Finding{f}, nil, 0, nil, nil, SummaryOptions{Ledger: ledger})
	next := append([]LedgerEntry(nil), ledger...)
	next[0].Status = statusResolved
	next[0].ResSHA = "bbbbbb2"
	next[0].ResKind = resolutionConversation
	next[0].ResAt = now.Add(time.Hour).Format(time.RFC3339)

	out, ok := replaceSummaryLedgerBody(body, info, next, nil)
	if !ok {
		t.Fatal("replaceSummaryLedgerBody returned false")
	}
	if !strings.Contains(out, "model text mentions **Result:** before the summary") {
		t.Fatalf("untrusted result text was modified:\n%s", out)
	}
	if !strings.Contains(out, "\n**Result:** <sub") || !strings.Contains(out, "conversation resolved") {
		t.Fatalf("summary result line not updated:\n%s", out)
	}
}

func TestSyncSummaryConversationResolvedEditsExistingComment(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "bbbbbb2", HTMLBase: "https://github.com/o/r", ReviewCount: 1}
	f := engine.Finding{File: "a.go", Line: 5, Severity: "low", Category: "bug", Title: "thing", QuotedCode: "x"}
	fp := Fingerprint(f)
	body := RenderSummaryFull(info, []engine.Finding{f}, nil, 0, nil, nil, SummaryOptions{
		Ledger: MergeLedger(nil, []engine.Finding{f}, "aaaaaa1", map[string]bool{"a.go": true}, now),
	})
	client := &syncRecordClient{
		recordClient: recordClient{
			issueStore:     []*gh.IssueComment{{ID: gh.Ptr(int64(7)), Body: gh.Ptr(body)}},
			reviewComments: [][]*gh.PullRequestComment{{{Body: gh.Ptr(fpMarker(fp)), HTMLURL: gh.Ptr("https://github.com/o/r/pull/1#discussion_r1")}}},
		},
		threads: []ReviewThread{{Resolved: true, Comments: []ReviewThreadComment{{Body: fpMarker(fp)}}}},
	}

	res, err := SyncSummaryConversationResolved(stdctx.Background(), client, info, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("SyncSummaryConversationResolved: %v", err)
	}
	if res.Action != UpsertEdited || res.Reason != "updated" || res.Resolved != 1 || client.editedID != 7 {
		t.Fatalf("unexpected result/action: res=%+v editedID=%d", res, client.editedID)
	}
	if !strings.Contains(client.editedBody, "conversation resolved") {
		t.Fatalf("edited body missing conversation marker:\n%s", client.editedBody)
	}
}

func TestSyncSummaryConversationResolvedContinuesWithoutInlineURLs(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "bbbbbb2", HTMLBase: "https://github.com/o/r", ReviewCount: 1}
	f := engine.Finding{File: "a.go", Line: 5, Severity: "low", Category: "bug", Title: "thing", QuotedCode: "x"}
	fp := Fingerprint(f)
	body := RenderSummaryFull(info, []engine.Finding{f}, nil, 0, nil, nil, SummaryOptions{
		Ledger: MergeLedger(nil, []engine.Finding{f}, "aaaaaa1", map[string]bool{"a.go": true}, now),
	})
	client := &syncRecordClientWithExistingError{syncRecordClient: syncRecordClient{
		recordClient: recordClient{issueStore: []*gh.IssueComment{{ID: gh.Ptr(int64(7)), Body: gh.Ptr(body)}}},
		threads:      []ReviewThread{{Resolved: true, Comments: []ReviewThreadComment{{Body: fpMarker(fp)}}}},
	}}

	res, err := SyncSummaryConversationResolved(stdctx.Background(), client, info, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("SyncSummaryConversationResolved: %v", err)
	}
	if res.Action != UpsertEdited || res.Reason != "updated_without_inline_urls" || client.editedID != 7 {
		t.Fatalf("unexpected result/action: res=%+v editedID=%d", res, client.editedID)
	}
	if !strings.Contains(client.editedBody, "conversation resolved") || !strings.Contains(client.editedBody, "a.go:5") {
		t.Fatalf("edited body missing fallback location:\n%s", client.editedBody)
	}
}
