package github

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/diff"
)

// severityOrder ranks severities high→low for a stable histogram.
var severityOrder = []string{"critical", "high", "medium", "low", "info"}

// severityMeta is the single source-of-truth severity→(emoji, P-level, shields color)
// map: critical→P0/red, high→P1/orange, medium→P2/yellow, low→P3/blue, info+unknown→
// P4/grey. DISPLAY ONLY: severity stays the gate/SARIF source (severityOrder/
// severityRank untouched). priorityBadge + severityCountBadge derive from it.
func severityMeta(sev string) (emoji, plevel, color string) {
	switch strings.ToLower(strings.TrimSpace(sev)) {
	case "critical":
		return "🔴", "P0", "red"
	case "high":
		return "🟠", "P1", "orange"
	case "medium":
		return "🟡", "P2", "yellow"
	case "low":
		return "🔵", "P3", "blue"
	default: // info + unknown
		return "⚪", "P4", "lightgrey"
	}
}

// priorityBadge renders the inline-finding P-level as a small shields.io badge
// (Codex-style), degrading to the alt text if shields is unreachable. P-level +
// color are fixed internal constants (no user input → no escaping needed).
func priorityBadge(sev string) string {
	_, p, color := severityMeta(sev)
	return fmt.Sprintf("<sub><sub>![%s](https://img.shields.io/badge/%s-%s?style=flat)</sub></sub>", p, p, color)
}

// severityCountBadge renders a small shields.io "P1 | N" count badge for the
// summary chips (same style as the inline priorityBadge, with the count as message).
func severityCountBadge(sev string, n int) string {
	_, p, color := severityMeta(sev)
	label := severityLabel(sev)
	// Px label carries the severity color (labelColor); the count is neutral grey.
	return fmt.Sprintf("<sub><sub>![%s | %s | %d](https://img.shields.io/badge/%s-%s-lightgrey?labelColor=%s&style=flat)</sub></sub>",
		p, label, n, url.PathEscape(fmt.Sprintf("%s | %s", p, label)), fmt.Sprintf("%d", n), color)
}

// severityCounts renders the per-level shields count badges for findings, critical/
// high first (severityOrder). Unknown severities fold into the info (P4) chip.
// Returns "" when there are no findings so callers omit the line.
func severityCounts(findings []engine.Finding) string {
	counts := map[string]int{}
	for _, f := range findings {
		sev := strings.ToLower(strings.TrimSpace(f.Severity))
		if !known(sev) {
			sev = "info"
		}
		counts[sev]++
	}
	var chips []string
	for _, sev := range severityOrder {
		if n := counts[sev]; n > 0 {
			chips = append(chips, severityCountBadge(sev, n))
		}
	}
	return strings.Join(chips, " ")
}

// offDiffSet returns the fingerprints of this-run findings that fall OUTSIDE the
// inline-eligible diff set (per mode) — GitHub can't carry them as inline
// comments, so they live only in the ledger's Open table, which tags them
// (off-diff). Returns nil when there's no diff to compare against (every render
// path with a real post carries the diffs).
func offDiffSet(findings []engine.Finding, diffs []diff.Diff, mode FilterMode) map[string]bool {
	if len(findings) == 0 || len(diffs) == 0 {
		return nil
	}
	eligible := make(map[string]bool)
	for _, f := range inlineEligible(findings, diffs, mode) {
		eligible[Fingerprint(f)] = true
	}
	off := make(map[string]bool)
	for _, f := range findings {
		if fp := Fingerprint(f); !eligible[fp] {
			off[fp] = true
		}
	}
	return off
}

// commitRef renders the head SHA as a short (7-char) linked reference when an HTML base
// is known, else a short code span, so the summary never repeats the full 40-char SHA.
func commitRef(info *PRInfo) string {
	if info == nil {
		return ""
	}
	sha := info.HeadSHA
	if sha == "" {
		return ""
	}
	short := sha
	if len(short) > 7 {
		short = short[:7]
	}
	if info.HTMLBase != "" {
		return fmt.Sprintf("[`%s`](<%s/commit/%s>)", short, info.HTMLBase, sha)
	}
	return "`" + short + "`"
}

const (
	summaryStatusStart = "<!-- miu-cr-status:start -->"
	summaryStatusEnd   = "<!-- miu-cr-status:end -->"
)

