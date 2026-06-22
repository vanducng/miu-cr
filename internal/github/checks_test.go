package github

import (
	stdctx "context"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/diff"
)

func TestPostChecksBuildsCheckRunWithAnnotations(t *testing.T) {
	c := &recordClient{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 7, HeadSHA: "headsha"}
	findings := []engine.Finding{
		{File: "p.go", Line: 2, EndLine: 3, Severity: "high", Category: "bug", Rationale: "leak"},    // in hunk → annotation, failure level
		{File: "p.go", Line: 4, Severity: "medium", Category: "style", Rationale: "nit"},             // context line → warning level
		{File: "p.go", Line: 99, Severity: "critical", Category: "x", Rationale: "off-diff dropped"}, // out of hunk → dropped
	}
	res, err := PostChecks(stdctx.Background(), c, info, findings, sampleDiffs(), map[string]any{"files_reviewed": float64(1)}, false, FilterDiffContext)
	if err != nil {
		t.Fatalf("PostChecks: %v", err)
	}
	if c.checkRunN != 1 || c.gotCheck == nil {
		t.Fatalf("want exactly one CreateCheckRun, got %d", c.checkRunN)
	}
	if c.gotCheck.HeadSHA != "headsha" {
		t.Fatalf("CheckRun head SHA = %q, want headsha", c.gotCheck.HeadSHA)
	}
	if c.gotCheck.GetConclusion() != "failure" {
		t.Fatalf("conclusion = %q, want failure (gate hit)", c.gotCheck.GetConclusion())
	}
	if res.Annotations != 2 {
		t.Fatalf("want 2 annotations (off-diff dropped), got %d", res.Annotations)
	}
	anns := c.gotCheck.Output.Annotations
	if len(anns) != 2 {
		t.Fatalf("want 2 annotations on the create call, got %d", len(anns))
	}
	if anns[0].GetStartLine() != 2 || anns[0].GetEndLine() != 3 || anns[0].GetAnnotationLevel() != "failure" {
		t.Fatalf("bad first annotation: %+v", anns[0])
	}
	if anns[0].GetMessage() != "leak" {
		t.Fatalf("annotation message = %q, want leak", anns[0].GetMessage())
	}
	if anns[1].GetAnnotationLevel() != "warning" {
		t.Fatalf("medium severity must map to warning, got %q", anns[1].GetAnnotationLevel())
	}
}

func TestPostChecksGateCleanSuccess(t *testing.T) {
	c := &recordClient{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"}
	res, err := PostChecks(stdctx.Background(), c, info, nil, sampleDiffs(), nil, true, FilterDiffContext)
	if err != nil {
		t.Fatalf("PostChecks: %v", err)
	}
	if res.Conclusion != "success" || c.gotCheck.GetConclusion() != "success" {
		t.Fatalf("gate-clean must map to success, got %q", res.Conclusion)
	}
	if !strings.Contains(c.gotCheck.Output.GetSummary(), "No findings") {
		t.Fatalf("empty summary should say No findings: %q", c.gotCheck.Output.GetSummary())
	}
}

func TestPostChecksBatchesAnnotationsOver50(t *testing.T) {
	// A wide hunk with 120 added lines, one finding per line → 3 batches (50/50/20).
	var diffLines strings.Builder
	diffLines.WriteString("@@ -1,1 +1,120 @@\n")
	for i := 0; i < 120; i++ {
		diffLines.WriteString("+line\n")
	}
	diffs := []diff.Diff{{NewPath: "p.go", Diff: diffLines.String()}}
	findings := make([]engine.Finding, 0, 120)
	for i := 1; i <= 120; i++ {
		findings = append(findings, engine.Finding{File: "p.go", Line: i, Severity: "high", Category: "bug", Rationale: "x"})
	}
	c := &recordClient{}
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"}
	res, err := PostChecks(stdctx.Background(), c, info, findings, diffs, nil, false, FilterDiffContext)
	if err != nil {
		t.Fatalf("PostChecks: %v", err)
	}
	if res.Annotations != 120 {
		t.Fatalf("want 120 annotations, got %d", res.Annotations)
	}
	if len(c.gotCheck.Output.Annotations) != maxAnnotationsPerBatch {
		t.Fatalf("create call must carry exactly 50 annotations, got %d", len(c.gotCheck.Output.Annotations))
	}
	if len(c.gotCheckUpd) != 2 {
		t.Fatalf("want 2 update batches after the create (50/50/20), got %d", len(c.gotCheckUpd))
	}
	if got := len(c.gotCheckUpd[1].Output.Annotations); got != 20 {
		t.Fatalf("last batch should carry 20 annotations, got %d", got)
	}
}
