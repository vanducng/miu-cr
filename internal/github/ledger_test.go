package github

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vanducng/miu-cr/internal/engine"
)

// fpStr returns a unique 16-hex fingerprint for index i (collision-free, unlike fpHex).
func fpStr(i int) string { return fmt.Sprintf("%016x", i) }

func mkFinding(file, sev, cat, quoted, title string) engine.Finding {
	return engine.Finding{File: file, Line: 10, Severity: sev, Category: cat, QuotedCode: quoted, Title: title}
}

// find returns the ledger entry for a finding's fingerprint, failing if absent.
func find(t *testing.T, ledger []LedgerEntry, f engine.Finding) LedgerEntry {
	t.Helper()
	fp := Fingerprint(f)
	for _, e := range ledger {
		if e.FP == fp {
			return e
		}
	}
	t.Fatalf("no ledger entry for fp %s (file %s)", fp, f.File)
	return LedgerEntry{}
}

func TestMergeLedgerOpensNewFindings(t *testing.T) {
	t1 := time.Date(2026, 6, 26, 22, 0, 0, 0, time.UTC)
	a := mkFinding("a.go", "critical", "security", "danger()", "SQL injection")
	b := mkFinding("b.go", "low", "style", "x := 1", "Unchecked error")

	ledger := MergeLedger(nil, []engine.Finding{a, b}, "aaaaaa1", map[string]bool{"a.go": true, "b.go": true}, t1)

	if len(ledger) != 2 {
		t.Fatalf("want 2 entries, got %d", len(ledger))
	}
	ea := find(t, ledger, a)
	if ea.Status != statusOpen || ea.OpenSHA != "aaaaaa1" || ea.FirstSev != "critical" || ea.Sev != "critical" {
		t.Fatalf("new finding A not opened correctly: %+v", ea)
	}
	if ea.FirstAt != t1.Format(time.RFC3339) {
		t.Fatalf("FirstAt = %q, want %q", ea.FirstAt, t1.Format(time.RFC3339))
	}
}