// RenderSummary builds the PR summary upserted as the single miucr issue comment:
// it leads with ReviewMarker (the owning sentinel) + the hidden runs token, then a
// `## Code Review Summary` header, the inline `**Result:**` severity-chips line,
// walkthrough prose, Important Files Changed, the collapsed Review reference
// details, and a footer whose `Review attempts: N` carries the relocated count.
func RenderSummary(info *PRInfo, findings []engine.Finding, stats map[string]any, omittedInline int) string {
	return RenderSummaryWithOverflow(info, findings, stats, omittedInline, nil, nil)
}

// RenderSummaryWithOverflow is RenderSummary plus a collapsible <details> block that
// lists each capped/omitted inline finding (severity, category, file:line, rationale,
// blob permalink) so nothing is silently dropped.
func RenderSummaryWithOverflow(info *PRInfo, findings []engine.Finding, stats map[string]any, omittedInline int, omitted []engine.Finding, categoryURLs map[string]string) string {
	return RenderSummaryFull(info, findings, stats, omittedInline, omitted, categoryURLs, SummaryOptions{})
}

func RenderRunningSummary(info *PRInfo, version string) string {
	var b strings.Builder
	count := 1
	if info != nil {
		count = max(info.ReviewCount, 1)
	}
	b.WriteString(ReviewMarker + "\n")
	b.WriteString(runsCountToken(count) + "\n")
	b.WriteString("## Code Review Summary\n\n")
	b.WriteString("**Result:** Review running. I will update this comment with the result shortly.\n")
	if v := strings.TrimSpace(version); v != "" {
		fmt.Fprintf(&b, "\n<sub>Posted by [miu-cr](https://github.com/vanducng/miu-cr) [%s](https://github.com/vanducng/miu-cr/releases/tag/%s)</sub>", mdInline(v), url.PathEscape(v))
	}
	if info != nil && info.PriorLedger != nil {
		b.WriteString("\n" + renderLedgerMarker(info.PriorLedger))
	}
	return b.String()
}

func RenderQueuedSummary(info *PRInfo, availableAt time.Time, debounce time.Duration, version string) string {
	var b strings.Builder
	count := 1
	if info != nil {
		count = max(info.ReviewCount, 1)
	}
	b.WriteString(ReviewMarker + "\n")
	b.WriteString(runsCountToken(count) + "\n")
	b.WriteString("## Code Review Summary\n\n")
	fmt.Fprintf(&b, "**Result:** %s\n", queuedSummaryText(info, availableAt, debounce))
	if v := strings.TrimSpace(version); v != "" {
		fmt.Fprintf(&b, "\n<sub>Posted by [miu-cr](https://github.com/vanducng/miu-cr) [%s](https://github.com/vanducng/miu-cr/releases/tag/%s)</sub>", mdInline(v), url.PathEscape(v))
	}
	if info != nil && info.PriorLedger != nil {
		b.WriteString("\n" + renderLedgerMarker(info.PriorLedger))
	}
	return b.String()
}

func RenderReviewingSummaryStatus(info *PRInfo) string {
	commit := commitRef(info)
	if commit == "" {
		return renderSummaryStatus("**Status:** Reviewing now. The previous result remains visible below until this review finishes.")
	}
	return renderSummaryStatus(fmt.Sprintf("**Status:** Reviewing commit %s. The previous result remains visible below until this review finishes.", commit))
}

func RenderQueuedSummaryStatus(info *PRInfo, availableAt time.Time, debounce time.Duration) string {
	return renderSummaryStatus("**Status:** " + queuedSummaryText(info, availableAt, debounce) + " Previous result remains visible below.")
}

func queuedSummaryText(info *PRInfo, availableAt time.Time, debounce time.Duration) string {
	parts := []string{"Review queued"}
	if commit := commitRef(info); commit != "" {
		parts[0] += " for commit " + commit
	}
	if !availableAt.IsZero() {
		parts = append(parts, "starts after "+availableAt.UTC().Format("2006-01-02 15:04 UTC"))
	} else if debounce > 0 {
		parts = append(parts, "waiting for "+debounce.String()+" debounce")
	}
	return strings.Join(parts, ". ") + "."
}

func renderSummaryStatus(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	return summaryStatusStart + "\n" + line + "\n" + summaryStatusEnd
}

