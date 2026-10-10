package github

import (
	stdctx "context"
	"strings"
	"testing"
	"time"

	gh "github.com/google/go-github/v84/github"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine"
)

func TestParseThreadReply(t *testing.T) {
	cases := []struct {
		body   string
		intent ReplyIntent
		sha    string
	}{
		{"Fixed in abcdef1: tightened the bound", ReplyFix, "abcdef1"},
		{"fixed in ABCDEF1234567 - done", ReplyFix, "abcdef1234567"},
		{"Deferred: tracked in #42 because the change is safe to ship", ReplyDefer, ""},
		{"NA: this helper is not on the request path for this pull request", ReplyNotApplicable, ""},
		{"Not applicable: the function is unused by this pull request's callers", ReplyNotApplicable, ""},
		{"thanks, looking", "", ""},
		{"<!-- miu-cr-bot -->\nFixed in abcdef1: no", "", ""},
		{"Fixed in abcdef1: done\n> <!-- miu-cr-bot -->", ReplyFix, "abcdef1"},
	}
	for _, tc := range cases {
		got := ParseThreadReply(tc.body)
		if got.Intent != tc.intent || got.SHA != tc.sha {
			t.Errorf("%q: got %+v, want intent %q sha %q", tc.body, got, tc.intent, tc.sha)
		}
	}
}

func TestIsBotBodyOnlyAtPrefix(t *testing.T) {
	if IsBotBody("Fixed in abcdef1: done\n<!-- miu-cr-bot -->") {
		t.Fatal("a quoted marker later in the body must still be a human reply")
	}
	if !IsBotBody("<!-- miu-cr-reply:9 -->\nresolved") {
		t.Fatal("reply marker prefix must be treated as the bot")
	}
}

type replyClient struct {
	recordClient
	shas     []string
	patches  map[string]string
	replies  []string
	resolved []string
	threads  []ReviewThread
	judgeN   int
}

func (c *replyClient) PullRequestCommitSHAs(stdctx.Context, string, string, int) ([]string, error) {
	return c.shas, nil
}

func (c *replyClient) CommitFilePatch(_ stdctx.Context, _, _, _, path string) (bool, string, error) {
	patch, ok := c.patches[path]
	return ok, patch, nil
}

func (c *replyClient) ReplyToReviewComment(_ stdctx.Context, _, _ string, _ int, _ int64, body string) (string, error) {
	c.replies = append(c.replies, body)
	return "https://github.com/acme/app/pull/1#discussion_r99", nil
}

func (c *replyClient) ResolveReviewThread(_ stdctx.Context, threadID string) error {
	c.resolved = append(c.resolved, threadID)
	return nil
}

func (c *replyClient) ReviewThreads(stdctx.Context, string, string, int) ([]ReviewThread, error) {
	return c.threads, nil
}

func (c *replyClient) EditReviewComment(stdctx.Context, string, string, int64, string) error {
	return nil
}

type fakeJudge struct {
	n       int
	accept  bool
	explain string
	err     error
}

func (j *fakeJudge) JudgeThreadReply(stdctx.Context, ThreadReplyJudgeInput) (ThreadReplyVerdict, error) {
	j.n++
	if j.err != nil {
		return ThreadReplyVerdict{}, j.err
	}
	return ThreadReplyVerdict{Accept: j.accept, Explanation: j.explain}, nil
}

