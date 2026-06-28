package github

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine"
)

// Finding lifecycle states carried in the comment-embedded ledger. "open" and
// "reopened" are both currently-flagged (reopened = was resolved, came back);
// "resolved" means a later commit dropped it from the diff'd findings.
const (
	statusOpen     = "open"
	statusResolved = "resolved"
	statusReopened = "reopened"
)

// maxLedgerEntries bounds the tracked set so the hidden marker + rendered tables
// stay well under GitHub's 65536-byte comment limit; over-cap drops the OLDEST
// resolved findings first (open findings are never dropped). maxResolvedRows
// bounds only the rendered Resolved table (the marker still carries them up to
// the entry cap), so the visible comment never sprawls.
const (
	maxLedgerEntries = 100
	maxResolvedRows  = 25
)

// LedgerEntry is one finding's lifecycle, keyed by the line-independent
// github.Fingerprint so the SAME finding is recognized across commits. It is
// persisted as a base64(JSON) blob inside the upserted summary comment — the
// storeless source of truth, robust in ephemeral CI where a local DB resets.
// JSON tags are terse to keep the marker compact.
type LedgerEntry struct {
	FP       string `json:"f"`            // fingerprint (16 hex)
	Path     string `json:"p"`            // last-known file path
	Line     int    `json:"l,omitempty"`  // last-known line
	Title    string `json:"t,omitempty"`  // short title (untrusted model text)
	Category string `json:"c,omitempty"`  // category (untrusted model text)
	Status   string `json:"s"`            // open | resolved | reopened
	Sev      string `json:"v"`            // current/last severity
	FirstSev string `json:"fv"`           // severity when first opened (the "before")
	OpenSHA  string `json:"os"`           // origin commit (head when first opened)
	ResSHA   string `json:"rs,omitempty"` // head when resolved
	FirstAt  string `json:"fa"`           // RFC3339 first opened
	ResAt    string `json:"ra,omitempty"` // RFC3339 resolved
	Reopens  int    `json:"ro,omitempty"` // times resolved-then-reappeared
}

const ledgerPrefix = "miu-cr-ledger:"

// ledgerMarkerRe extracts the base64 ledger payload from the summary body. The
// payload is base64(JSON) so untrusted title/category text can never break out
// of the HTML comment (no '<', '>', or '-->' in the base64 alphabet). Built from
// ledgerPrefix so the parser can't silently desync from renderLedgerMarker.
var ledgerMarkerRe = regexp.MustCompile("<!-- " + regexp.QuoteMeta(ledgerPrefix) + `([A-Za-z0-9+/=]*) -->`)

// renderLedgerMarker encodes the ledger as the hidden comment line embedded in
// the summary body; "" (no marker) on a marshal error so a render never fails.
func renderLedgerMarker(entries []LedgerEntry) string {
	data, err := json.Marshal(entries)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("<!-- %s%s -->", ledgerPrefix, base64.StdEncoding.EncodeToString(data))
}

// ParseLedger reads the prior ledger out of a summary comment body, returning
// nil when no (or a corrupt) marker is present so the caller starts fresh.
func ParseLedger(body string) []LedgerEntry {
	m := ledgerMarkerRe.FindStringSubmatch(body)
	if m == nil {
		return nil
	}
	data, err := base64.StdEncoding.DecodeString(m[1])
	if err != nil {
		return nil
	}
	var out []LedgerEntry
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return out
}