func withSummaryStatus(body, status string) string {
	body = stripSummaryStatus(body)
	status = strings.TrimSpace(status)
	if strings.TrimSpace(body) == "" || status == "" {
		return body
	}
	insert := status + "\n\n"
	const heading = "## Code Review Summary"
	if idx := strings.Index(body, heading); idx >= 0 {
		pos := idx + len(heading)
		for pos < len(body) && (body[pos] == ' ' || body[pos] == '\t' || body[pos] == '\r' || body[pos] == '\n') {
			pos++
		}
		return body[:pos] + insert + body[pos:]
	}
	pos := summaryStatusFallbackPos(body)
	return body[:pos] + insert + body[pos:]
}

func stripSummaryStatus(body string) string {
	for {
		start := strings.Index(body, summaryStatusStart)
		if start < 0 {
			return body
		}
		endRel := strings.Index(body[start:], summaryStatusEnd)
		if endRel < 0 {
			return body
		}
		end := start + endRel + len(summaryStatusEnd)
		removeStart := start
		for removeStart > 0 && body[removeStart-1] == '\n' {
			removeStart--
		}
		removeEnd := end
		for removeEnd < len(body) && body[removeEnd] == '\n' {
			removeEnd++
		}
		repl := ""
		if removeStart > 0 && removeEnd < len(body) {
			repl = "\n\n"
		}
		body = body[:removeStart] + repl + body[removeEnd:]
	}
}

func summaryStatusFallbackPos(body string) int {
	offset := 0
	for offset < len(body) {
		next := strings.IndexByte(body[offset:], '\n')
		if next < 0 {
			line := strings.TrimSpace(body[offset:])
			if strings.HasPrefix(line, "<!--") && strings.HasSuffix(line, "-->") {
				return len(body)
			}
			return offset
		}
		lineEnd := offset + next
		line := strings.TrimSpace(body[offset:lineEnd])
		if line == "" || (strings.HasPrefix(line, "<!--") && strings.HasSuffix(line, "-->")) {
			offset = lineEnd + 1
			continue
		}
		return offset
	}
	return len(body)
}

// SummaryOptions bundles the additive reviewer-trust inputs to RenderSummaryFull:
// the same review pass's walkthrough/per-file digest/diagram plus local diff +
// review_id data. Grouping them in a struct keeps the call site readable and makes
// it hard to transpose the same-typed walkthrough/diagram strings or the
// diffs/reviewID pair. The zero value reproduces the legacy summary byte-for-byte.
type SummaryOptions struct {
	Diffs []diff.Diff
	// FilterMode is the inline-eligibility filter used for posting; the ledger
	// Open table marks a finding (off-diff) when it falls outside this set (so it
	// could not be carried as an inline comment). Empty = diff_context.
	FilterMode    FilterMode
	ReviewID      string
	Walkthrough   string
	FileSummaries map[string]string
	Diagram       string
	// Version is the miucr release tag appended to the footer "Posted by" line
	// when non-empty (sourced from the cli version var via the wire layer).
	Version string
	// RuleCitations grounds an omitted finding's cited rule stem in the overflow
	// list (validated/linked in the wire layer; an unmatched stem is dropped).
	RuleCitations map[string]RuleCitation
	// Ledger, when non-nil, switches the summary to lifecycle mode: the Result
	// line and grouped Open/Resolved tables render from the merged finding
	// ledger (per-finding open/resolved/reopened, origin+resolved commit,
	// severity before→after) and a hidden base64 ledger marker is embedded for
	// the next run to read. Nil (the zero value) preserves the legacy
	// current-run-only rendering byte-for-byte.
	Ledger []LedgerEntry
	// InlineURLs maps a finding fingerprint to its inline-comment HTML URL (the
	// #discussion_r… thread anchor). When set, the ledger Location cell links to
	// the inline review thread instead of the file blob. nil → blob links.
	InlineURLs map[string]string
	Published  bool
	PublishKey string
	// Format selects the presentation preset (see modes.go). Empty = "full" =
	// the legacy byte-for-byte summary; "minimal" drops the heading, walkthrough,
	// result badges, changes table, and review reference.
	Format string
	// SuppressWalkthrough drops the "What changed" walkthrough block; FileChangeSummary
	// opts INTO the "Important Files Changed" table. Both AND with the format preset:
	// minimal already has them off. Zero-value safe — the walkthrough defaults on, the
	// file table defaults off (= [review].code_summary defaults).
	SuppressWalkthrough bool
	FileChangeSummary   bool
	ApprovalReason      string
}