func replyFixture(t *testing.T, head string) (*PRInfo, engine.Finding, string, *replyClient) {
	t.Helper()
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	info := &PRInfo{Owner: "acme", Repo: "app", Number: 1, HeadSHA: head, HTMLBase: "https://github.com/acme/app", ReviewCount: 1, AuthorAssociation: "MEMBER", AuthorLogin: "dev"}
	f := engine.Finding{File: "a.go", Line: 5, Severity: "low", Category: "bug", Title: "bounds", QuotedCode: "i <= n"}
	fp := Fingerprint(f)
	body := RenderSummaryFull(info, []engine.Finding{f}, nil, 0, nil, nil, SummaryOptions{
		Ledger:    MergeLedger(nil, []engine.Finding{f}, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", map[string]bool{"a.go": true}, now),
		Published: true,
	})
	root := &gh.PullRequestComment{
		ID: gh.Ptr(int64(10)), User: &gh.User{Login: gh.Ptr("reviewer")},
		Body: gh.Ptr("finding\n\n" + threadReplyHint + "\n\n" + fpMarker(fp) + "\n" + botMarker),
	}
	client := &replyClient{
		recordClient: recordClient{
			login:   "reviewer",
			headSHA: head,
			issueStore: []*gh.IssueComment{{
				ID: gh.Ptr(int64(7)), User: &gh.User{Login: gh.Ptr("reviewer")}, Body: gh.Ptr(body),
			}},
			reviewComments: [][]*gh.PullRequestComment{{root}},
		},
		shas: []string{"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		patches: map[string]string{
			"a.go": "@@ -1,6 +1,6 @@\n ctx\n ctx\n ctx\n ctx\n-old\n+new\n",
		},
		threads: []ReviewThread{{ID: "THR_1", Comments: []ReviewThreadComment{{ID: 10, Body: root.GetBody()}}}},
	}
	return info, f, fp, client
}

func TestApplyThreadReplyRejectsMissingCommitWithoutJudge(t *testing.T) {
	head := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	info, _, _, client := replyFixture(t, head)
	judge := &fakeJudge{accept: true, explain: "should not run"}
	res, err := ApplyThreadReply(stdctx.Background(), client, info, ThreadReplyRequest{
		CommentID: 11, Body: "Fixed in deadbee: changed the bound", UserLogin: "dev", InReplyTo: 10, Kind: "review_comment",
	}, judge, config.ApprovalPolicy{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if judge.n != 0 {
		t.Fatalf("judge calls = %d, want 0", judge.n)
	}
	if res.Accepted != 0 || res.Rejected != 1 {
		t.Fatalf("result = %+v", res)
	}
	if len(client.replies) != 1 || !strings.Contains(client.replies[0], "Still open") {
		t.Fatalf("reply = %v", client.replies)
	}
}

func TestApplyThreadReplyAcceptsFixAndResolves(t *testing.T) {
	head := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	info, _, fp, client := replyFixture(t, head)
	client.shas = []string{"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	judge := &fakeJudge{accept: true, explain: "the bound is fixed"}
	res, err := ApplyThreadReply(stdctx.Background(), client, info, ThreadReplyRequest{
		CommentID: 11, Body: "Fixed in bbbbbbb: tightened the loop", UserLogin: "dev", InReplyTo: 10, Kind: "review_comment",
	}, judge, config.ApprovalPolicy{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if judge.n != 1 || res.Accepted != 1 || res.Rejected != 0 {
		t.Fatalf("judge=%d result=%+v", judge.n, res)
	}
	ledger := ParseLedger(client.editedBody)
	if len(ledger) != 1 || ledger[0].Status != statusResolved || ledger[0].FP != fp || ledger[0].ResSHA != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("ledger = %+v", ledger)
	}
	if len(client.resolved) != 1 || client.resolved[0] != "THR_1" {
		t.Fatalf("resolved = %v", client.resolved)
	}
	if !strings.Contains(client.replies[0], "<!-- miu-cr-reply:11 -->") {
		t.Fatalf("reply missing idempotency marker: %s", client.replies[0])
	}
}

func TestApplyThreadReplyAcceptsDeferralAndApproves(t *testing.T) {
	head := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	info, _, _, client := replyFixture(t, head)
	res, err := ApplyThreadReply(stdctx.Background(), client, info, ThreadReplyRequest{
		CommentID: 11,
		Body:      "Deferred: tracked in #42 because this helper is unused on the request path and safe to ship later",
		UserLogin: "dev", InReplyTo: 10, Kind: "review_comment",
	}, nil, config.ApprovalPolicy{Mode: "clean"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Approved || res.Accepted != 1 {
		t.Fatalf("result = %+v", res)
	}
	ledger := ParseLedger(client.editedBody)
	if len(ledger) != 1 || ledger[0].Status != statusDeferred || ledger[0].ResKind != resolutionDeferral {
		t.Fatalf("ledger = %+v", ledger)
	}
	if !strings.Contains(client.editedBody, "**⏸️ Deferred (1)**") {
		t.Fatalf("summary missing deferred table:\n%s", client.editedBody)
	}
	if client.createReviewN != 1 || client.gotReview.GetEvent() != "APPROVE" {
		t.Fatalf("approve n=%d event=%q", client.createReviewN, client.gotReview.GetEvent())
	}
}

func TestApplyThreadReplyIgnoresBotAndDuplicate(t *testing.T) {
	head := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	info, _, _, client := replyFixture(t, head)
	bot, err := ApplyThreadReply(stdctx.Background(), client, info, ThreadReplyRequest{
		CommentID: 12, Body: "<!-- miu-cr-bot -->\nnoted", UserLogin: "reviewer", Kind: "review_comment",
	}, nil, config.ApprovalPolicy{}, time.Now())
	if err != nil || bot.Reason != "bot_comment" {
		t.Fatalf("bot = %+v err=%v", bot, err)
	}
	client.reviewComments[0] = append(client.reviewComments[0], &gh.PullRequestComment{
		ID: gh.Ptr(int64(13)), Body: gh.Ptr("<!-- miu-cr-reply:11 -->\nok"), User: &gh.User{Login: gh.Ptr("reviewer")},
	})
	dup, err := ApplyThreadReply(stdctx.Background(), client, info, ThreadReplyRequest{
		CommentID: 11, Body: "Deferred: tracked in #42 because this is already answered", UserLogin: "dev", InReplyTo: 10, Kind: "review_comment",
	}, nil, config.ApprovalPolicy{}, time.Now())
	if err != nil || dup.Reason != "duplicate" {
		t.Fatalf("dup = %+v err=%v", dup, err)
	}
	if len(client.replies) != 0 {
		t.Fatalf("posted %d replies", len(client.replies))
	}
}

func TestListActionableRepliesFiltersAndCaps(t *testing.T) {
	info := &PRInfo{Owner: "acme", Repo: "app", Number: 1, AuthorLogin: "dev"}
	root := &gh.PullRequestComment{
		ID: gh.Ptr(int64(1)), User: &gh.User{Login: gh.Ptr("reviewer")},
		Body: gh.Ptr("<!-- miucr:fp=aaaaaaaaaaaaaaaa -->\n" + botMarker),
	}
	comments := []*gh.PullRequestComment{root}
	comments = append(comments, &gh.PullRequestComment{
		ID: gh.Ptr(int64(2)), InReplyTo: gh.Ptr(int64(1)), User: &gh.User{Login: gh.Ptr("reviewer")},
		Body: gh.Ptr("Deferred: tracked in #42 because this helper is unused and safe to ship later"),
	})
	comments = append(comments, &gh.PullRequestComment{
		ID: gh.Ptr(int64(3)), InReplyTo: gh.Ptr(int64(1)), User: &gh.User{Login: gh.Ptr("stranger")}, AuthorAssociation: gh.Ptr("NONE"),
		Body: gh.Ptr("Deferred: tracked in #42 because this helper is unused and safe to ship later"),
	})
	comments = append(comments, &gh.PullRequestComment{
		ID: gh.Ptr(int64(4)), InReplyTo: gh.Ptr(int64(1)), User: &gh.User{Login: gh.Ptr("dev")},
		Body: gh.Ptr("thanks"),
	})
	for i := int64(5); i <= 13; i++ {
		comments = append(comments, &gh.PullRequestComment{
			ID: gh.Ptr(i), InReplyTo: gh.Ptr(int64(1)), User: &gh.User{Login: gh.Ptr("dev")},
			Body: gh.Ptr("Deferred: tracked in #42 because this helper is unused and safe to ship later"),
		})
	}
	comments = append(comments, &gh.PullRequestComment{
		ID: gh.Ptr(int64(99)), User: &gh.User{Login: gh.Ptr("reviewer")},
		Body: gh.Ptr("<!-- miu-cr-reply:5 -->\nok"),
	})
	client := &replyClient{recordClient: recordClient{login: "reviewer", reviewComments: [][]*gh.PullRequestComment{comments}}}
	got, err := ListActionableReplies(stdctx.Background(), client, info)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 8 {
		t.Fatalf("got %d replies, want 8: %+v", len(got), got)
	}
	if got[0].CommentID == 2 || got[0].CommentID == 3 || got[0].CommentID == 4 || got[0].CommentID == 5 {
		t.Fatalf("first queued comment = %d", got[0].CommentID)
	}
}

func TestApplyThreadReplyMarksUnparseableVerdict(t *testing.T) {
	head := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	info, _, _, client := replyFixture(t, head)
	judge := &fakeJudge{err: stdctx.DeadlineExceeded}
	res, err := ApplyThreadReply(stdctx.Background(), client, info, ThreadReplyRequest{
		CommentID: 11, Body: "Deferred: tracked in #42 because this helper is unused and safe to ship later",
		UserLogin: "dev", InReplyTo: 10, Kind: "review_comment",
	}, judge, config.ApprovalPolicy{}, time.Now())
	if err == nil || res.Reason != "judge_failed" || len(client.replies) != 0 {
		t.Fatalf("transient judge error = %+v err=%v replies=%d", res, err, len(client.replies))
	}
	judge.err = errString("agent: reply verdict is not JSON")
	res, err = ApplyThreadReply(stdctx.Background(), client, info, ThreadReplyRequest{
		CommentID: 11, Body: "Deferred: tracked in #42 because this helper is unused and safe to ship later",
		UserLogin: "dev", InReplyTo: 10, Kind: "review_comment",
	}, judge, config.ApprovalPolicy{}, time.Now())
	if err != nil || res.Reason != "judge_unparseable" || len(client.replies) != 1 || !strings.Contains(client.replies[0], "<!-- miu-cr-reply:11 -->") {
		t.Fatalf("unparseable = %+v err=%v replies=%v", res, err, client.replies)
	}

	info, _, _, client = replyFixture(t, head)
	judge = &fakeJudge{err: errString("agent: reply verdict: invalid character")}
	res, err = ApplyThreadReply(stdctx.Background(), client, info, ThreadReplyRequest{
		CommentID: 11, Body: "Deferred: tracked in #42 because this helper is unused and safe to ship later",
		UserLogin: "dev", InReplyTo: 10, Kind: "review_comment",
	}, judge, config.ApprovalPolicy{}, time.Now())
	if err != nil || res.Reason != "judge_unparseable" || len(client.replies) != 1 {
		t.Fatalf("braced verdict failure = %+v err=%v replies=%v", res, err, client.replies)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestApplyThreadReplyMarksAlreadyHandled(t *testing.T) {
	head := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	info, _, _, client := replyFixture(t, head)
	first, err := ApplyThreadReply(stdctx.Background(), client, info, ThreadReplyRequest{
		CommentID: 11,
		Body:      "Deferred: tracked in #42 because this helper is unused on the request path and safe to ship later",
		UserLogin: "dev", InReplyTo: 10, Kind: "review_comment",
	}, nil, config.ApprovalPolicy{}, time.Now())
	if err != nil || first.Accepted != 1 {
		t.Fatalf("first = %+v err=%v", first, err)
	}
	second, err := ApplyThreadReply(stdctx.Background(), client, info, ThreadReplyRequest{
		CommentID: 12,
		Body:      "Deferred: tracked in #42 because this helper is unused on the request path and safe to ship later",
		UserLogin: "dev", InReplyTo: 10, Kind: "review_comment",
	}, nil, config.ApprovalPolicy{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if second.Reason != "already_handled" || len(client.replies) != 2 || !strings.Contains(client.replies[1], "<!-- miu-cr-reply:12 -->") || !strings.Contains(client.replies[1], "already handled") {
		t.Fatalf("second=%+v replies=%v", second, client.replies)
	}
}

func TestApplyThreadReplyIgnoresUntrustedCommenter(t *testing.T) {
	head := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	info, _, _, client := replyFixture(t, head)
	judge := &fakeJudge{accept: true, explain: "should not run"}
	res, err := ApplyThreadReply(stdctx.Background(), client, info, ThreadReplyRequest{
		CommentID: 11, Body: "Deferred: tracked in #42 because this helper is unused and safe to ship later",
		UserLogin: "stranger", AuthorAssociation: "NONE", InReplyTo: 10, Kind: "review_comment",
	}, judge, config.ApprovalPolicy{Mode: "clean"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Reason != "untrusted_comment" || judge.n != 0 || len(client.replies) != 0 || res.Approved {
		t.Fatalf("result=%+v judge=%d replies=%d", res, judge.n, len(client.replies))
	}
}

func TestApplyThreadReplyKeepsSharedThreadOpen(t *testing.T) {
	head := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	info := &PRInfo{Owner: "acme", Repo: "app", Number: 1, HeadSHA: head, HTMLBase: "https://github.com/acme/app", ReviewCount: 1, AuthorAssociation: "MEMBER", AuthorLogin: "dev"}
	first := engine.Finding{File: "a.go", Line: 5, Severity: "low", Category: "bug", Title: "bounds", QuotedCode: "i <= n"}
	second := engine.Finding{File: "b.go", Line: 9, Severity: "low", Category: "bug", Title: "other", QuotedCode: "return x"}
	findings := []engine.Finding{first, second}
	body := RenderSummaryFull(info, findings, nil, 0, nil, nil, SummaryOptions{
		Ledger:    MergeLedger(nil, findings, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", map[string]bool{"a.go": true, "b.go": true}, now),
		Published: true,
	})
	rootBody := "shared\n\n" + fpMarker(Fingerprint(first)) + "\n" + fpMarker(Fingerprint(second)) + "\n" + botMarker
	root := &gh.PullRequestComment{ID: gh.Ptr(int64(10)), User: &gh.User{Login: gh.Ptr("reviewer")}, Body: gh.Ptr(rootBody)}
	client := &replyClient{
		recordClient: recordClient{
			login:   "reviewer",
			headSHA: head,
			issueStore: []*gh.IssueComment{{
				ID: gh.Ptr(int64(7)), User: &gh.User{Login: gh.Ptr("reviewer")}, Body: gh.Ptr(body),
			}},
			reviewComments: [][]*gh.PullRequestComment{{root}},
		},
		threads: []ReviewThread{{ID: "THR_SHARED", Comments: []ReviewThreadComment{{ID: 10, Body: rootBody}}}},
	}
	res, err := ApplyThreadReply(stdctx.Background(), client, info, ThreadReplyRequest{
		CommentID: 11,
		Body:      "Deferred: a.go:5 tracked in #42 because this helper is unused on the request path and safe to ship later",
		UserLogin: "dev", InReplyTo: 10, Kind: "review_comment",
	}, nil, config.ApprovalPolicy{Mode: "clean"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 1 || res.Approved || len(client.resolved) != 0 {
		t.Fatalf("result=%+v resolved=%v", res, client.resolved)
	}
	ledger := ParseLedger(client.editedBody)
	open := 0
	for _, e := range ledger {
		if e.Status == statusOpen {
			open++
		}
	}
	if open != 1 {
		t.Fatalf("want one finding still open, ledger=%+v", ledger)
	}
}

func TestSyncLedgerConversationResolvedKeepsDeferred(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	entries := []LedgerEntry{{
		FP: fpStr(1), Path: "a.go", Status: statusDeferred, Sev: "low", FirstSev: "low",
		OpenSHA: "aaaaaa1", ResKind: resolutionDeferral, Note: "tracked in #42",
	}}
	got, delta := SyncLedgerConversationResolved(entries, map[string]bool{fpStr(1): true}, now)
	if delta.Resolved != 0 || got[0].Status != statusDeferred || got[0].ResKind != resolutionDeferral {
		t.Fatalf("deferred row changed: delta=%+v entry=%+v", delta, got[0])
	}
}

func TestMergeLedgerKeepsDeferredWhenReportedAgain(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	f := engine.Finding{File: "a.go", Line: 5, Severity: "low", Category: "bug", Title: "bounds", QuotedCode: "i <= n"}
	prior := MergeLedger(nil, []engine.Finding{f}, "aaaaaa1", map[string]bool{"a.go": true}, now)
	prior[0].Status = statusDeferred
	prior[0].ResKind = resolutionDeferral
	got := MergeLedger(prior, []engine.Finding{f}, "bbbbbb2", map[string]bool{"a.go": true}, now.Add(time.Hour))
	if got[0].Status != statusDeferred {
		t.Fatalf("status = %s, want deferred", got[0].Status)
	}
}

func TestLedgerClearForApprovalAllowsDeferrals(t *testing.T) {
	entries := []LedgerEntry{{Status: statusDeferred}, {Status: statusIrrelevant}, {Status: statusResolved}}
	if !LedgerClearForApproval(entries) {
		t.Fatal("accepted deferrals and irrelevant rows should not block")
	}
	if LedgerClearForApproval([]LedgerEntry{{Status: statusOpen}}) {
		t.Fatal("open finding must block")
	}
}

func TestReplyNamesTargetRequiresBoundary(t *testing.T) {
	if replyNamesTarget("please check a.go:51", "", "a.go", 5) {
		t.Fatal("a.go:51 must not match a.go:5")
	}
	if !replyNamesTarget("please check a.go:51", "", "a.go", 51) {
		t.Fatal("a.go:51 should match")
	}
	if !replyNamesTarget("fixed a.go:5.", "", "a.go", 5) {
		t.Fatal("a.go:5 at a boundary should match")
	}
}

func TestNoSummaryWaitsInsteadOfLooping(t *testing.T) {
	info := &PRInfo{Owner: "acme", Repo: "app", Number: 1, HeadSHA: strings.Repeat("a", 40), AuthorLogin: "dev"}
	client := &replyClient{recordClient: recordClient{login: "reviewer", issueStore: []*gh.IssueComment{}}}
	res, err := ApplyThreadReply(stdctx.Background(), client, info, ThreadReplyRequest{
		CommentID: 3, Body: "Deferred: tracked in #9 because this is safe to merge later",
		UserLogin: "dev", AuthorAssociation: "NONE",
	}, nil, config.ApprovalPolicy{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Reason != "no_summary" || res.CoolDown != replyNotReadyWait {
		t.Fatalf("result = %+v", res)
	}
	if len(client.replies) != 0 {
		t.Fatal("a missing summary must not post a marker")
	}
}
