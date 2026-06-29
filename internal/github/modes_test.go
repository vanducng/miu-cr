package github

import (
	"reflect"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine"
)

func TestModesRegistry(t *testing.T) {
	if !ValidFormat("") || !ValidFormat("full") || !ValidFormat("minimal") {
		t.Fatal("full/minimal/empty must be valid formats")
	}
	if ValidFormat("bogus") {
		t.Fatal("unknown format must be invalid")
	}
	if got := ModeNames(); len(got) != 2 || got[0] != "full" || got[1] != "minimal" {
		t.Fatalf("ModeNames() = %v, want [full minimal] sorted", got)
	}
	// full must set EVERY presentation flag — guards a newly-added section block
	// silently missing from the full preset.
	full := reflect.ValueOf(presentationFor("full"))
	for i := 0; i < full.NumField(); i++ {
		if !full.Field(i).Bool() {
			t.Fatalf("full preset leaves %s unset; every section must be on in full", full.Type().Field(i).Name)
		}
	}
	// minimal must clear every flag.
	if presentationFor("minimal") != (presentation{}) {
		t.Fatal("minimal preset must clear every section flag")
	}
	// unknown/empty fall back to full.
	if presentationFor("") != presentationFor("full") || presentationFor("bogus") != presentationFor("full") {
		t.Fatal("empty/unknown format must resolve to full")
	}
}

func TestRenderSummaryMinimalStripsChrome(t *testing.T) {
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "deadbeef", ReviewCount: 2}
	findings := []engine.Finding{{Severity: "high", Rationale: "x"}}
	out := RenderSummaryFull(info, findings, map[string]any{"truncation_level": "full"}, 0, nil, nil, SummaryOptions{
		Walkthrough: "this is the walkthrough lead prose",
		Format:      "minimal",
	})
	// Dropped chrome — incl. the visible footer sub-line.
	for _, banned := range []string{
		"img.shields.io",
		"## Code Review Summary",
		"this is the walkthrough lead prose",
		"<summary>Review reference</summary>",
		"<summary>Important Files Changed",
		"<sub>Last reviewed commit",
	} {
		if strings.Contains(out, banned) {
			t.Fatalf("minimal output must not contain %q:\n%s", banned, out)
		}
	}
	// Kept essentials: upsert markers, plain result, inline pointer, and the HIDDEN
	// reviewed-commit marker (so the resolution-sync still finds the head).
	for _, required := range []string{
		ReviewMarker,
		"<!-- miu-cr-runs:",
		"**Result:** 1 finding",
		"→ Review the 1 inline comment below.",
		"<!-- Reviewed commit deadbeef -->",
	} {
		if !strings.Contains(out, required) {
			t.Fatalf("minimal output must contain %q:\n%s", required, out)
		}
	}
	// The hidden marker must remain parseable by reviewedCommitRe.
	if got := parseReviewedCommit(out); got != "deadbeef" {
		t.Fatalf("parseReviewedCommit on minimal output = %q, want deadbeef", got)
	}
}

func TestRenderSummaryMinimalLedgerNoBadges(t *testing.T) {
	// The host/lifecycle path (the deployment that renders Open/Resolved tables)
	// must also strip every shields badge under minimal — ledgerResultPlain swaps
	// the severity-count chips and renderLedger emits emoji+P# cells only.
	info := &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "deadbeef", ReviewCount: 3}
	ledger := []LedgerEntry{
		{FP: "a1b2c3d4e5f60718", Path: "src/x.go", Line: 4, Title: "leak", Category: "bug", Status: statusOpen, Sev: "high", FirstSev: "high", OpenSHA: "deadbeef", FirstAt: "2026-06-28T00:00:00Z"},
		{FP: "00112233445566aa", Path: "src/y.go", Line: 9, Title: "fixed it", Category: "bug", Status: statusResolved, Sev: "low", FirstSev: "low", OpenSHA: "cafe", ResSHA: "deadbeef", FirstAt: "2026-06-27T00:00:00Z", ResAt: "2026-06-28T00:00:00Z"},
	}
	out := RenderSummaryFull(info, nil, map[string]any{"truncation_level": "full"}, 0, nil, nil, SummaryOptions{
		Ledger: ledger,
		Format: "minimal",
	})
	if strings.Contains(out, "img.shields.io") {
		t.Fatalf("minimal ledger output must not contain any shields badge:\n%s", out)
	}
	if strings.Contains(out, "## Code Review Summary") {
		t.Fatalf("minimal ledger output must drop the summary heading:\n%s", out)
	}
	// The tracking tables themselves stay (history is the point); markers stay.
	if !strings.Contains(out, ReviewMarker) || !strings.Contains(out, "1 open finding") {
		t.Fatalf("minimal ledger output must keep marker + plain open count:\n%s", out)
	}
}

func TestCommentBodyMinimalDropsPriorityBadge(t *testing.T) {
	f := engine.Finding{Severity: "high", Category: "bug", Title: "boom", Rationale: "why"}
	full, _ := commentBody(nil, f, "", PostReviewOptions{}, false)
	if !strings.Contains(full, "img.shields.io") {
		t.Fatalf("full inline comment should carry the priority badge:\n%s", full)
	}
	min, _ := commentBody(nil, f, "", PostReviewOptions{Format: "minimal"}, false)
	if strings.Contains(min, "img.shields.io") {
		t.Fatalf("minimal inline comment must not carry the priority badge:\n%s", min)
	}
	if !strings.Contains(min, "bug") || !strings.Contains(min, "**boom**") {
		t.Fatalf("minimal inline comment must keep category + title:\n%s", min)
	}
}