func renderApprovalBlocker(b *strings.Builder, reason string) {
	switch reason {
	case approveReasonMergeConflict:
		b.WriteString("> **Approval:** Resolve the merge conflicts, then update the pull request.\n\n")
	case approveReasonChecksNotGreen:
		b.WriteString("> **Approval:** Waiting for CI checks to finish successfully. Fix any failures, then update the pull request.\n\n")
	case approveReasonReadinessUnverified:
		b.WriteString("> **Approval:** GitHub readiness could not be verified. Check mergeability and CI, then rerun the review.\n\n")
	}
}

// RenderSummaryFull is RenderSummaryWithOverflow plus the LLM-free reviewer-trust
// blocks (walkthrough, effort badge, per-file changes table, agent-handoff)
// derived from the same review pass (opts.Walkthrough/FileSummaries) + local data
// (opts.Diffs, opts.ReviewID). Every block is additive markdown and degrades cleanly
// (empty walkthrough/diffs/reviewID skip their block), so the summary stays
// idempotent + back-compatible; no extra model call is made. Untrusted model
// text (walkthrough/fileSummaries) is escaped via mdInline at render.
func RenderSummaryFull(info *PRInfo, findings []engine.Finding, stats map[string]any, omittedInline int, omitted []engine.Finding, categoryURLs map[string]string, opts SummaryOptions) string {
	var b strings.Builder

	b.WriteString(ReviewMarker + "\n")
	// Hidden runs counter for the next upsert. ReviewCount is already this run's number
	// (FetchPR did the +1), so write it straight back; max(,1) only guards a direct
	// render with an unset ReviewCount (no FetchPR), which still seeds N=1.
	b.WriteString(runsCountToken(max(info.ReviewCount, 1)) + "\n")
	if opts.Published && strings.TrimSpace(info.HeadSHA) != "" {
		b.WriteString(publishedToken(info.HeadSHA, opts.PublishKey) + "\n")
	}

	p := presentationFor(opts.Format)

	// Keep the H2 small; severity chips ride the compact Result line below, not the header.
	if p.Heading {
		b.WriteString("## Code Review Summary\n\n")
	}

	if opts.Ledger != nil {
		// Lifecycle mode: Result → a concise PR summary → the always-visible
		// Open/Resolved tracking tables. No inline-comment pointer (GitHub already
		// surfaces the inline review thread below).
		result := ledgerResultLine(opts.Ledger, info.ReviewCount, info.HeadSHA, reviewChangeSizeOf(info, opts.Diffs))
		if !p.ResultBadges {
			result = ledgerResultPlain(opts.Ledger)
		}
		fmt.Fprintf(&b, "**Result:** %s\n\n", result)
		renderApprovalBlocker(&b, opts.ApprovalReason)
		if p.Walkthrough && !opts.SuppressWalkthrough {
			renderWalkthrough(&b, opts.Walkthrough)
		}
		renderLedger(&b, info, opts.Ledger, opts.InlineURLs, offDiffSet(findings, opts.Diffs, opts.FilterMode))
	} else {
		var lead string
		if p.ResultBadges {
			lead = severityCounts(findings)
			if lead != "" {
				lead += fmt.Sprintf(" · %d finding%s", len(findings), plural(len(findings)))
			} else {
				lead = "<sub><sub>![No findings](https://img.shields.io/badge/No_findings-brightgreen?style=flat)</sub></sub>"
			}
		} else if len(findings) == 0 {
			lead = "No findings"
		} else {
			lead = fmt.Sprintf("%d finding%s", len(findings), plural(len(findings)))
		}
		fmt.Fprintf(&b, "**Result:** %s\n\n", lead)
		renderApprovalBlocker(&b, opts.ApprovalReason)
		if posted := len(findings) - omittedInline; posted > 0 {
			b.WriteString(fmt.Sprintf("→ Review the %d inline comment%s below.", posted, plural(posted)))
			b.WriteString("\n\n")
		}
		if p.Walkthrough && !opts.SuppressWalkthrough {
			renderWalkthrough(&b, opts.Walkthrough)
		}
	}
	if p.Diagram {
		renderDiagram(&b, opts.Diagram)
	}

	if info.IsFork {
		b.WriteString("> Source: fork (comments posted to the base repo)\n\n")
	}
	if omittedInline > 0 {
		fmt.Fprintf(&b, "> Omitted inline: %d finding%s over the %d-comment limit were not posted inline\n\n", omittedInline, plural(omittedInline), maxInlineComments)
	}

	if len(omitted) > 0 {
		renderOverflow(&b, info, omitted, categoryURLs, opts.RuleCitations)
	}

	if p.ChangesTable && opts.FileChangeSummary {
		renderPresentation(&b, info, findings, opts.Diffs, opts.FileSummaries)
	}

	if p.ReviewRef {
		renderReviewReference(&b, info, stats, opts.Diffs)
	}

	if p.Footer {
		handoff := ""
		if info.ReviewCount > 0 {
			handoff = fmt.Sprintf(" · Review attempts: %d", info.ReviewCount)
		}
		ver := ""
		if v := strings.TrimSpace(opts.Version); v != "" {
			ver = fmt.Sprintf(" [%s](https://github.com/vanducng/miu-cr/releases/tag/%s)", mdInline(v), url.PathEscape(v))
		}
		fmt.Fprintf(&b, "\n<sub>Last reviewed commit %s%s · Posted by [miu-cr](https://github.com/vanducng/miu-cr)%s</sub>", commitRef(info), handoff, ver)
	} else if sha := strings.TrimSpace(info.HeadSHA); sha != "" {
		// Footer-off formats drop the visible sub-line but keep the reviewed head in
		// a hidden marker; reviewedCommitRe matches "Reviewed commit <sha>" inside it.
		fmt.Fprintf(&b, "\n<!-- Reviewed commit %s -->", sha)
	}
	if opts.Ledger != nil {
		b.WriteString("\n" + renderLedgerMarker(opts.Ledger))
	}
	return b.String()
}

