package github

import (
	"strings"
	"testing"
	"time"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/diff"
)

func deltaDiff() []diff.Diff {
	return []diff.Diff{{
		OldPath: "a.go",
		NewPath: "a.go",
		Diff:    "@@ -1,4 +1,5 @@\n package p\n \n func f() {\n+\treturn 1\n }\n",
	}}
}

func TestSelectStableFindings(t *testing.T) {
	old := engine.Finding{File: "b.go", Line: 3, Severity: "high", Category: "bug", QuotedCode: "old()", Title: "already open"}
	fresh := engine.Finding{File: "a.go", Line: 4, Severity: "medium", Category: "bug", QuotedCode: "return 1", Title: "on the new line"}
	context := engine.Finding{File: "a.go", Line: 1, Severity: "low", Category: "bug", QuotedCode: "package p", Title: "untouched context"}
	other := engine.Finding{File: "b.go", Line: 9, Severity: "high", Category: "bug", QuotedCode: "elsewhere()", Title: "new on old scope"}
	prior := MergeLedger(nil, []engine.Finding{old}, "aaaaaa1", map[string]bool{"b.go": true}, time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC))

	kept, dropped := SelectStableFindings([]engine.Finding{old, fresh, context, other}, prior, deltaDiff())
	if dropped != 2 {
		t.Fatalf("dropped = %d, want 2", dropped)
	}
	got := map[string]bool{}
	for _, f := range kept {
		got[f.Title] = true
	}
	if !got["already open"] || !got["on the new line"] || got["untouched context"] || got["new on old scope"] {
		t.Fatalf("kept = %#v", got)
	}

	all, n := SelectStableFindings([]engine.Finding{other}, nil, nil)
	if n != 0 || len(all) != 1 {
		t.Fatalf("unknown delta must keep findings, dropped %d kept %d", n, len(all))
	}
}

func TestQuoteCommonContextLineDoesNotAdmit(t *testing.T) {
	ambiguous := []diff.Diff{{
		OldPath: "a.go", NewPath: "a.go",
		Diff: "@@ -1,4 +1,4 @@\n func f() error {\n \treturn err\n-\treturn err\n+\treturn nil\n }\n",
	}}
	common := engine.Finding{File: "a.go", Severity: "high", Category: "bug", QuotedCode: "return err", Title: "common"}
	unique := engine.Finding{File: "a.go", Severity: "high", Category: "bug", QuotedCode: "return nil", Title: "unique"}
	kept, dropped := SelectStableFindings([]engine.Finding{common, unique}, nil, ambiguous)
	if dropped != 1 || len(kept) != 1 || kept[0].Title != "unique" {
		t.Fatalf("kept %+v dropped %d", kept, dropped)
	}
}

func TestQuoteRequiresEveryLineChanged(t *testing.T) {
	mixed := engine.Finding{File: "a.go", Severity: "high", Category: "bug", QuotedCode: "func g() {\n\told()", Title: "mixed"}
	only := engine.Finding{File: "a.go", Severity: "high", Category: "bug", QuotedCode: "old()", Title: "deleted"}
	kept, dropped := SelectStableFindings([]engine.Finding{mixed, only}, nil, replaceDiff())
	if dropped != 1 || len(kept) != 1 || kept[0].Title != "deleted" {
		t.Fatalf("kept %+v dropped %d", kept, dropped)
	}
}

func TestSelectStableFindingsUsesQuoteNotLineNumber(t *testing.T) {
	misnumbered := engine.Finding{File: "a.go", Line: 8, Severity: "high", Category: "bug", QuotedCode: "new()", Title: "quote is the new line"}
	wrongQuote := engine.Finding{File: "a.go", Line: 9, Severity: "high", Category: "bug", QuotedCode: "func g() {", Title: "line number is new, quote is not"}
	deleted := engine.Finding{File: "a.go", Line: 8, Severity: "high", Category: "bug", QuotedCode: "old()", Title: "removed check"}
	kept, dropped := SelectStableFindings([]engine.Finding{misnumbered, wrongQuote, deleted}, nil, replaceDiff())
	if dropped != 1 || len(kept) != 2 {
		t.Fatalf("kept %d dropped %d", len(kept), dropped)
	}
	got := map[string]bool{}
	for _, f := range kept {
		got[f.Title] = true
	}
	if !got["quote is the new line"] || !got["removed check"] || got["line number is new, quote is not"] {
		t.Fatalf("kept = %#v", got)
	}
}

func replaceDiff() []diff.Diff {
	return []diff.Diff{{
		OldPath: "a.go",
		NewPath: "a.go",
		Diff:    "@@ -8,3 +8,3 @@\n func g() {\n-\told()\n+\tnew()\n }\n",
	}}
}

