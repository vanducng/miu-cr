package github

import (
	"testing"
	"time"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/diff"
)

func TestMergeReviewLedgerResolvesRevertedFile(t *testing.T) {
	f := mkFinding("a.go", "high", "bug", "func f() {}", "reverted")
	now := time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)
	prior := MergeLedger(nil, []engine.Finding{f}, "aaaaaa1", map[string]bool{"a.go": true}, now)
	deleted := []diff.Diff{{
		OldPath: "a.go", NewPath: "/dev/null", IsDeleted: true,
		Diff: "@@ -1,3 +0,0 @@\n-package p\n-\n-func f() {}\n",
	}}
	info := &PRInfo{
		HeadSHA:     "bbbbbb2",
		PriorLedger: prior,
		StableFPs:   UntouchedAnchors(prior, deleted),
	}
	out := MergeReviewLedger(info, nil, nil, now.Add(time.Hour))
	if out[0].Status != statusResolved || out[0].ResSHA != "bbbbbb2" {
		t.Fatalf("reverted file = %+v", out[0])
	}
	if len(GateFindings(nil, out)) != 0 {
		t.Fatal("resolved revert still blocks")
	}
	repeated := MergeReviewLedger(info, []engine.Finding{f}, nil, now.Add(time.Hour))
	if repeated[0].Status != statusOpen {
		t.Fatalf("repeated finding = %+v", repeated[0])
	}

	quiet := []diff.Diff{{OldPath: "b.go", NewPath: "b.go", Diff: "@@ -1 +1 @@\n-x\n+y\n"}}
	info.StableFPs = UntouchedAnchors(prior, quiet)
	kept := MergeReviewLedger(info, nil, nil, now.Add(2*time.Hour))
	if kept[0].Status != statusOpen {
		t.Fatalf("off-diff omission = %+v", kept[0])
	}
}

func TestShiftedLineFollowsInsert(t *testing.T) {
	quiet := engine.Finding{File: "a.go", Line: 5, Severity: "high", Category: "bug", QuotedCode: "keep()", Title: "stays"}
	now := time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)
	prior := MergeLedger(nil, []engine.Finding{quiet}, "aaaaaa1", map[string]bool{"a.go": true}, now)
	insert := []diff.Diff{{
		OldPath: "a.go", NewPath: "a.go",
		Diff: "@@ -1,2 +1,4 @@\n line1\n+added a\n+added b\n line2\n",
	}}
	lines := ShiftedLines(prior, insert)
	if lines[prior[0].FP].Line != 7 {
		t.Fatalf("shifted line = %d, want 7 (%v)", lines[prior[0].FP].Line, lines)
	}
	info := &PRInfo{
		HeadSHA:     "bbbbbb2",
		PriorLedger: prior,
		StableFPs:   UntouchedAnchors(prior, insert),
		StableLines: lines,
	}
	out := MergeReviewLedger(info, nil, []diff.Diff{{NewPath: "a.go"}}, now.Add(time.Hour))
	if out[0].Status != statusOpen || out[0].Line != 7 {
		t.Fatalf("preserved anchor = %+v", out[0])
	}
	deleted := []diff.Diff{{OldPath: "a.go", NewPath: "a.go", Diff: "@@ -7,1 +7,0 @@\n-keep()\n"}}
	if !anchorRemoved(out[0], deleted) {
		t.Fatal("deleting the shifted line must remove the anchor")
	}
	stale := out[0]
	stale.Line = 5
	if anchorRemoved(stale, deleted) {
		t.Fatal("the pre-shift line number is a different line")
	}
	zero := []diff.Diff{{OldPath: "a.go", NewPath: "a.go", Diff: "@@ -1,0 +2,2 @@\n+added a\n+added b\n"}}
	if got := ShiftedLines(prior, zero)[prior[0].FP].Line; got != 7 {
		t.Fatalf("zero-context insert line = %d, want 7", got)
	}
	above := []diff.Diff{{OldPath: "a.go", NewPath: "a.go", Diff: "@@ -1,2 +1,1 @@\n line1\n-gone\n"}}
	if got := ShiftedLines(prior, above)[prior[0].FP].Line; got != 4 {
		t.Fatalf("delete-above line = %d, want 4", got)
	}
	both := []diff.Diff{{
		OldPath: "a.go", NewPath: "a.go",
		Diff: "@@ -1,0 +2,2 @@\n+added a\n+added b\n@@ -5,1 +7,1 @@\n keep()\n",
	}}
	if got := ShiftedLines(prior, both)[prior[0].FP].Line; got != 7 {
		t.Fatalf("two-hunk line = %d, want 7", got)
	}
	renamed := []diff.Diff{{OldPath: "a.go", NewPath: "b.go", IsRenamed: true}}
	if got := ShiftedLines(prior, renamed)[prior[0].FP]; got.Path != "b.go" || got.Line != 5 {
		t.Fatalf("rename = %+v", got)
	}
	info.StableFPs = UntouchedAnchors(prior, renamed)
	info.StableLines = ShiftedLines(prior, renamed)
	renamedOut := MergeReviewLedger(info, nil, renamed, now.Add(2*time.Hour))
	if renamedOut[0].Status != statusOpen || renamedOut[0].Path != "b.go" {
		t.Fatalf("preserved rename = %+v", renamedOut[0])
	}
	fresh := []diff.Diff{{OldPath: "/dev/null", NewPath: "a.go", IsNew: true, Diff: "@@ -0,0 +1,1 @@\n+package p\n"}}
	if !anchorRemoved(prior[0], fresh) {
		t.Fatal("a new file has no prior line")
	}
}