// plural returns "s" unless n == 1, for "N finding(s)" phrasing.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// renderReviewReference writes ONE collapsed <details> combining the agent
// handoff (how to re-run / pick up the review) and the review metadata (Files, Churn,
// Effort, Context) with a one-line meaning each, so the block is a single secondary
// section. review_id is NOT shown: it only resolves on the machine + store that ran
// the review, so it is meaningless in a posted comment (it stays in the JSON envelope).
// All values are trusted (ints, the PR URL, fixed effort/context enums).
func renderReviewReference(b *strings.Builder, info *PRInfo, stats map[string]any, diffs []diff.Diff) {
	b.WriteString("\n<details>\n<summary>Review reference</summary>\n\n")

	// Handoff only makes sense with a real re-run URL; without one (the legacy non-PR
	// body) skip it and keep just the internals. A bare `<pr-url>` placeholder is not
	// actionable, so it is never emitted.
	if url := prURL(info); url != "" {
		b.WriteString("**Run again**\n\n")
		fmt.Fprintf(b, "- Run locally: `miucr review --pr %s -o pretty`\n\n", strings.ReplaceAll(url, "`", "'"))
	}

	// Priority legend: explains the P0–P4 column with a badge + short meaning.
	b.WriteString("**Priority**\n\n")
	renderPriorityLegend(b)

	// Review context: badge + short note only (no leading bold label, no Files/Churn).
	b.WriteString("\n**Review context**\n\n")
	files, adds, dels := diffStats(diffs)
	if files == 0 {
		files = statIntVal(stats, "files_reviewed")
	}
	if adds > 0 || dels > 0 {
		fmt.Fprintf(b, "- %s · estimated review size\n", effortBadge(effortSize(files, adds+dels)))
	}
	lvl := truncationLevel(stats)
	fmt.Fprintf(b, "- %s · %s\n", contextBadge(lvl), contextNote(lvl))
	b.WriteString("\n</details>\n")
}

