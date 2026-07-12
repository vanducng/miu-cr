package github

import (
	stdctx "context"
	"errors"
	"strings"
	"testing"
	"time"

	gh "github.com/google/go-github/v84/github"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
)

func upsertInfo() *PRInfo {
	return &PRInfo{Owner: "o", Repo: "r", Number: 1, HTMLBase: "https://github.com/o/r"}
}

func TestUpsertSummaryCommentCreatesOnFirstRun(t *testing.T) {
	c := &recordClient{issueStore: []*gh.IssueComment{}}
	act, url, err := UpsertSummaryComment(stdctx.Background(), c, upsertInfo(), ReviewMarker+"\n## Code Review Summary\nbody")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if act != UpsertCreated {
		t.Fatalf("want created, got %q", act)
	}
	if url != "https://github.com/o/r/pull/1#issuecomment-1" {
		t.Fatalf("summary URL = %q", url)
	}
	if len(c.issueStore) != 1 {
		t.Fatalf("want 1 comment, got %d", len(c.issueStore))
	}
	if !strings.HasPrefix(c.issueStore[0].GetBody(), ReviewMarker) {
		t.Fatalf("created body must lead with the marker:\n%s", c.issueStore[0].GetBody())
	}
}

func TestUpsertSummaryCommentEditsNotStacks(t *testing.T) {
	c := &recordClient{issueStore: []*gh.IssueComment{
		{ID: gh.Ptr(int64(7)), Body: gh.Ptr(ReviewMarker + "\nold summary")},
	}}
	c.issueIDSeq = 7
	act, url, err := UpsertSummaryComment(stdctx.Background(), c, upsertInfo(), ReviewMarker+"\nnew summary")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if act != UpsertEdited {
		t.Fatalf("want edited, got %q", act)
	}
	if url != "https://github.com/o/r/pull/1#issuecomment-7" {
		t.Fatalf("summary URL = %q", url)
	}
	if len(c.issueStore) != 1 {
		t.Fatalf("must not stack: want 1 comment, got %d", len(c.issueStore))
	}
	if got := c.issueStore[0].GetBody(); !strings.Contains(got, "new summary") {
		t.Fatalf("body must be replaced in place, got:\n%s", got)
	}
	if c.editedID != 7 {
		t.Fatalf("want edit of id 7, got %d", c.editedID)
	}
}

func TestUpsertSummaryCommentEditsLowestID(t *testing.T) {
	c := &recordClient{issueStore: []*gh.IssueComment{
		{ID: gh.Ptr(int64(9)), Body: gh.Ptr(ReviewMarker + "\ndup nine")},
		{ID: gh.Ptr(int64(5)), Body: gh.Ptr(ReviewMarker + "\ndup five")},
	}}
	c.issueIDSeq = 9
	act, _, err := UpsertSummaryComment(stdctx.Background(), c, upsertInfo(), ReviewMarker+"\nedited")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if act != UpsertEdited {
		t.Fatalf("want edited, got %q", act)
	}
	if c.editedID != 5 {
		t.Fatalf("want lowest-id (5) edited, got %d", c.editedID)
	}
	// id 9 left untouched (the next run still reconverges to id 5).
	for _, ic := range c.issueStore {
		if ic.GetID() == 9 && !strings.Contains(ic.GetBody(), "dup nine") {
			t.Fatalf("higher-id duplicate must be left untouched, got:\n%s", ic.GetBody())
		}
	}
}

func TestUpsertSummaryCommentIgnoresUnmarked(t *testing.T) {
	c := &recordClient{issueStore: []*gh.IssueComment{
		{ID: gh.Ptr(int64(3)), Body: gh.Ptr("a human comment, no marker")},
	}}
	c.issueIDSeq = 3
	act, _, err := UpsertSummaryComment(stdctx.Background(), c, upsertInfo(), ReviewMarker+"\nsummary")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if act != UpsertCreated {
		t.Fatalf("must not hijack a human comment; want created, got %q", act)
	}
	if c.editN != 0 {
		t.Fatalf("must not edit the unmarked human comment, edits=%d", c.editN)
	}
	if len(c.issueStore) != 2 {
		t.Fatalf("want human comment + new summary = 2, got %d", len(c.issueStore))
	}
}

func TestUpsertSummaryCommentEmptyBodyIsNoop(t *testing.T) {
	c := &recordClient{issueStore: []*gh.IssueComment{}}
	act, _, err := UpsertSummaryComment(stdctx.Background(), c, upsertInfo(), "   \n  ")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if act != UpsertNone {
		t.Fatalf("want none for empty body, got %q", act)
	}
	if c.createIssueN != 0 || c.editN != 0 {
		t.Fatalf("empty body must touch nothing, create=%d edit=%d", c.createIssueN, c.editN)
	}
}