func TestMergeLedgerResolvesWhenAbsentAndPathInDiff(t *testing.T) {
	t1 := time.Date(2026, 6, 26, 22, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	a := mkFinding("a.go", "critical", "security", "danger()", "SQL injection")
	b := mkFinding("b.go", "low", "style", "x := 1", "Unchecked error")
	paths := map[string]bool{"a.go": true, "b.go": true}

	run1 := MergeLedger(nil, []engine.Finding{a, b}, "aaaaaa1", paths, t1)
	// run2: B is gone but b.go is still in the diff → resolved.
	run2 := MergeLedger(run1, []engine.Finding{a}, "bbbbbb2", paths, t2)

	eb := find(t, run2, b)
	if eb.Status != statusResolved || eb.ResSHA != "bbbbbb2" || eb.ResAt != t2.Format(time.RFC3339) {
		t.Fatalf("B should be resolved at bbbbbb2: %+v", eb)
	}
	if eb.OpenSHA != "aaaaaa1" {
		t.Fatalf("B origin commit must be preserved, got %q", eb.OpenSHA)
	}
	if ea := find(t, run2, a); ea.Status != statusOpen {
		t.Fatalf("A should still be open: %+v", ea)
	}
}

func TestMergeLedgerOffDiffAbsenceIsNotAFix(t *testing.T) {
	t1 := time.Date(2026, 6, 26, 22, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	d := mkFinding("d.go", "high", "bug", "leak()", "Leak")

	run1 := MergeLedger(nil, []engine.Finding{d}, "aaaaaa1", map[string]bool{"d.go": true}, t1)
	// run2: d.go is NOT in this push's diff → absence must NOT resolve it.
	run2 := MergeLedger(run1, nil, "bbbbbb2", map[string]bool{"other.go": true}, t2)

	if ed := find(t, run2, d); ed.Status != statusOpen {
		t.Fatalf("off-diff finding must linger as open, got %q", ed.Status)
	}
}

func TestMergeLedgerReopen(t *testing.T) {
	t1 := time.Date(2026, 6, 26, 22, 0, 0, 0, time.UTC)
	paths := map[string]bool{"a.go": true}
	a := mkFinding("a.go", "medium", "bug", "boom()", "Crash")

	run1 := MergeLedger(nil, []engine.Finding{a}, "aaaaaa1", paths, t1)
	run2 := MergeLedger(run1, nil, "bbbbbb2", paths, t1.Add(time.Hour))                   // resolved
	run3 := MergeLedger(run2, []engine.Finding{a}, "cccccc3", paths, t1.Add(2*time.Hour)) // reappears

	ea := find(t, run3, a)
	if ea.Status != statusReopened || ea.Reopens != 1 {
		t.Fatalf("A should be reopened with Reopens=1: %+v", ea)
	}
	if ea.ResSHA != "" || ea.ResAt != "" {
		t.Fatalf("reopen must clear resolved fields: %+v", ea)
	}
	if ea.OpenSHA != "aaaaaa1" {
		t.Fatalf("origin commit must survive a reopen, got %q", ea.OpenSHA)
	}
}

func TestMergeLedgerSeverityEscalation(t *testing.T) {
	t1 := time.Date(2026, 6, 26, 22, 0, 0, 0, time.UTC)
	paths := map[string]bool{"a.go": true}
	low := mkFinding("a.go", "low", "bug", "boom()", "Crash")
	high := mkFinding("a.go", "high", "bug", "boom()", "Crash") // same fp, escalated severity

	run1 := MergeLedger(nil, []engine.Finding{low}, "aaaaaa1", paths, t1)
	run2 := MergeLedger(run1, []engine.Finding{high}, "bbbbbb2", paths, t1.Add(time.Hour))

	ea := find(t, run2, high)
	if ea.FirstSev != "low" || ea.Sev != "high" {
		t.Fatalf("want FirstSev=low (before) and Sev=high (after), got FirstSev=%q Sev=%q", ea.FirstSev, ea.Sev)
	}
	if cell := ledgerSevCell(ea, false); !strings.Contains(cell, "→") {
		t.Fatalf("escalated open finding should render a before→after sev cell, got %q", cell)
	}
}

func TestLedgerMarkerRoundTrip(t *testing.T) {
	in := []LedgerEntry{
		{FP: "0123456789abcdef", Path: "a.go", Line: 9, Title: "Title -->break<-- attempt", Status: statusOpen, Sev: "high", FirstSev: "high", OpenSHA: "aaaaaa1", FirstAt: "2026-06-26T22:00:00Z"},
		{FP: "fedcba9876543210", Path: "b.go", Status: statusResolved, Sev: "low", FirstSev: "low", OpenSHA: "aaaaaa1", ResSHA: "bbbbbb2", FirstAt: "2026-06-26T22:00:00Z", ResAt: "2026-06-26T23:00:00Z", Reopens: 2},
	}
	marker := renderLedgerMarker(in)
	if !strings.Contains(marker, "<!-- "+ledgerPrefix) {
		t.Fatalf("marker missing prefix: %q", marker)
	}
	// The base64 payload must not contain a comment-breaking "-->" even though a
	// title literally embeds one.
	if strings.Count(marker, "-->") != 1 {
		t.Fatalf("untrusted title broke out of the HTML comment: %q", marker)
	}
	out := ParseLedger("noise\n" + marker + "\nmore noise")
	if len(out) != len(in) {
		t.Fatalf("round-trip length mismatch: got %d want %d", len(out), len(in))
	}
	if out[0].Title != in[0].Title || out[1].Reopens != 2 || out[1].ResSHA != "bbbbbb2" {
		t.Fatalf("round-trip field mismatch: %+v", out)
	}
}

func TestParseLedgerAbsentOrCorrupt(t *testing.T) {
	if ParseLedger("no marker here") != nil {
		t.Fatal("absent marker must return nil")
	}
	if ParseLedger("<!-- miu-cr-ledger:!!!notbase64!!! -->") != nil {
		t.Fatal("corrupt base64 must return nil")
	}
}

func TestCapLedgerDropsOldestResolvedKeepsOpen(t *testing.T) {
	var entries []LedgerEntry
	// 5 open (kept) + (maxLedgerEntries) resolved, oldest should be dropped.
	for i := 0; i < 5; i++ {
		entries = append(entries, LedgerEntry{FP: fpHex(i), Status: statusOpen, Sev: "high"})
	}
	for i := 0; i < maxLedgerEntries; i++ {
		entries = append(entries, LedgerEntry{FP: fpHex(1000 + i), Status: statusResolved, ResAt: time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC).Format(time.RFC3339)})
	}
	out := capLedger(entries)
	if len(out) != maxLedgerEntries {
		t.Fatalf("capped length = %d, want %d", len(out), maxLedgerEntries)
	}
	openKept := 0
	for _, e := range out {
		if e.Status == statusOpen {
			openKept++
		}
	}
	if openKept != 5 {
		t.Fatalf("all 5 open findings must survive the cap, got %d", openKept)
	}
}

func fpHex(n int) string {
	const hex = "0123456789abcdef"
	b := make([]byte, 16)
	for i := range b {
		b[i] = hex[(n>>(i%4))&0xf]
	}
	return string(b)
}

func TestRenderSummaryLedgerGroupedTables(t *testing.T) {
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "deadbeef", HTMLBase: "https://github.com/o/r"}
	now := time.Date(2026, 6, 26, 22, 51, 0, 0, time.UTC)
	ledger := []LedgerEntry{
		{FP: "aaaaaaaaaaaaaaaa", Path: "api/db.go", Line: 42, Title: "SQL injection", Category: "security", Status: statusOpen, Sev: "critical", FirstSev: "critical", OpenSHA: "a1b2c3d4", FirstAt: now.Format(time.RFC3339)},
		{FP: "bbbbbbbbbbbbbbbb", Path: "fs/read.go", Line: 12, Title: "Path traversal", Category: "security", Status: statusResolved, Sev: "high", FirstSev: "high", OpenSHA: "a1b2c3d4", ResSHA: "e4f5a6b7", FirstAt: now.Format(time.RFC3339), ResAt: now.Format(time.RFC3339)},
	}
	// InlineURLs set for the OPEN finding → its Location links to the discussion
	// thread (end-to-end: SummaryOptions → renderLedger → ledgerLocation). The
	// resolved finding has no inline URL → blob fallback.
	inlineURLs := map[string]string{"aaaaaaaaaaaaaaaa": "https://github.com/o/r/pull/1#discussion_r123"}
	out := RenderSummaryFull(info, nil, nil, 0, nil, nil, SummaryOptions{Ledger: ledger, Version: "v0.44.0", Walkthrough: "- adds a thing\n- removes another", InlineURLs: inlineURLs})

	for _, want := range []string{
		"**⚠️ Open (1)**",    // bold (not H3), calm warning marker
		"**✅ Resolved (1)**", // resolved table expanded (no <details>)
		"| Priority |",       // column renamed from Sev
		"SQL injection",
		"Path traversal",
		"#discussion_r123",     // open finding Location links to its inline thread
		"Last reviewed commit", // footer commit label
		"/commit/a1b2c3d4",     // linked origin commit
		"/commit/e4f5a6b7",     // linked resolved commit (distinct → "opened → resolved")
		"<!-- " + ledgerPrefix, // embedded ledger marker for the next run
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered ledger summary missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "| Resolved |") {
		t.Fatalf("resolved table header should be just 'Resolved':\n%s", out)
	}
	for _, absent := range []string{
		"### 🔴 Open",          // no oversized H3 / alarming red marker
		"<summary>✅ Resolved", // resolved table is no longer collapsed
		"| Sev |",             // old column header
		"Opened → Resolved",   // resolved column header simplified to "Resolved"
		"→✅",                  // resolved Priority cell is plain severity (no →✅ transition)
		"Review the",          // inline-comment pointer removed in ledger mode
	} {
		if strings.Contains(out, absent) {
			t.Fatalf("rendered ledger summary should NOT contain %q:\n%s", absent, out)
		}
	}
	// The concise "What changed" summary must sit ABOVE the Open tracking table.
	if wc, op := strings.Index(out, "What changed"), strings.Index(out, "⚠️ Open"); wc < 0 || op < 0 || wc > op {
		t.Fatalf("walkthrough must render above the Open table (wc=%d, open=%d):\n%s", wc, op, out)
	}
	// The embedded marker must round-trip back to the same ledger.
	if got := ParseLedger(out); len(got) != 2 {
		t.Fatalf("embedded marker should parse back to 2 entries, got %d", len(got))
	}
}