// renderPriorityLegend lists the P0–P4 priority levels (critical→info), each a
// shields badge + a short, direct meaning for a code-review finding.
func renderPriorityLegend(b *strings.Builder) {
	meaning := map[string]string{
		"critical": "immediate blocker: security, data loss, outage, or auth bypass",
		"high":     "fix before merge: major breakage or no safe workaround",
		"medium":   "should fix soon: real defect with limited impact or workaround",
		"low":      "can wait: minor defect, edge case, or maintainability risk",
		"info":     "optional FYI: non-blocking suggestion or observation",
	}
	for _, sev := range severityOrder { // critical, high, medium, low, info → P0..P4
		fmt.Fprintf(b, "- %s · %s\n", priorityBadge(sev), meaning[sev])
	}
}

// effortBadge renders the S/M/L/XL effort bucket as a color-graded shields badge
// (green to red). The bucket is a fixed enum, safe in the badge URL.
func effortBadge(size string) string {
	color := map[string]string{"S": "brightgreen", "M": "blue", "L": "orange", "XL": "red"}[size]
	if color == "" {
		color = "lightgrey"
	}
	return fmt.Sprintf("<sub><sub>![%s](https://img.shields.io/badge/effort-%s-%s?style=flat)</sub></sub>", size, size, color)
}

func contextBadge(level string) string {
	color := "yellow"
	switch level {
	case "full":
		color = "brightgreen"
	case "filenames_only":
		color = "orange"
	}
	return fmt.Sprintf("<sub><sub>![%s](https://img.shields.io/badge/context-%s-%s?style=flat)</sub></sub>", level, level, color)
}

// contextNote explains the context level in one line.
func contextNote(level string) string {
	switch level {
	case "full":
		return "the model saw the complete diff"
	case "hunks_only", "hunks":
		return "the model saw changed diff hunks; expanded surrounding code was dropped to fit context"
	case "filenames_only":
		return "the model saw changed filenames only; diff content did not fit context"
	default:
		return "context level reported by the review run"
	}
}

func severityLabel(sev string) string {
	s := strings.ToLower(strings.TrimSpace(sev))
	if known(s) {
		return s
	}
	return "info"
}

// diffStats sums the changed-file count and total insertions/deletions, skipping
// deleted files (NewPath empty or /dev/null).
func diffStats(diffs []diff.Diff) (files int, adds, dels int64) {
	for i := range diffs {
		if p := diffs[i].NewPath; p == "" || p == "/dev/null" {
			continue
		}
		files++
		adds += diffs[i].Insertions
		dels += diffs[i].Deletions
	}
	return files, adds, dels
}

func reviewChangeSizeOf(info *PRInfo, diffs []diff.Diff) reviewChangeSize {
	files, adds, dels := diffStats(diffs)
	if files > 0 || adds+dels > 0 {
		return reviewChangeSize{files: files, churn: adds + dels}
	}
	if info == nil {
		return reviewChangeSize{}
	}
	files = info.ChangedFiles
	if files == 0 {
		files = len(info.Files)
	}
	return reviewChangeSize{files: files, churn: info.Additions + info.Deletions}
}

// renderOverflow appends a collapsible block listing the omitted inline findings.
func renderOverflow(b *strings.Builder, info *PRInfo, omitted []engine.Finding, categoryURLs map[string]string, cites map[string]RuleCitation) {
	fmt.Fprintf(b, "\n<details>\n<summary>Omitted inline findings (%d)</summary>\n\n", len(omitted))
	for _, f := range omitted {
		sev := strings.ToUpper(strings.TrimSpace(f.Severity))
		if sev == "" {
			sev = "NOTE"
		}
		// Neutralize the chars that could break the `code-span` or the [link text](url)
		// AND collapse whitespace so a newline can't terminate the row (mdPathLabel).
		file := mdPathLabel(f.File)
		loc := fmt.Sprintf("`%s:%d`", file, f.Line)
		if url := blobURL(info, f.File, f.Line, f.EndLine); url != "" {
			loc = fmt.Sprintf("[`%s:%d`](%s)", file, f.Line, url)
		}
		cat := ""
		if c := mdInline(f.Category); c != "" {
			cat = " (" + categoryMarkdownText(f.Category, c, categoryURLs) + ")"
		}
		title := ""
		if t := mdInline(f.Title); t != "" {
			title = " **" + t + "**"
		}
		fmt.Fprintf(b, "- **%s**%s %s —%s %s%s\n", mdInline(sev), cat, loc, title, mdInline(f.Rationale), ruleCitation(info, f.Rule, cites))
	}
	b.WriteString("\n</details>\n")
}