// MergeLedger folds this run's findings into the prior ledger and returns the
// new state: unseen findings open (origin = headSHA, FirstSev recorded), a
// recurring resolved finding reopens, and a prior open/reopened finding absent
// from this run is resolved (head = headSHA) ONLY when its file is still in the
// diff — absence off-diff is not a fix (mirrors trackResolution). Severity is
// refreshed each run so FirstSev→Sev captures escalation. now stamps timestamps.
func MergeLedger(prior []LedgerEntry, current []engine.Finding, headSHA string, diffPaths map[string]bool, now time.Time) []LedgerEntry {
	nowStr := now.UTC().Format(time.RFC3339)
	out := make([]LedgerEntry, len(prior))
	copy(out, prior)
	idx := make(map[string]int, len(out))
	for i := range out {
		idx[out[i].FP] = i
	}

	seen := make(map[string]bool, len(current))
	for _, f := range current {
		fp := Fingerprint(f)
		seen[fp] = true
		sev := severityLabel(f.Severity)
		if i, ok := idx[fp]; ok {
			e := &out[i]
			switch e.Status {
			case statusResolved:
				e.Status = statusReopened
				e.Reopens++
				e.ResSHA = ""
				e.ResAt = ""
			case statusReopened, statusOpen:
				// already current; keep status
			default:
				e.Status = statusOpen // normalize "" / unknown (e.g. tampered or cross-version) status
			}
			e.Sev = sev
			e.Path = f.File
			e.Line = f.Line
			if t := strings.TrimSpace(f.Title); t != "" {
				e.Title = t
			}
			if c := strings.TrimSpace(f.Category); c != "" {
				e.Category = c
			}
			continue
		}
		idx[fp] = len(out)
		out = append(out, LedgerEntry{
			FP: fp, Path: f.File, Line: f.Line,
			Title: strings.TrimSpace(f.Title), Category: strings.TrimSpace(f.Category),
			Status: statusOpen, Sev: sev, FirstSev: sev,
			OpenSHA: headSHA, FirstAt: nowStr,
		})
	}

	for i := range out {
		e := &out[i]
		if seen[e.FP] {
			continue
		}
		// Any not-yet-resolved finding (open/reopened, or a normalized unknown
		// status) absent this run resolves ONLY when its file is still in the diff
		// — absence off-diff is not a fix (mirrors trackResolution); such a finding
		// lingers as open until its file is touched again.
		if e.Status != statusResolved && diffPaths[e.Path] {
			e.Status = statusResolved
			e.ResSHA = headSHA
			e.ResAt = nowStr
		}
	}
	return capLedger(out)
}

// capLedger keeps the ledger under maxLedgerEntries by dropping the OLDEST
// resolved findings (by ResAt) while NEVER dropping an open/reopened one (so an
// open count over the cap intentionally exceeds it). Original order is
// preserved. Keep decisions are tracked by slice INDEX, not fingerprint, so a
// duplicate-FP entry from a tampered/corrupt parsed comment can't collapse two
// rows into one keep/drop. Logs the exact drop count so a silent cap can't read
// as "tracked everything".
func capLedger(entries []LedgerEntry) []LedgerEntry {
	if len(entries) <= maxLedgerEntries {
		return entries
	}
	type resPos struct {
		idx   int
		resAt string
	}
	var resolved []resPos
	for i, e := range entries {
		if e.Status == statusResolved {
			resolved = append(resolved, resPos{i, e.ResAt})
		}
	}
	budget := maxLedgerEntries - (len(entries) - len(resolved)) // open = total - resolved
	if budget < 0 {
		budget = 0
	}
	if len(resolved) <= budget {
		return entries
	}
	sort.SliceStable(resolved, func(i, j int) bool { return resolved[i].resAt > resolved[j].resAt })
	keep := make(map[int]bool, budget)
	for _, r := range resolved[:budget] {
		keep[r.idx] = true
	}
	dropped := len(resolved) - budget
	out := make([]LedgerEntry, 0, len(entries)-dropped)
	for i, e := range entries {
		if e.Status != statusResolved || keep[i] {
			out = append(out, e)
		}
	}
	os.Stderr.WriteString(config.RedactString(fmt.Sprintf("miucr: finding ledger at cap (%d), dropped %d oldest resolved finding(s) from tracking", maxLedgerEntries, dropped)) + "\n")
	return out
}

// greenChip renders a small green shields pill in the same <sub><sub> style as
// severityCountBadge, so the all-clear Result line aligns with the severity
// chips. text is internal (no user input → no escaping); spaces map to the
// shields underscore separator.
func greenChip(text string) string {
	return fmt.Sprintf("<sub><sub>![%s](https://img.shields.io/badge/%s-brightgreen?style=flat)</sub></sub>",
		text, strings.ReplaceAll(text, " ", "_"))
}

// greenResultBadge renders one all-green shields pill "label | msg". The bar is a
// single segment (not shields' label/message split, which shows no divider when
// both halves are green). Escaping mirrors shields' own conventions: a literal "-"
// doubles to "--" (else shields reads it as the message/color delimiter), and the
// "|" becomes %7C so it stays valid in the URL and safe inside a table cell.
func greenResultBadge(label, msg string) string {
	enc := strings.ReplaceAll(label+" | "+msg, " ", "_")
	enc = strings.ReplaceAll(enc, "-", "--")
	enc = strings.ReplaceAll(enc, "|", "%7C")
	return fmt.Sprintf("<sub><sub>![%s %s](https://img.shields.io/badge/%s-brightgreen?style=flat)</sub></sub>",
		label, msg, enc)
}