func TestRenderSummaryLedgerReviewPassed(t *testing.T) {
	// Non-nil but empty ledger (clean review) → "Review passed", not "No findings".
	out := RenderSummaryFull(&PRInfo{HeadSHA: "h"}, nil, nil, 0, nil, nil, SummaryOptions{Ledger: []LedgerEntry{}})
	// A clean review renders the Review passed pill in the same <sub><sub> chip
	// style as the severity chips (consistent + baseline-aligned).
	if !strings.Contains(out, "<sub><sub>![Review passed](https://img.shields.io/badge/Review_passed-brightgreen?style=flat)</sub></sub>") {
		t.Fatalf("clean ledger review should show the Review passed chip in the shields-chip style:\n%s", out)
	}
	if strings.Contains(out, "No_findings") {
		t.Fatalf("ledger mode must not use the legacy No findings badge:\n%s", out)
	}
}

func TestRenderSummaryLedgerOffDiffMarker(t *testing.T) {
	// A finding whose line is outside the reviewed diff can't be an inline comment,
	// so the Open table tags it (off-diff); an on-diff finding is not tagged.
	now := time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)
	findings := []engine.Finding{
		{File: "p.go", Line: 2, Title: "on diff finding", Severity: "high", Category: "bug"},   // added line → on-diff
		{File: "p.go", Line: 99, Title: "off diff finding", Severity: "high", Category: "bug"}, // off-hunk → off-diff
	}
	diffs := sampleDiffs()
	ledger := MergeLedger(nil, findings, "headsha1", map[string]bool{"p.go": true}, now)
	out := RenderSummaryFull(&PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "headsha1"}, findings, map[string]any{"truncation_level": "full"}, 0, nil, nil, SummaryOptions{
		Ledger: ledger,
		Diffs:  diffs,
	})
	if n := strings.Count(out, "(off-diff)"); n != 1 {
		t.Fatalf("want exactly one (off-diff) marker, got %d:\n%s", n, out)
	}
	if !strings.Contains(out, "off diff finding <sub>(off-diff)</sub>") {
		t.Fatalf("the off-diff finding row must carry the marker:\n%s", out)
	}
	if strings.Contains(out, "on diff finding <sub>(off-diff)</sub>") {
		t.Fatalf("the on-diff finding row must NOT carry the marker:\n%s", out)
	}
}

