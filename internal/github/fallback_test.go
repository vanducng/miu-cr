package github

import (
	stdctx "context"
	"net/http"
	"strings"
	"testing"

	gh "github.com/google/go-github/v84/github"

	"github.com/vanducng/miu-cr/internal/engine"
)

func forbidden403() error {
	return &gh.ErrorResponse{
		Response: &http.Response{StatusCode: 403},
		Message:  "Resource not accessible by integration",
	}
}

func TestPostReviewForkFallbackUnderActions(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	c := &recordClient{createReviewErr: forbidden403()}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"}
	findings := []engine.Finding{
		{File: "p.go", Line: 2, EndLine: 3, Severity: "high", Category: "bug", Rationale: "leak\nhere"},
		{File: "p.go", Line: 4, Severity: "medium", Category: "style", Rationale: "nit"},
	}
	var out strings.Builder
	res, err := PostReview(stdctx.Background(), c, info, findings, sampleDiffs(), "summary", nil, PostReviewOptions{ActionsOut: &out})
	if err != nil {
		t.Fatalf("fork fallback must not hard-fail, got error: %v", err)
	}
	if res.Fallback != 2 {
		t.Fatalf("want 2 workflow annotations emitted, got %d", res.Fallback)
	}
	if res.Posted != 0 {
		t.Fatalf("nothing should be reported as posted on the fallback, got %d", res.Posted)
	}
	got := out.String()
	if !strings.Contains(got, "::error file=p.go,line=2,endLine=3::leak%0Ahere") {
		t.Fatalf("missing/incorrect first ::error:: command:\n%s", got)
	}
	if !strings.Contains(got, "::error file=p.go,line=4,endLine=4::nit") {
		t.Fatalf("missing second ::error:: command:\n%s", got)
	}
}

func TestPostReview403NotUnderActionsStillErrors(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	c := &recordClient{createReviewErr: forbidden403()}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"}
	findings := []engine.Finding{{File: "p.go", Line: 2, Severity: "high", Category: "bug", Rationale: "x"}}
	if _, err := PostReview(stdctx.Background(), c, info, findings, sampleDiffs(), "summary", nil, PostReviewOptions{}); err == nil {
		t.Fatal("a 403 outside Actions must still surface as an error")
	}
}