// ledgerResultLine builds the **Result:** lead for ledger mode: open-severity
// count chips when findings are open, else one combined all-green "Review passed
// | N resolved" badge (or just "Review passed" when nothing was resolved) in the
// SAME <sub><sub> shields-chip style as the severity chips, so the all-clear line
// is visually consistent and baseline-aligned.
func ledgerResultLine(entries []LedgerEntry) string {
	counts := map[string]int{}
	open, resolved := 0, 0
	for _, e := range entries {
		if e.Status == statusResolved {
			resolved++
			continue
		}
		open++
		counts[severityLabel(e.Sev)]++
	}

	if open == 0 {
		if resolved > 0 {
			return greenResultBadge("Review passed", fmt.Sprintf("%d resolved", resolved))
		}
		return greenChip("Review passed")
	}
	// Just the per-severity chips. The open total is NOT appended — it already
	// shows in the "⚠️ Open (N)" tracking-table heading below.
	var chips []string
	for _, sev := range severityOrder {
		if n := counts[sev]; n > 0 {
			chips = append(chips, severityCountBadge(sev, n))
		}
	}
	return strings.Join(chips, " ")
}

// ledgerResultPlain is the badge-free Result line for minimal formats: a plain
// open-finding count (or "all clear"), mirroring ledgerResultLine's open tally.
func ledgerResultPlain(entries []LedgerEntry) string {
	open := 0
	for _, e := range entries {
		if e.Status != statusResolved {
			open++
		}
	}
	if open == 0 {
		return "all clear"
	}
	return fmt.Sprintf("%d open finding%s", open, plural(open))
}

// renderLedger writes the grouped lifecycle tables. Both are ALWAYS VISIBLE
// (not collapsed) — the tracking history is the point of the section. Section
// labels are bold (not H3) so the marker emoji stays normal-sized; ⚠️ flags Open
// (attention, not alarm) and ✅ flags Resolved. Untrusted title/path text is
// escaped; commit SHAs link to their commit page. inlineURLs (fp -> inline
// comment URL) links the Location cell to the review thread when one exists.
func renderLedger(b *strings.Builder, info *PRInfo, entries []LedgerEntry, inlineURLs map[string]string, offDiff map[string]bool) {
	var open, resolved []LedgerEntry
	for _, e := range entries {
		if e.Status == statusResolved {
			resolved = append(resolved, e)
		} else {
			open = append(open, e)
		}
	}

	if len(open) > 0 {
		sortLedgerBySeverity(open)
		fmt.Fprintf(b, "**⚠️ Open (%d)**\n\n", len(open))
		b.WriteString("| Priority | Issue | Location | Opened |\n|----------|-------|----------|--------|\n")
		for _, e := range open {
			fmt.Fprintf(b, "| %s | %s | %s | %s |\n", ledgerSevCell(e, false), ledgerIssue(e, offDiff[e.FP]), ledgerLocation(info, e, inlineURLs), shaLink(info, e.OpenSHA))
		}
		b.WriteString("\n")
	}

	if len(resolved) > 0 {
		sort.SliceStable(resolved, func(i, j int) bool { return resolved[i].ResAt > resolved[j].ResAt })
		fmt.Fprintf(b, "**✅ Resolved (%d)**\n\n", len(resolved))
		b.WriteString("| Priority | Issue | Location | Resolved |\n|----------|-------|----------|----------|\n")
		shown, extra := resolved, 0
		if len(shown) > maxResolvedRows {
			extra = len(shown) - maxResolvedRows
			shown = shown[:maxResolvedRows]
		}
		for _, e := range shown {
			// Show "opened → resolved" only when they are DIFFERENT commits (a real
			// cross-commit fix); when a finding opened and resolved at the same commit
			// (e.g. a re-review of the same SHA) the transition is noise — show one SHA.
			resolvedCell := shaLink(info, e.ResSHA)
			if e.OpenSHA != "" && e.ResSHA != "" && e.OpenSHA != e.ResSHA {
				resolvedCell = shaLink(info, e.OpenSHA) + " → " + resolvedCell
			}
			fmt.Fprintf(b, "| %s | %s | %s | %s |\n", ledgerSevCell(e, true), ledgerIssue(e, false), ledgerLocation(info, e, inlineURLs), resolvedCell)
		}
		if extra > 0 {
			fmt.Fprintf(b, "\n_+%d older resolved finding(s) tracked but not shown._\n", extra)
		}
		b.WriteString("\n")
	}
}