func TestRenderSummaryLedgerNoMarkerWithoutDiff(t *testing.T) {
	// With no diff to compare against, off-diff is undeterminable → no marker.
	now := time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)
	findings := []engine.Finding{{File: "p.go", Line: 99, Title: "lonely", Severity: "high"}}
	ledger := MergeLedger(nil, findings, "h", map[string]bool{"p.go": true}, now)
	out := RenderSummaryFull(&PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"}, findings, map[string]any{"truncation_level": "full"}, 0, nil, nil, SummaryOptions{Ledger: ledger})
	if strings.Contains(out, "(off-diff)") {
		t.Fatalf("no diffs → no off-diff marker:\n%s", out)
	}
}

func TestLedgerResultLineAllClearShowsStats(t *testing.T) {
	// All findings resolved (0 open): the all-clear Result line is ONE combined
	// all-green badge — "Review passed | N resolved" — not two separate chips.
	ledger := []LedgerEntry{
		{FP: "aaaaaaaaaaaaaaaa", Path: "a.go", Status: statusResolved, Sev: "high", FirstSev: "high", OpenSHA: "aaaaaa1", ResSHA: "bbbbbb2"},
	}
	line := ledgerResultLine(ledger)
	want := "<sub><sub>![Review passed 1 resolved](https://img.shields.io/badge/Review_passed_%7C_1_resolved-brightgreen?style=flat)</sub></sub>"
	if line != want {
		t.Fatalf("all-clear Result line should be one combined badge\n got: %q\nwant: %q", line, want)
	}
	// Exactly one badge image, not two separate chips.
	if n := strings.Count(line, "img.shields.io"); n != 1 {
		t.Fatalf("all-clear line should render exactly one badge, got %d in %q", n, line)
	}
	// No code-span pills or trailing emoji — pure chip, like the open-findings line.
	if strings.Contains(line, "`0 open`") || strings.Contains(line, "🎉") {
		t.Fatalf("all-clear line should be a pure chip (no code-span/emoji), got %q", line)
	}
}