// categoryMarkdown renders a finding Category for a Markdown context. When
// categoryURLs holds a validated URL for the lowercased category, it returns a
// Markdown link `[text](<url>)` with the link TEXT escaped via mdInline (the
// category is untrusted model text); the URL is from Trusted, scheme-validated
// config. With no match it returns plainText unchanged so the default render is
// byte-for-byte preserved. plainText is what the caller renders today (raw in
// the inline comment, mdInline'd in the summary), passed in to keep each site's
// existing escaping intact on the unmapped path.
func categoryMarkdown(cat string, categoryURLs map[string]string) string {
	return categoryMarkdownText(cat, cat, categoryURLs)
}

// categoryMarkdownText is categoryMarkdown with an explicit plain fallback so the
// summary site can pass its already-mdInline'd category while the link text is
// freshly escaped from the raw category.
func categoryMarkdownText(cat, plainText string, categoryURLs map[string]string) string {
	if url, ok := categoryURLs[strings.ToLower(strings.TrimSpace(cat))]; ok && url != "" {
		// Angle-bracket destination so a ')' or paren in the URL can't close the link
		// early; the validator already strips whitespace/'<'/'>'/backslash/control chars.
		return "[" + mdInline(cat) + "](<" + url + ">)"
	}
	return plainText
}

// RuleCitation is the wire-validated grounding for one rule stem the model cited:
// whether it matched a loaded rule and, for repo (RepoUntrusted) rules only, the
// repo-relative path to link. The wire layer NEVER sets RepoRelPath for a user
// rule (absolute home path → privacy leak) or a built-in (defaults/* → not a repo
// file); those stay Linkable=false and are cited as text only.
type RuleCitation struct {
	RepoRelPath string
	Linkable    bool
}

// ruleCitation renders the trailing "(per <stem>)" grounding for a finding. The
// stem is matched against the wire-validated cites map (built from the LOADED,
// fork-dropped rule set); a stem absent from the map is DROPPED entirely
// (anti-hallucination/injection). A matched stem is cited as mdInline-escaped
// text; a Linkable (repo) stem additionally links to its repo-relative blob URL.
// Returns "" when there is nothing to cite, so callers append it unconditionally.
func ruleCitation(info *PRInfo, ruleStem string, cites map[string]RuleCitation) string {
	stem := strings.TrimSpace(ruleStem)
	if stem == "" || cites == nil {
		return ""
	}
	cite, ok := cites[stem]
	if !ok {
		return ""
	}
	label := mdInline(stem)
	if cite.Linkable {
		if url := blobURL(info, cite.RepoRelPath, 0, 0); url != "" {
			return fmt.Sprintf(" (per [%s](<%s>))", label, url)
		}
	}
	return fmt.Sprintf(" (per %s)", label)
}

// mdInline neutralizes untrusted model text (rationale/category) for a Markdown
// list item: collapse newlines to one line, HTML-escape <> (so a rationale can't
// inject markup or break out of the <details> block), and backslash-escape the
// Markdown breakout chars. '#' is escaped too: collapsed text placed at the start
// of a line (e.g. after "## Walkthrough\n\n") would otherwise inject a heading.
// RenderError renders the summary-comment body for a review that FAILED before
// producing findings (provider/network error after miucr's internal retries, bad
// key). It carries ReviewMarker + the runs token so UpsertSummaryComment edits the
// SAME comment in place — a later successful run replaces this error with findings,
// and re-runs don't stack. msg is untrusted (provider-originated), escaped via
// mdInline; the caller redacts secrets first.
func RenderError(info *PRInfo, msg, version string) string {
	return RenderErrorNotice(info, ErrorNotice{
		Level:   "warning",
		Title:   "miucr could not complete the review",
		Message: msg,
	}, version)
}

// ErrorNotice is the operator-facing failure rendered into the PR summary.
type ErrorNotice struct {
	Level   string
	Title   string
	Code    string
	Message string
	Hint    string
}