// ledgerSevCell renders the Priority cell. Resolved rows show the plain
// severity (the ✅ Resolved table heading already conveys resolution, so no
// →✅). An OPEN finding that escalated shows <first>→<current>; otherwise just
// the current severity. P-level reflects the latest severity.
func ledgerSevCell(e LedgerEntry, resolved bool) string {
	emoji, p, _ := severityMeta(e.Sev)
	if !resolved && e.FirstSev != "" && !strings.EqualFold(e.FirstSev, e.Sev) {
		first, _, _ := severityMeta(e.FirstSev)
		return fmt.Sprintf("%s→%s %s", first, emoji, p)
	}
	return fmt.Sprintf("%s %s", emoji, p)
}

// ledgerIssue is the escaped Issue cell: title (or category fallback), prefixed
// 🔁 when the finding was reopened, suffixed (off-diff) when the finding's line
// falls outside the reviewed diff (so GitHub can't carry it as an inline comment
// and it lives only in this table). The suffix is static text — no escaping.
func ledgerIssue(e LedgerEntry, offDiff bool) string {
	t := mdInline(e.Title)
	if t == "" {
		t = mdInline(e.Category)
	}
	if t == "" {
		t = "—"
	}
	if e.Reopens > 0 && e.Status != statusResolved {
		t = "🔁 " + t
	}
	if offDiff {
		t += " <sub>(off-diff)</sub>"
	}
	return t
}

// ledgerLocation renders the `file:line` label, linked to the inline review
// THREAD when one exists for this finding (inlineURLs[fp], the #discussion_r…
// anchor) — a direct jump to the discussion is more useful than the raw file.
// It falls back to the head-blob permalink, then a plain code span. The path is
// UNTRUSTED (round-trips through the editable comment) so it is neutralized via
// mdPathLabel; the inline URL is GitHub-server-assigned (trusted), angle-bracketed.
func ledgerLocation(info *PRInfo, e LedgerEntry, inlineURLs map[string]string) string {
	label := mdPathLabel(e.Path)
	if e.Line > 0 {
		label = fmt.Sprintf("%s:%d", label, e.Line)
	}
	if u := inlineURLs[e.FP]; u != "" {
		return fmt.Sprintf("[`%s`](<%s>)", label, u)
	}
	if u := blobURL(info, e.Path, e.Line, 0); u != "" {
		return fmt.Sprintf("[`%s`](%s)", label, u)
	}
	return "`" + label + "`"
}

// mdPathLabel neutralizes an untrusted file path for a code-span / link-label
// table cell: collapse ALL whitespace (so a smuggled newline can't terminate the
// GFM table row and escape the cell), neutralize the code-span/link breakout
// chars (backtick, brackets), and escape a pipe (the table-cell delimiter). The
// blobURL link TARGET is separately url.PathEscape'd per segment in blobURL.
func mdPathLabel(p string) string {
	p = strings.Join(strings.Fields(p), " ")
	return strings.NewReplacer("`", "'", "[", "(", "]", ")", "|", "\\|").Replace(p)
}

// shaLink renders a 7-char commit SHA linked to its commit page; "—" for an
// empty SHA, a bare code span when no HTML base or the value isn't a hex SHA
// (defense against a tampered ledger injecting a link target).
func shaLink(info *PRInfo, sha string) string {
	if sha == "" {
		return "—"
	}
	short := sha
	if len(short) > 7 {
		short = short[:7]
	}
	if info != nil && info.HTMLBase != "" && isHexSHA(sha) {
		return fmt.Sprintf("[`%s`](<%s/commit/%s>)", short, strings.TrimRight(info.HTMLBase, "/"), sha)
	}
	// Bare fallback: a non-hex SHA only arises from a tampered ledger; neutralize
	// it (backtick/pipe) so it can't break the code span or split the table cell.
	return "`" + mdPathLabel(short) + "`"
}

func isHexSHA(s string) bool {
	if len(s) < 7 || len(s) > 40 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func sortLedgerBySeverity(entries []LedgerEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		ri, rj := severityRank(entries[i].Sev), severityRank(entries[j].Sev)
		if ri != rj {
			return ri < rj
		}
		return entries[i].Path < entries[j].Path
	})
}
