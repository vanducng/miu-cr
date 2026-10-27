package github

import (
	"strconv"
	"strings"
	"time"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/diff"
)

// SelectStableFindings drops new findings the commits since the last review did
// not add or delete. A fingerprint already open, reopened, or deferred is kept
// so the same finding can be confirmed. The quote is compared to the diff, so a
// shifted line number cannot admit unchanged code. delta nil means the caller
// could not prove the scope, and every finding is kept.
func SelectStableFindings(findings []engine.Finding, prior []LedgerEntry, delta []diff.Diff) (kept []engine.Finding, dropped int) {
	if delta == nil {
		return findings, 0
	}
	active := make(map[string]bool, len(prior))
	for _, e := range prior {
		switch e.Status {
		case statusOpen, statusReopened, statusDeferred:
			active[e.FP] = true
		}
	}
	kept = make([]engine.Finding, 0, len(findings))
	for _, f := range findings {
		if active[Fingerprint(f)] || findingOnChangedCode(f, delta) {
			kept = append(kept, f)
			continue
		}
		dropped++
	}
	return kept, dropped
}

// StablePaths are files the follow-up commits left unchanged. A nil delta
// returns nil so the caller keeps the legacy resolve-on-absence rule.
func StablePaths(files []string, prior []LedgerEntry, delta []diff.Diff) map[string]bool {
	if delta == nil {
		return nil
	}
	changed := map[string]bool{}
	for i := range delta {
		if p := delta[i].NewPath; p != "" && p != "/dev/null" {
			changed[p] = true
		}
		if p := delta[i].OldPath; p != "" && p != "/dev/null" {
			changed[p] = true
		}
	}
	stable := map[string]bool{}
	for _, p := range files {
		if p != "" && !changed[p] {
			stable[p] = true
		}
	}
	for _, e := range prior {
		if e.Path != "" && !changed[e.Path] {
			stable[e.Path] = true
		}
	}
	return stable
}

// PreserveStableFindings puts back an open, reopened, or deferred row when this
// run omitted it and the file did not change. stable nil or empty keeps merged as-is.
func PreserveStableFindings(merged, prior []LedgerEntry, stable map[string]bool) []LedgerEntry {
	if len(stable) == 0 || len(prior) == 0 {
		return merged
	}
	return restorePreserved(merged, prior, func(e LedgerEntry) bool { return stable[e.Path] })
}

// PreserveUntouchedAnchors puts back an open, reopened, or deferred row when
// this run omitted it and the follow-up commits did not delete that line.
// untouched nil keeps merged as-is. An empty map preserves nothing.
func PreserveUntouchedAnchors(merged, prior []LedgerEntry, untouched map[string]bool) []LedgerEntry {
	if untouched == nil || len(prior) == 0 {
		return merged
	}
	return restorePreserved(merged, prior, func(e LedgerEntry) bool { return untouched[e.FP] })
}

func restorePreserved(merged, prior []LedgerEntry, keep func(LedgerEntry) bool) []LedgerEntry {
	prev := make(map[string]LedgerEntry, len(prior))
	for _, e := range prior {
		prev[e.FP] = e
	}
	out := append([]LedgerEntry(nil), merged...)
	for i := range out {
		p, ok := prev[out[i].FP]
		if !ok || !keep(p) {
			continue
		}
		switch p.Status {
		case statusOpen, statusReopened, statusDeferred:
		default:
			continue
		}
		if out[i].Status != statusResolved || out[i].ResKind != "" {
			continue
		}
		out[i].Status = p.Status
		out[i].ResSHA = p.ResSHA
		out[i].ResKind = p.ResKind
		out[i].ResAt = p.ResAt
		out[i].Note = p.Note
	}
	return out
}