func TestLedgerResultLineOpenOmitsCountSuffix(t *testing.T) {
	// With open findings the Result line is just the per-severity chips; the open
	// total is NOT appended (it lives in the "⚠️ Open (N)" table heading).
	ledger := []LedgerEntry{
		{FP: fpStr(1), Status: statusOpen, Sev: "high"},
		{FP: fpStr(2), Status: statusOpen, Sev: "low"},
	}
	line := ledgerResultLine(ledger)
	if strings.Contains(line, "open") {
		t.Fatalf("Result line must not append the open count, got %q", line)
	}
	if !strings.Contains(line, "P1") || !strings.Contains(line, "P3") {
		t.Fatalf("Result line must show per-severity chips, got %q", line)
	}
}

func TestRenderSummaryLegacyUnchangedWithoutLedger(t *testing.T) {
	// Zero SummaryOptions (Ledger nil) must keep the legacy badge + no timestamp.
	out := RenderSummaryFull(&PRInfo{HeadSHA: "h"}, nil, nil, 0, nil, nil, SummaryOptions{})
	if !strings.Contains(out, "No_findings") {
		t.Fatalf("legacy path must keep the No findings badge:\n%s", out)
	}
	if strings.Contains(out, " UTC") {
		t.Fatalf("legacy path must not stamp a timestamp:\n%s", out)
	}
	if strings.Contains(out, ledgerPrefix) {
		t.Fatalf("legacy path must not embed a ledger marker:\n%s", out)
	}
}

func TestMergeLedgerResolvedStaysResolved(t *testing.T) {
	t1 := time.Date(2026, 6, 26, 22, 0, 0, 0, time.UTC)
	paths := map[string]bool{"a.go": true}
	a := mkFinding("a.go", "high", "bug", "boom()", "Crash")

	r1 := MergeLedger(nil, []engine.Finding{a}, "aaaaaa1", paths, t1)
	r2 := MergeLedger(r1, nil, "bbbbbb2", paths, t1.Add(time.Hour))   // resolved at bbbbbb2
	r3 := MergeLedger(r2, nil, "cccccc3", paths, t1.Add(2*time.Hour)) // still absent, path in diff
	r4 := MergeLedger(r3, nil, "dddddd4", paths, t1.Add(3*time.Hour)) // still absent

	e := find(t, r4, a)
	if e.Status != statusResolved {
		t.Fatalf("finding must STAY resolved across runs, got %q", e.Status)
	}
	// ResSHA/ResAt must NOT be re-stamped on later pushes (provenance preserved).
	if e.ResSHA != "bbbbbb2" || e.ResAt != t1.Add(time.Hour).Format(time.RFC3339) {
		t.Fatalf("resolved commit/time must not be re-stamped on later runs: %+v", e)
	}
	if e.OpenSHA != "aaaaaa1" || e.FirstAt != t1.Format(time.RFC3339) {
		t.Fatalf("origin commit/time must be preserved: %+v", e)
	}
}