func TestUntouchedAnchorStaysOpenWhenFileChangesElsewhere(t *testing.T) {
	quiet := engine.Finding{File: "a.go", Line: 1, Severity: "high", Category: "bug", QuotedCode: "package p", Title: "elsewhere in the file"}
	edited := engine.Finding{File: "a.go", Line: 9, Severity: "high", Category: "bug", QuotedCode: "old()", Title: "on the replaced line"}
	now := time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)
	prior := MergeLedger(nil, []engine.Finding{quiet, edited}, "aaaaaa1", map[string]bool{"a.go": true}, now)
	merged := MergeLedger(prior, nil, "bbbbbb2", map[string]bool{"a.go": true}, now.Add(time.Hour))
	untouched := UntouchedAnchors(prior, replaceDiff())
	out := PreserveUntouchedAnchors(merged, prior, untouched)
	if eb := find(t, out, quiet); eb.Status != statusOpen {
		t.Fatalf("untouched line must stay open, got %+v", eb)
	}
	if eb := find(t, out, edited); eb.Status != statusResolved {
		t.Fatalf("deleted line resolves when omitted, got %+v", eb)
	}
	if UntouchedAnchors(prior, nil) != nil {
		t.Fatal("nil delta must return nil")
	}
}

func TestSelectStableFindingsDoesNotReviveResolved(t *testing.T) {
	f := engine.Finding{File: "b.go", Line: 3, Severity: "low", Category: "bug", QuotedCode: "old()", Title: "done"}
	now := time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)
	prior := MergeLedger(nil, []engine.Finding{f}, "aaaaaa1", map[string]bool{"b.go": true}, now)
	prior = MergeLedger(prior, nil, "bbbbbb2", map[string]bool{"b.go": true}, now.Add(time.Hour))
	kept, dropped := SelectStableFindings([]engine.Finding{f}, prior, deltaDiff())
	if dropped != 1 || len(kept) != 0 {
		t.Fatalf("resolved finding on an unchanged line must drop, kept %d dropped %d", len(kept), dropped)
	}
}

func TestPreserveStableFindings(t *testing.T) {
	a := mkFinding("a.go", "high", "bug", "changed()", "touched")
	b := mkFinding("b.go", "medium", "bug", "quiet()", "untouched")
	now := time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)
	paths := map[string]bool{"a.go": true, "b.go": true}
	prior := MergeLedger(nil, []engine.Finding{a, b}, "aaaaaa1", paths, now)
	merged := MergeLedger(prior, []engine.Finding{a}, "bbbbbb2", paths, now.Add(time.Hour))
	stable := map[string]bool{"b.go": true}

	out := PreserveStableFindings(merged, prior, stable)
	if eb := find(t, out, b); eb.Status != statusOpen || eb.ResSHA != "" {
		t.Fatalf("unchanged file must stay open, got %+v", eb)
	}
	if ea := find(t, out, a); ea.Status != statusOpen {
		t.Fatalf("repeated finding stays open, got %+v", ea)
	}

	resolved := PreserveStableFindings(merged, prior, map[string]bool{"a.go": true})
	if eb := find(t, resolved, b); eb.Status != statusResolved {
		t.Fatalf("changed file still resolves when omitted, got %+v", eb)
	}
	if same := PreserveStableFindings(merged, prior, nil); find(t, same, b).Status != statusResolved {
		t.Fatal("nil stable set must keep legacy resolution")
	}
}

func TestMergeReviewLedgerKeepsStableOpen(t *testing.T) {
	b := mkFinding("b.go", "medium", "bug", "quiet()", "untouched")
	now := time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)
	info := &PRInfo{
		HeadSHA:     "bbbbbb2",
		PriorLedger: MergeLedger(nil, []engine.Finding{b}, "aaaaaa1", map[string]bool{"b.go": true}, now),
		StablePaths: map[string]bool{"b.go": true},
	}
	ledger := MergeReviewLedger(info, nil, []diff.Diff{{NewPath: "b.go"}}, now.Add(time.Hour))
	if ledger[0].Status != statusOpen {
		t.Fatalf("status = %s, want open", ledger[0].Status)
	}
}

func TestGateFindingsIncludesOmittedOpen(t *testing.T) {
	b := mkFinding("b.go", "high", "bug", "quiet()", "untouched")
	now := time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)
	ledger := MergeLedger(nil, []engine.Finding{b}, "aaaaaa1", map[string]bool{"b.go": true}, now)
	got := GateFindings(nil, ledger)
	if len(got) != 1 || got[0].Severity != "high" || got[0].File != "b.go" {
		t.Fatalf("gate findings = %+v", got)
	}
}

func TestAppendCarriedOffDiffUsesLedgerFingerprint(t *testing.T) {
	e := LedgerEntry{FP: "abc123", Path: "b.go", Line: 4, Title: "quiet", Sev: "high", Status: statusOpen}
	body := AppendCarriedOffDiff("", []LedgerEntry{e})
	if !strings.Contains(body, fpMarker("abc123")) || !strings.Contains(body, "Still open from the earlier review.") {
		t.Fatalf("carried body = %s", body)
	}
}

func TestStablePaths(t *testing.T) {
	prior := []LedgerEntry{{Path: "b.go"}, {Path: "a.go"}}
	got := StablePaths([]string{"a.go", "b.go", "c.go"}, prior, deltaDiff())
	if got["a.go"] || !got["b.go"] || !got["c.go"] {
		t.Fatalf("stable = %#v", got)
	}
	if StablePaths(nil, nil, nil) != nil {
		t.Fatal("nil delta must return nil")
	}
}