// MergeReviewLedger folds this run into the summary ledger, then keeps a prior
// finding open when the follow-up commits did not delete its line.
func MergeReviewLedger(info *PRInfo, findings []engine.Finding, diffs []diff.Diff, now time.Time) []LedgerEntry {
	if info == nil {
		return nil
	}
	ledger := MergeLedger(info.PriorLedger, findings, info.HeadSHA, diffPaths(diffs), now)
	if len(diffs) > 0 {
		ledger = ApplyOffDiffDisposition(ledger, findings, diffs)
	}
	if info.StableFPs != nil {
		ledger = PreserveUntouchedAnchors(ledger, info.PriorLedger, info.StableFPs)
		ledger = resolveRemovedAnchors(ledger, info.PriorLedger, info.StableFPs, findings, info.HeadSHA, now)
		return applyShiftedLines(ledger, info.StableLines)
	}
	return PreserveStableFindings(ledger, info.PriorLedger, info.StablePaths)
}

// GateFindings is the approval set: this run's blocking findings, plus open
// ledger rows the model did not repeat.
func GateFindings(current []engine.Finding, ledger []LedgerEntry) []engine.Finding {
	blocking := BlockingFindings(current, ledger)
	extra := carriedOpenFindings(ledger, current)
	if len(extra) == 0 {
		return blocking
	}
	out := make([]engine.Finding, 0, len(blocking)+len(extra))
	out = append(out, blocking...)
	out = append(out, extra...)
	return out
}

// CarriedOffDiff are open ledger rows still outside the changed lines and not
// repeated by this run. They stay on the shared thread.
func CarriedOffDiff(ledger []LedgerEntry, current []engine.Finding, diffs []diff.Diff, minSeverity string) []LedgerEntry {
	seen := findingFPs(current)
	var out []LedgerEntry
	for _, e := range ledger {
		if seen[e.FP] || !ledgerBlocksApproval(e.Status) || !severityMeets(e.Sev, minSeverity) {
			continue
		}
		stub := engine.Finding{File: e.Path, Line: e.Line, Severity: e.Sev}
		if ClassifyOffDiff(stub, diffs) == offDiffThread {
			out = append(out, e)
		}
	}
	return out
}

// AppendCarriedOffDiff adds still-open off-diff rows to a thread body, keyed by
// the ledger fingerprint so a reply still matches.
func AppendCarriedOffDiff(body string, entries []LedgerEntry) string {
	if len(entries) == 0 {
		return body
	}
	var b strings.Builder
	if strings.TrimSpace(body) == "" {
		b.WriteString(offDiffMarker + "\n" + botMarker + "\n\n")
		b.WriteString("These findings sit outside the changed lines. ")
		b.WriteString(threadReplyHint)
		b.WriteString("\n\n")
	} else {
		b.WriteString(strings.TrimRight(body, "\n"))
		b.WriteString("\n\n")
	}
	for _, e := range entries {
		loc := e.Path
		if e.Line > 0 {
			loc = loc + ":" + strconv.Itoa(e.Line)
		}
		b.WriteString("- **")
		b.WriteString(severityLabel(e.Sev))
		b.WriteString("** `")
		b.WriteString(mdPathLabel(loc))
		b.WriteString("`")
		if title := mdInline(e.Title); title != "" {
			b.WriteString(" ")
			b.WriteString(title)
		}
		b.WriteString("\n\nStill open from the earlier review.\n\n")
		b.WriteString(fpMarker(e.FP))
		b.WriteString("\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func carriedOpenFindings(ledger []LedgerEntry, current []engine.Finding) []engine.Finding {
	seen := findingFPs(current)
	var out []engine.Finding
	for _, e := range ledger {
		if seen[e.FP] || !ledgerBlocksApproval(e.Status) {
			continue
		}
		out = append(out, engine.Finding{
			File: e.Path, Line: e.Line, Severity: e.Sev, Title: e.Title, Category: e.Category,
		})
	}
	return out
}

func findingFPs(findings []engine.Finding) map[string]bool {
	seen := make(map[string]bool, len(findings))
	for _, f := range findings {
		seen[Fingerprint(f)] = true
	}
	return seen
}

func severityMeets(sev, min string) bool {
	if min == "" || min == "none" {
		return true
	}
	return severityRank(sev) <= severityRank(min)
}

func diffPaths(diffs []diff.Diff) map[string]bool {
	m := make(map[string]bool, len(diffs))
	for i := range diffs {
		if p := diffs[i].NewPath; p != "" && p != "/dev/null" {
			m[p] = true
		}
	}
	return m
}