func TestMergeLedgerUnknownStatusNormalizedAndResolvable(t *testing.T) {
	t1 := time.Date(2026, 6, 26, 22, 0, 0, 0, time.UTC)
	// A tampered / cross-version prior entry with an unknown status, absent this
	// run, must escape limbo and resolve (path still in diff) rather than linger.
	prior := []LedgerEntry{{FP: fpStr(1), Path: "a.go", Status: "weird", Sev: "high", FirstSev: "high", OpenSHA: "aaaaaa1", FirstAt: t1.Format(time.RFC3339)}}
	out := MergeLedger(prior, nil, "bbbbbb2", map[string]bool{"a.go": true}, t1.Add(time.Hour))
	if out[0].Status != statusResolved {
		t.Fatalf("unknown-status entry (absent, path in diff) must resolve, got %q", out[0].Status)
	}

	// An unknown-status entry that IS present this run normalizes to open.
	b := mkFinding("b.go", "low", "style", "x := 1", "B")
	prior2 := []LedgerEntry{{FP: Fingerprint(b), Path: "b.go", Status: "weird", Sev: "low", FirstSev: "low", OpenSHA: "aaaaaa1"}}
	out2 := MergeLedger(prior2, []engine.Finding{b}, "cccccc3", map[string]bool{"b.go": true}, t1)
	if out2[0].Status != statusOpen {
		t.Fatalf("present unknown-status entry must normalize to open, got %q", out2[0].Status)
	}
}

func TestCapLedgerOverCapKeepsAllOpen(t *testing.T) {
	var entries []LedgerEntry
	nOpen := maxLedgerEntries + 5
	for i := 0; i < nOpen; i++ {
		entries = append(entries, LedgerEntry{FP: fpStr(i), Status: statusOpen, Sev: "high"})
	}
	for i := 0; i < 10; i++ {
		entries = append(entries, LedgerEntry{FP: fpStr(10000 + i), Status: statusResolved, ResAt: time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC).Format(time.RFC3339)})
	}
	out := capLedger(entries)
	open, resolved := 0, 0
	for _, e := range out {
		if e.Status == statusResolved {
			resolved++
		} else {
			open++
		}
	}
	if open != nOpen {
		t.Fatalf("every open finding must survive even over the cap, got %d want %d", open, nOpen)
	}
	if resolved != 0 {
		t.Fatalf("budget 0 → all resolved dropped, got %d", resolved)
	}
	if len(out) != nOpen {
		t.Fatalf("over-cap len must equal open count (cap intentionally exceeded), got %d want %d", len(out), nOpen)
	}
}