// RenderErrorNotice renders a GitHub alert summary for a failed review run.
func RenderErrorNotice(info *PRInfo, notice ErrorNotice, version string) string {
	var b strings.Builder
	b.WriteString(ReviewMarker + "\n")
	b.WriteString(runsCountToken(max(info.ReviewCount, 1)) + "\n")
	b.WriteString("## Code Review Summary\n\n")

	level := errorNoticeLevel(notice.Level)
	title := strings.TrimSpace(notice.Title)
	if title == "" {
		if level == "CAUTION" {
			title = "miucr hit an internal error"
		} else {
			title = "miucr could not complete the review"
		}
	}
	b.WriteString("> [!" + level + "]\n")
	b.WriteString("> **" + mdInline(title) + "**\n")
	if code := mdInline(notice.Code); code != "" {
		b.WriteString(">\n> Code: " + code + "\n")
	}
	if m := mdInline(notice.Message); m != "" {
		b.WriteString(">\n> " + m + "\n")
	}
	if h := mdInline(notice.Hint); h != "" {
		b.WriteString(">\n> Hint: " + h + "\n")
	}
	b.WriteString("\n")
	if level == "CAUTION" {
		b.WriteString("This looks like an internal miu-cr failure. Re-run after the issue is fixed; a later successful review replaces this notice with findings.")
	} else {
		b.WriteString("This is an operational review issue, not a problem with your changes. Re-run the job to retry; a later successful review replaces this notice with findings.")
	}
	if strings.TrimSpace(version) != "" {
		b.WriteString("\n\n<sub>miucr " + mdInline(version) + "</sub>")
	}
	return b.String()
}

func errorNoticeLevel(level string) string {
	switch strings.ToUpper(strings.TrimSpace(level)) {
	case "CAUTION", "ERROR":
		return "CAUTION"
	default:
		return "WARNING"
	}
}

func mdInline(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.NewReplacer(
		"<", "&lt;", ">", "&gt;", "`", "\\`", "[", "\\[", "]", "\\]", "*", "\\*", "_", "\\_", "|", "\\|", "#", "\\#",
	).Replace(s)
}

// mdProse neutralizes the structural breakout vectors in untrusted model prose (a
// finding's rationale, a walkthrough) while keeping it readable and scannable:
//   - HTML/comment vectors (< >, so </details>, the <!-- miucr:fp --> sentinel, and
//     <script> can't break out) are escaped.
//   - Runs of 3+ backticks (``` fences) are DEFUSED backtick-by-backtick so they
//     can't open a code block that swallows a suggestion/patch fence or the tables
//     rendered after the prose.
//   - Single/double backticks are PRESERVED so the model's `inline code` (columns,
//     tables, functions, paths) renders as monospace instead of literal backticks.
//
// A stray unbalanced inline backtick cannot leak into a trailing patch fence: the
// fence sits on its own line after a blank line (a CommonMark paragraph boundary),
// where an unclosed inline span ends and renders as a literal backtick. Whitespace
// and brackets/links are left intact. GitHub renders &lt;/&gt; back to </>.
func mdProse(s string) string {
	s = strings.NewReplacer("<", "&lt;", ">", "&gt;").Replace(s)
	return fenceRe.ReplaceAllStringFunc(s, func(run string) string {
		return strings.Repeat("\\`", len(run))
	})
}

// fenceRe matches a run of 3+ backticks (a fenced code-block delimiter).
var fenceRe = regexp.MustCompile("`{3,}")

func known(sev string) bool {
	for _, s := range severityOrder {
		if s == sev {
			return true
		}
	}
	return false
}

// severityRank orders a severity high→low for top-N inline selection; unknown sorts last.
func severityRank(sev string) int {
	s := strings.ToLower(strings.TrimSpace(sev))
	for i, x := range severityOrder {
		if x == s {
			return i
		}
	}
	return len(severityOrder)
}

func truncationLevel(stats map[string]any) string {
	if stats == nil {
		return "full"
	}
	if v, ok := stats["truncation_level"]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return "full"
}

func statInt(stats map[string]any, key string) string {
	if stats == nil {
		return "0"
	}
	switch v := stats[key].(type) {
	case float64:
		return fmt.Sprintf("%d", int(v))
	case int:
		return fmt.Sprintf("%d", v)
	case nil:
		return "0"
	default:
		return fmt.Sprintf("%v", v)
	}
}

// statIntVal is the int form of statInt (for arithmetic/pluralization).
func statIntVal(stats map[string]any, key string) int {
	if stats == nil {
		return 0
	}
	switch v := stats[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}
