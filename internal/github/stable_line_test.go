package github

import (
	"testing"
	"time"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/diff"
)

func TestShiftedLineFollowsInsert(t *testing.T) {
	quiet := engine.Finding{File: "a.go", Line: 5, Severity: "high", Category: "bug", QuotedCode: "keep()", Title: "stays"}
	now := time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)
	prior := MergeLedger(nil, []engine.Finding{quiet}, "aaaaaa1", map[string]bool{"a.go": true}, now)
	insert := []diff.Diff{{
		OldPath: "a.go", NewPath: "a.go",
		Diff: "@@ -1,2 +1,4 @@\n line1\n+added a\n+added b\n line2\n",
	}}
	lines := ShiftedLines(prior, insert)
	if lines[prior[0].FP] != 7 {
		t.Fatalf("shifted line = %d, want 7 (%v)", lines[prior[0].FP], lines)
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
	if got := ShiftedLines(prior, zero)[prior[0].FP]; got != 7 {
		t.Fatalf("zero-context insert line = %d, want 7", got)
	}
	above := []diff.Diff{{OldPath: "a.go", NewPath: "a.go", Diff: "@@ -1,2 +1,1 @@\n line1\n-gone\n"}}
	if got := ShiftedLines(prior, above)[prior[0].FP]; got != 4 {
		t.Fatalf("delete-above line = %d, want 4", got)
	}
	both := []diff.Diff{{
		OldPath: "a.go", NewPath: "a.go",
		Diff: "@@ -1,0 +2,2 @@\n+added a\n+added b\n@@ -5,1 +7,1 @@\n keep()\n",
	}}
	if got := ShiftedLines(prior, both)[prior[0].FP]; got != 7 {
		t.Fatalf("two-hunk line = %d, want 7", got)
	}
	fresh := []diff.Diff{{OldPath: "/dev/null", NewPath: "a.go", IsNew: true, Diff: "@@ -0,0 +1,1 @@\n+package p\n"}}
	if !anchorRemoved(prior[0], fresh) {
		t.Fatal("a new file has no prior line")
	}
}