func TestRenderLedgerResolvedRowCap(t *testing.T) {
	info := &PRInfo{HeadSHA: "deadbeef", HTMLBase: "https://github.com/o/r"}
	now := time.Date(2026, 6, 26, 22, 0, 0, 0, time.UTC)
	ledger := []LedgerEntry{{FP: fpStr(1), Path: "a.go", Title: "still open", Status: statusOpen, Sev: "high", FirstSev: "high", OpenSHA: "aaaaaa1"}}
	for i := 0; i < 30; i++ {
		ledger = append(ledger, LedgerEntry{FP: fpStr(100 + i), Path: fmt.Sprintf("r%d.go", i), Title: fmt.Sprintf("resolved %d", i), Status: statusResolved, Sev: "low", FirstSev: "low", OpenSHA: "aaaaaa1", ResSHA: "bbbbbb2", ResAt: now.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)})
	}
	out := RenderSummaryFull(info, nil, nil, 0, nil, nil, SummaryOptions{Ledger: ledger})

	// The resolved table is capped at maxResolvedRows visible rows. Each resolved
	// row here has distinct open/resolved SHAs, so it carries an "opened → resolved"
	// arrow; the lone open row (no escalation) has none, so the count = shown rows.
	if n := strings.Count(out, " → "); n != maxResolvedRows {
		t.Fatalf("want %d rendered resolved rows, got %d", maxResolvedRows, n)
	}
	if !strings.Contains(out, fmt.Sprintf("_+%d older resolved finding(s) tracked but not shown._", 30-maxResolvedRows)) {
		t.Fatalf("want the +%d older-resolved note:\n%s", 30-maxResolvedRows, out)
	}
	// But the persisted marker still tracks ALL of them.
	if got := ParseLedger(out); len(got) != 31 {
		t.Fatalf("marker should still carry all 31 entries, got %d", len(got))
	}
}

func TestRenderLedgerSameCommitNoArrow(t *testing.T) {
	info := &PRInfo{HeadSHA: "deadbeef", HTMLBase: "https://github.com/o/r"}
	now := time.Date(2026, 6, 26, 22, 0, 0, 0, time.UTC)
	// Opened AND resolved at the same commit (e.g. a same-SHA re-review): the
	// Resolved column shows ONE SHA, no "opened → resolved" arrow.
	ledger := []LedgerEntry{
		{FP: fpStr(1), Path: "a.go", Title: "x", Status: statusResolved, Sev: "low", FirstSev: "low", OpenSHA: "0519d5d", ResSHA: "0519d5d", ResAt: now.Format(time.RFC3339)},
	}
	out := RenderSummaryFull(info, nil, nil, 0, nil, nil, SummaryOptions{Ledger: ledger})
	if strings.Contains(out, " → ") {
		t.Fatalf("same opened/resolved commit must not render a transition arrow:\n%s", out)
	}
	if !strings.Contains(out, "/commit/0519d5d") {
		t.Fatalf("want the single resolved commit link:\n%s", out)
	}

	// A resolved entry with an empty ResSHA (tampered/corrupt ledger) must not
	// render "<openSHA> → —"; with no resolved commit it shows just the dash.
	noRes := RenderSummaryFull(info, nil, nil, 0, nil, nil, SummaryOptions{
		Ledger: []LedgerEntry{{FP: fpStr(2), Path: "a.go", Title: "x", Status: statusResolved, Sev: "low", OpenSHA: "0519d5d", ResSHA: "", ResAt: now.Format(time.RFC3339)}},
	})
	if strings.Contains(noRes, " → ") {
		t.Fatalf("empty ResSHA must not render an arrow to a dash:\n%s", noRes)
	}
}

func TestRenderLedgerReopenPrefix(t *testing.T) {
	info := &PRInfo{HeadSHA: "deadbeef", HTMLBase: "https://github.com/o/r"}
	now := time.Date(2026, 6, 26, 22, 0, 0, 0, time.UTC)
	ledger := []LedgerEntry{
		{FP: fpStr(1), Path: "a.go", Title: "reopened open", Status: statusReopened, Sev: "high", FirstSev: "high", OpenSHA: "aaaaaa1", Reopens: 1},
		{FP: fpStr(2), Path: "b.go", Title: "reopened then fixed", Status: statusResolved, Sev: "low", FirstSev: "low", OpenSHA: "aaaaaa1", ResSHA: "bbbbbb2", ResAt: now.Format(time.RFC3339), Reopens: 1},
	}
	out := RenderSummaryFull(info, nil, nil, 0, nil, nil, SummaryOptions{Ledger: ledger})

	if !strings.Contains(out, "🔁 reopened open") {
		t.Fatalf("a currently-open reopened finding must show the 🔁 prefix:\n%s", out)
	}
	if strings.Contains(out, "🔁 reopened then fixed") {
		t.Fatalf("a resolved finding must NOT show the 🔁 prefix:\n%s", out)
	}
}