func TestCreateSummaryCommentIfMissingCreatesOnFirstRun(t *testing.T) {
	c := &recordClient{issueStore: []*gh.IssueComment{}}
	act, url, err := CreateSummaryCommentIfMissing(stdctx.Background(), c, upsertInfo(), ReviewMarker+"\n## Code Review Summary\nrunning")
	if err != nil {
		t.Fatalf("create missing: %v", err)
	}
	if act != UpsertCreated {
		t.Fatalf("want created, got %q", act)
	}
	if url != "https://github.com/o/r/pull/1#issuecomment-1" {
		t.Fatalf("summary URL = %q", url)
	}
	if c.createIssueN != 1 || c.editN != 0 {
		t.Fatalf("first placeholder must create only: create=%d edit=%d", c.createIssueN, c.editN)
	}
	if got := c.issueStore[0].GetBody(); !strings.Contains(got, "running") {
		t.Fatalf("created body = %q", got)
	}
}

func TestCreateSummaryCommentIfMissingKeepsExistingSummary(t *testing.T) {
	c := &recordClient{issueStore: []*gh.IssueComment{{
		ID:      gh.Ptr(int64(8)),
		HTMLURL: gh.Ptr("https://github.com/o/r/pull/1#issuecomment-8"),
		Body:    gh.Ptr(ReviewMarker + "\n## Code Review Summary\n**Result:** Review passed!"),
	}}}
	act, url, err := CreateSummaryCommentIfMissing(stdctx.Background(), c, upsertInfo(), ReviewMarker+"\n## Code Review Summary\n**Result:** Review running.")
	if err != nil {
		t.Fatalf("create missing: %v", err)
	}
	if act != UpsertNone {
		t.Fatalf("want none for existing summary, got %q", act)
	}
	if url != "https://github.com/o/r/pull/1#issuecomment-8" {
		t.Fatalf("summary URL = %q", url)
	}
	if c.createIssueN != 0 || c.editN != 0 {
		t.Fatalf("existing summary must not be touched: create=%d edit=%d", c.createIssueN, c.editN)
	}
	if got := c.issueStore[0].GetBody(); !strings.Contains(got, "Review passed") || strings.Contains(got, "Review running") {
		t.Fatalf("existing body was overwritten:\n%s", got)
	}
}

func TestUpsertSummaryStatusEditsExistingSummary(t *testing.T) {
	info := upsertInfo()
	info.HeadSHA = "abcdef123456"
	c := &recordClient{issueStore: []*gh.IssueComment{{
		ID:      gh.Ptr(int64(8)),
		HTMLURL: gh.Ptr("https://github.com/o/r/pull/1#issuecomment-8"),
		Body:    gh.Ptr(ReviewMarker + "\n" + runsCountToken(1) + "\n## Code Review Summary\n\n**Result:** Review passed!"),
	}}}
	c.issueIDSeq = 8
	act, url, err := UpsertSummaryStatus(stdctx.Background(), c, info, RenderReviewingSummaryStatus(info), RenderRunningSummary(info, ""))
	if err != nil {
		t.Fatalf("upsert status: %v", err)
	}
	if act != UpsertEdited {
		t.Fatalf("want edited, got %q", act)
	}
	if url != "https://github.com/o/r/pull/1#issuecomment-8" {
		t.Fatalf("summary URL = %q", url)
	}
	body := c.issueStore[0].GetBody()
	if !strings.Contains(body, "Reviewing commit") || !strings.Contains(body, "Review passed") {
		t.Fatalf("status overlay should keep prior result:\n%s", body)
	}
}

func TestUpsertSummaryStatusCreatesFallbackOnFirstRun(t *testing.T) {
	info := upsertInfo()
	info.HeadSHA = "abcdef123456"
	c := &recordClient{issueStore: []*gh.IssueComment{}}
	availableAt := time.Date(2026, 7, 12, 1, 2, 0, 0, time.UTC)
	act, _, err := UpsertSummaryStatus(stdctx.Background(), c, info, RenderQueuedSummaryStatus(info, availableAt, time.Minute), RenderQueuedSummary(info, availableAt, time.Minute, ""))
	if err != nil {
		t.Fatalf("upsert status: %v", err)
	}
	if act != UpsertCreated {
		t.Fatalf("want created, got %q", act)
	}
	if len(c.issueStore) != 1 {
		t.Fatalf("summary comments = %d, want 1", len(c.issueStore))
	}
	body := c.issueStore[0].GetBody()
	if !strings.Contains(body, "**Result:** Review queued") || !strings.Contains(body, "2026-07-12 01:02 UTC") {
		t.Fatalf("fallback summary should carry queued state:\n%s", body)
	}
	if strings.Contains(body, "Previous result remains visible below") {
		t.Fatalf("first-run fallback should not imply a previous result:\n%s", body)
	}
}

