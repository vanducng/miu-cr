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

func changedDiff() []diff.Diff {
	return []diff.Diff{{
		NewPath: "a.go",
		Diff:    "@@ -1,4 +1,5 @@\n package p\n \n func f() {\n+\treturn 1\n }\n",
	}}
}

func TestClassifyOffDiff(t *testing.T) {
	diffs := changedDiff()
	inline := engine.Finding{File: "a.go", Line: 4, Severity: "high", Title: "on the hunk"}
	if ClassifyOffDiff(inline, diffs) != offDiffInline {
		t.Fatalf("changed line class = %s", ClassifyOffDiff(inline, diffs))
	}
	nearby := engine.Finding{File: "a.go", Line: 80, Severity: "low", Title: "same file"}
	if ClassifyOffDiff(nearby, diffs) != offDiffThread {
		t.Fatalf("off-hunk class = %s", ClassifyOffDiff(nearby, diffs))
	}
	noise := engine.Finding{File: "other.go", Line: 3, Severity: "info", Title: "style"}
	if ClassifyOffDiff(noise, diffs) != offDiffNoise {
		t.Fatalf("untouched info class = %s", ClassifyOffDiff(noise, diffs))
	}
	real := engine.Finding{File: "other.go", Line: 3, Severity: "high", Title: "must fix"}
	if ClassifyOffDiff(real, diffs) != offDiffThread {
		t.Fatalf("untouched high class = %s", ClassifyOffDiff(real, diffs))
	}
}

func TestApplyOffDiffDispositionAndAnchor(t *testing.T) {
	diffs := changedDiff()
	noise := engine.Finding{File: "other.go", Line: 3, Severity: "info", Title: "nit", QuotedCode: "x"}
	real := engine.Finding{File: "other.go", Line: 9, Severity: "high", Title: "bug", QuotedCode: "y"}
	entries := MergeLedger(nil, []engine.Finding{noise, real}, "abc", map[string]bool{"a.go": true}, time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC))
	entries = ApplyOffDiffDisposition(entries, []engine.Finding{noise, real}, diffs)
	byFP := map[string]LedgerEntry{}
	for _, e := range entries {
		byFP[e.FP] = e
	}
	if byFP[Fingerprint(noise)].Status != statusIrrelevant {
		t.Fatalf("noise status = %s", byFP[Fingerprint(noise)].Status)
	}
	if byFP[Fingerprint(real)].Status != statusOpen {
		t.Fatalf("real status = %s", byFP[Fingerprint(real)].Status)
	}
	blocking := BlockingFindings([]engine.Finding{noise, real}, entries)
	if len(blocking) != 1 || blocking[0].Title != "bug" {
		t.Fatalf("blocking = %+v", blocking)
	}
	path, line, ok := NearestAnchor(real, diffs)
	if !ok || path != "a.go" || line != 1 {
		t.Fatalf("anchor = %s:%d ok=%v", path, line, ok)
	}
	body := RenderOffDiffComment([]engine.Finding{real})
	if !strings.Contains(body, offDiffMarker) || !strings.Contains(body, fpMarker(Fingerprint(real))) {
		t.Fatalf("off-diff body missing markers:\n%s", body)
	}
}

func TestOffDiffThreadHonorsSeverityFloor(t *testing.T) {
	low := engine.Finding{File: "other.go", Line: 2, Severity: "low", Title: "nit", QuotedCode: "a"}
	high := engine.Finding{File: "other.go", Line: 3, Severity: "high", Title: "bug", QuotedCode: "b"}
	diffs := changedDiff()
	findings := []engine.Finding{low, high}
	ledger := MergeLedger(nil, findings, "abc", map[string]bool{"a.go": true}, time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC))
	got := MinSeverityFloor(ActionableOffDiffFindings(findings, diffs, ledger), "high")
	if len(got) != 1 || got[0].Title != "bug" {
		t.Fatalf("off-diff thread = %+v", got)
	}
}

func TestUpsertOffDiffIssueCommentEditsInPlace(t *testing.T) {
	info := &PRInfo{Owner: "acme", Repo: "app", Number: 1, HeadSHA: "abc"}
	f := engine.Finding{File: "other.go", Line: 3, Severity: "high", Title: "bug", Rationale: "breaks the bound"}
	client := &recordClient{login: "reviewer", issueStore: []*gh.IssueComment{}}
	if _, err := UpsertOffDiffComment(stdctx.Background(), client, info, []engine.Finding{f}, nil); err != nil {
		t.Fatal(err)
	}
	if client.createIssueN != 1 {
		t.Fatalf("creates = %d", client.createIssueN)
	}
	if _, err := UpsertOffDiffComment(stdctx.Background(), client, info, []engine.Finding{f}, nil); err != nil {
		t.Fatal(err)
	}
	if client.createIssueN != 1 {
		t.Fatalf("second pass created another comment: %d", client.createIssueN)
	}
	if _, err := UpsertOffDiffComment(stdctx.Background(), client, info, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(client.issueStore[0].GetBody(), "No off-diff findings remain.") {
		t.Fatalf("body = %s", client.issueStore[0].GetBody())
	}
}

func TestIrrelevantPromotesWhenInlineAgain(t *testing.T) {
	f := engine.Finding{File: "a.go", Line: 4, Severity: "info", Title: "was noise", QuotedCode: "return 1"}
	entries := []LedgerEntry{{FP: Fingerprint(f), Path: "other.go", Status: statusIrrelevant, Sev: "info", FirstSev: "info"}}
	got := ApplyOffDiffDisposition(entries, []engine.Finding{f}, changedDiff())
	if got[0].Status != statusOpen {
		t.Fatalf("status = %s, want open", got[0].Status)
	}
}