func TestLedgerLocation(t *testing.T) {
	info := &PRInfo{HeadSHA: "deadbeef", HTMLBase: "https://github.com/o/r"}
	got := ledgerLocation(info, LedgerEntry{Path: "api/db.go", Line: 42}, nil)
	if !strings.Contains(got, "https://github.com/o/r/blob/deadbeef/api/db.go#L42") || !strings.Contains(got, "`api/db.go:42`") {
		t.Fatalf("want blob permalink + file:line label, got %q", got)
	}
	// When an inline-comment URL exists for the fp, the cell links to the THREAD
	// (not the blob), keeping the file:line label.
	withURL := ledgerLocation(info, LedgerEntry{FP: "abcd", Path: "api/db.go", Line: 42}, map[string]string{"abcd": "https://github.com/o/r/pull/9#discussion_r123"})
	if !strings.Contains(withURL, "(<https://github.com/o/r/pull/9#discussion_r123>)") {
		t.Fatalf("want a link to the inline discussion thread, got %q", withURL)
	}
	if strings.Contains(withURL, "/blob/") {
		t.Fatalf("an inline thread URL must take precedence over the blob link, got %q", withURL)
	}
	if !strings.Contains(withURL, "`api/db.go:42`") {
		t.Fatalf("thread link must keep the file:line label, got %q", withURL)
	}
	// No head SHA → bare code span (blobURL returns "").
	if got := ledgerLocation(&PRInfo{}, LedgerEntry{Path: "a.go", Line: 3}, nil); got != "`a.go:3`" {
		t.Fatalf("fallback must be a bare code span, got %q", got)
	}
	// Tampered path: newline (row breakout), pipe (cell delimiter), backtick and
	// brackets (span/link breakout) must all be neutralized.
	mal := "a.go\n\n![x](http://evil/x.png)\n\n| junk`["
	got = ledgerLocation(&PRInfo{}, LedgerEntry{Path: mal}, nil)
	if strings.Contains(got, "\n") {
		t.Fatalf("newline must be collapsed (no table-row breakout): %q", got)
	}
	if strings.Contains(got, "](") || strings.Contains(got, "[") {
		t.Fatalf("link/bracket breakout must be neutralized: %q", got)
	}
	if !strings.Contains(got, "\\|") {
		t.Fatalf("pipe must be escaped to \\| (cell delimiter): %q", got)
	}
}

func TestShaLinkRejectsNonHexTarget(t *testing.T) {
	info := &PRInfo{HTMLBase: "https://github.com/o/r"}
	if got := shaLink(info, "deadbeef"); !strings.Contains(got, "/commit/deadbeef") {
		t.Fatalf("hex sha should link: %q", got)
	}
	// A tampered, non-hex "sha" must not become a link target.
	if got := shaLink(info, "evil)](javascript:alert(1)"); strings.Contains(got, "javascript") || strings.Contains(got, "/commit/") {
		t.Fatalf("non-hex sha must not produce a link: %q", got)
	}
	// A tampered value whose first 7 chars carry a backtick/pipe must be neutralized
	// in the bare fallback (no raw backtick to close the span, pipe escaped).
	if got := shaLink(info, "a`b|cde0000"); strings.Contains(got, "`b") || !strings.Contains(got, "\\|") {
		t.Fatalf("non-hex sha breakout chars must be neutralized in the bare fallback: %q", got)
	}
	if got := shaLink(info, ""); got != "—" {
		t.Fatalf("empty sha should render an em dash, got %q", got)
	}
}