func TestUpsertSummaryStatusNoopsWhenUnchanged(t *testing.T) {
	info := upsertInfo()
	info.HeadSHA = "abcdef123456"
	body := withSummaryStatus(ReviewMarker+"\n## Code Review Summary\n\n**Result:** Review passed!", RenderReviewingSummaryStatus(info))
	c := &recordClient{issueStore: []*gh.IssueComment{{
		ID:   gh.Ptr(int64(8)),
		Body: gh.Ptr(body),
	}}}
	c.issueIDSeq = 8
	act, _, err := UpsertSummaryStatus(stdctx.Background(), c, info, RenderReviewingSummaryStatus(info), RenderRunningSummary(info, ""))
	if err != nil {
		t.Fatalf("upsert status: %v", err)
	}
	if act != UpsertNone {
		t.Fatalf("want none, got %q", act)
	}
	if c.editN != 0 {
		t.Fatalf("unchanged status should not edit, edits=%d", c.editN)
	}
}

func TestUpsertSummaryCommentForkFallbackOnCreate(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	c := &recordClient{issueStore: []*gh.IssueComment{}, createIssueErr: forbidden403()}
	act, _, err := UpsertSummaryComment(stdctx.Background(), c, upsertInfo(), ReviewMarker+"\nsummary")
	if err != nil {
		t.Fatalf("fork 403 must not hard-fail: %v", err)
	}
	if act != UpsertForkFallback {
		t.Fatalf("want fork_fallback, got %q", act)
	}
}

func TestUpsertSummaryCommentForkFallbackOnEdit(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	c := &recordClient{
		issueStore: []*gh.IssueComment{{ID: gh.Ptr(int64(2)), Body: gh.Ptr(ReviewMarker + "\nold")}},
		editErr:    forbidden403(),
	}
	c.issueIDSeq = 2
	act, _, err := UpsertSummaryComment(stdctx.Background(), c, upsertInfo(), ReviewMarker+"\nnew")
	if err != nil {
		t.Fatalf("fork 403 on edit must not hard-fail: %v", err)
	}
	if act != UpsertForkFallback {
		t.Fatalf("want fork_fallback, got %q", act)
	}
}

func TestUpsertSummaryCommentTypedErrorOnOtherFailure(t *testing.T) {
	c := &recordClient{issueStore: []*gh.IssueComment{}, createIssueErr: errors.New("boom 500")}
	act, _, err := UpsertSummaryComment(stdctx.Background(), c, upsertInfo(), ReviewMarker+"\nsummary")
	if err == nil {
		t.Fatal("a non-403 API error must surface")
	}
	if act != UpsertNone {
		t.Fatalf("want none on error, got %q", act)
	}
	var ce *clierr.CLIError
	if !errors.As(err, &ce) {
		t.Fatalf("want a typed cli.CLIError, got %T", err)
	}
	if ce.Code != "github.upsert_summary_failed" {
		t.Fatalf("want stable code github.upsert_summary_failed, got %q", ce.Code)
	}
}

// storeless N round-trip: render body with runs token N=1, create, re-read parses 1;
// bump to 2, re-upsert (edit), stored body parses 2 - all with NO store.
func TestUpsertSummaryCommentStorelessRunsRoundTrip(t *testing.T) {
	c := &recordClient{issueStore: []*gh.IssueComment{}}
	info := upsertInfo()

	body1 := runsCountToken(1) + "\n" + ReviewMarker + "\n## Code Review Summary"
	if act, _, err := UpsertSummaryComment(stdctx.Background(), c, info, body1); err != nil || act != UpsertCreated {
		t.Fatalf("first run: act=%q err=%v", act, err)
	}
	if got := parseRunsCount(c.issueStore[0].GetBody()); got != 1 {
		t.Fatalf("stored body must parse runs=1, got %d", got)
	}

	body2 := runsCountToken(2) + "\n" + ReviewMarker + "\n## Code Review Summary"
	if act, _, err := UpsertSummaryComment(stdctx.Background(), c, info, body2); err != nil || act != UpsertEdited {
		t.Fatalf("second run: act=%q err=%v", act, err)
	}
	if len(c.issueStore) != 1 {
		t.Fatalf("re-run must edit, not stack: got %d comments", len(c.issueStore))
	}
	if got := parseRunsCount(c.issueStore[0].GetBody()); got != 2 {
		t.Fatalf("edited body must parse runs=2, got %d", got)
	}
}
