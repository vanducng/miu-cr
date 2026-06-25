package github

import (
	stdctx "context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	gh "github.com/google/go-github/v84/github"
)

// convClient is a Client fake whose conversation list ops return canned synthetic
// data (or an error). It embeds the shared fakeClient for the unused write/other
// read methods, overriding only the three conversation reads.
type convClient struct {
	fakeClient
	reviews       []*gh.PullRequestReview
	reviewComment []*gh.PullRequestComment
	issueComments []*gh.IssueComment
	reviewsErr    error
	reviewCmtErr  error
	issueCmtErr   error
}

func (c *convClient) ListReviews(stdctx.Context, string, string, int, *gh.ListOptions) ([]*gh.PullRequestReview, *gh.Response, error) {
	if c.reviewsErr != nil {
		return nil, nil, c.reviewsErr
	}
	return c.reviews, &gh.Response{}, nil
}

func (c *convClient) ListReviewComments(stdctx.Context, string, string, int, *gh.PullRequestListCommentsOptions) ([]*gh.PullRequestComment, *gh.Response, error) {
	if c.reviewCmtErr != nil {
		return nil, nil, c.reviewCmtErr
	}
	return c.reviewComment, &gh.Response{}, nil
}

func (c *convClient) ListIssueComments(stdctx.Context, string, string, int, *gh.IssueListCommentsOptions) ([]*gh.IssueComment, *gh.Response, error) {
	if c.issueCmtErr != nil {
		return nil, nil, c.issueCmtErr
	}
	return c.issueComments, &gh.Response{}, nil
}

func convInfo() *PRInfo {
	return &PRInfo{Owner: "octo", Repo: "widget", Number: 7}
}

func TestFetchConversationRendersSections(t *testing.T) {
	c := &convClient{
		reviews: []*gh.PullRequestReview{
			{Body: gh.Ptr(ReviewMarker + "\nReview summary: 2 findings.")},
			{Body: gh.Ptr("a human review, no marker")}, // skipped: not miucr's
		},
		reviewComment: []*gh.PullRequestComment{
			{Path: gh.Ptr("src/auth.go"), Body: gh.Ptr("Unchecked error on token parse")},
		},
		issueComments: []*gh.IssueComment{
			{Body: gh.Ptr("I disagree, that path is unreachable")},
			{Body: gh.Ptr(ReviewMarker + "\nmiucr own summary comment")}, // skipped: bot
		},
	}

	out := FetchConversation(stdctx.Background(), c, convInfo())

	for _, want := range []string{
		"Prior miucr review summaries:",
		"Review summary: 2 findings.",
		"Inline finding threads:",
		"[src/auth.go] Unchecked error on token parse",
		"Developer replies:",
		"I disagree, that path is unreachable",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q\n--- got ---\n%s", want, out)
		}
	}
	if strings.Contains(out, "a human review, no marker") {
		t.Fatalf("non-miucr review leaked into summaries:\n%s", out)
	}
	if strings.Contains(out, "miucr own summary comment") {
		t.Fatalf("miucr's own issue comment leaked into developer replies:\n%s", out)
	}
}

func TestFetchConversationEmptyWhenNothing(t *testing.T) {
	c := &convClient{}
	if out := FetchConversation(stdctx.Background(), c, convInfo()); out != "" {
		t.Fatalf("want empty, got %q", out)
	}
}

func TestFetchConversationRespectsByteCap(t *testing.T) {
	big := strings.Repeat("x", maxConversationBytes*2)
	c := &convClient{
		issueComments: []*gh.IssueComment{{Body: gh.Ptr(big)}},
	}
	out := FetchConversation(stdctx.Background(), c, convInfo())
	if len([]rune(out)) > maxConversationBytes {
		t.Fatalf("output exceeds cap: %d runes", len([]rune(out)))
	}
	if !strings.HasSuffix(out, conversationTruncated) {
		t.Fatalf("truncated output missing ellipsis marker:\n%s", out)
	}
}

func TestFetchConversationListErrorDegradesToEmpty(t *testing.T) {
	c := &convClient{
		reviewsErr:    errors.New("boom"),
		reviewCmtErr:  errors.New("boom"),
		issueCmtErr:   errors.New("boom"),
		reviews:       []*gh.PullRequestReview{{Body: gh.Ptr(ReviewMarker + "\nshould not appear")}},
		issueComments: []*gh.IssueComment{{Body: gh.Ptr("should not appear")}},
	}
	if out := FetchConversation(stdctx.Background(), c, convInfo()); out != "" {
		t.Fatalf("list error must degrade to empty, got %q", out)
	}
}

func TestCapConversationValidUTF8(t *testing.T) {
	s := strings.Repeat("世界", 3000) // 3-byte runes, well over the cap
	out := capConversation(s)
	if !utf8.ValidString(out) {
		t.Fatal("capConversation produced invalid UTF-8 (split a multi-byte rune)")
	}
	if len(out) > maxConversationBytes {
		t.Fatalf("capConversation overshot the byte cap: %d > %d", len(out), maxConversationBytes)
	}
}
