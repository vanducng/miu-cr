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

// A file path carrying workflow-command delimiters (',', ':') or a newline must be
// property-escaped so it can't terminate the file= property early or inject a fake
// ::error:: annotation; the rationale's '%' must be data-escaped. The added line
// here lands on new-side line 2 of sampleFileDiff so it is in-hunk + inline-eligible.
func TestEmitWorkflowAnnotationsEscapesProperties(t *testing.T) {
	var out strings.Builder
	findings := []engine.Finding{
		{File: "weird,path:x\ninjected.go", Line: 2, EndLine: 3, Rationale: "100% busted"},
	}
	n := emitWorkflowAnnotations(&out, findings)
	if n != 1 {
		t.Fatalf("want 1 annotation emitted, got %d", n)
	}
	got := out.String()
	want := "::error file=weird%2Cpath%3Ax%0Ainjected.go,line=2,endLine=3::100%25 busted\n"
	if got != want {
		t.Fatalf("escaping mismatch:\n got=%q\nwant=%q", got, want)
	}
	// Exactly one command line — a raw newline/comma in the path did NOT inject a second.
	if c := strings.Count(got, "::error"); c != 1 {
		t.Fatalf("path delimiters must not inject extra commands, got %d ::error tokens:\n%s", c, got)
	}
}

// A finding with Line<=0 (file-level / drift, not line-anchorable) must emit a
// FILE-level workflow annotation — `file=` with no line/endLine (the grammar allows
// it); emitting line=0 is rejected by the runner.
func TestEmitWorkflowAnnotationsFileLevelForNonPositiveLine(t *testing.T) {
	var out strings.Builder
	findings := []engine.Finding{
		{File: "p.go", Line: 0, Rationale: "file-level"},
		{File: "q.go", Line: -3, Rationale: "negative"},
	}
	n := emitWorkflowAnnotations(&out, findings)
	if n != 2 {
		t.Fatalf("file-level findings must still be emitted, got %d", n)
	}
	got := out.String()
	for _, want := range []string{"::error file=p.go::file-level\n", "::error file=q.go::negative\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing file-level annotation %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "line=0") || strings.Contains(got, "line=-3") {
		t.Fatalf("a Line<=0 finding must not emit a line= property:\n%s", got)
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
