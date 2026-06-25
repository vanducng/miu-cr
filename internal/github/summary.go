package github

import (
	"fmt"
	"strings"

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
	// Px label carries the severity color (labelColor); the count is neutral grey.
	return fmt.Sprintf("<sub><sub>![%s %d](https://img.shields.io/badge/%s-%d-lightgrey?labelColor=%s&style=flat)</sub></sub>", p, n, p, n, color)
}

// severityCounts renders the per-level shields count badges for findings, critical/
// high first (severityOrder), e.g. "![P1 2] ![P3 1]". Unknown severities fold into
// the info (P4) chip. Returns "" when there are no findings so callers omit the line.
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

// commitRef renders the head SHA as a short (8-char) linked reference when an HTML base
// is known, else a short code span, so the summary never repeats the full 40-char SHA.
func commitRef(info *PRInfo) string {
	sha := info.HeadSHA
	if sha == "" {
		return ""
	}
	short := sha
	if len(short) > 8 {
		short = short[:8]
	}
	if info.HTMLBase != "" {
		return fmt.Sprintf("[`%s`](<%s/commit/%s>)", short, info.HTMLBase, sha)
	}
	return "`" + short + "`"
}

// commitLabel renders the reviewed commit as a permalink whose text is the commit
// SUBJECT (truncated, escaped) when known, so the cover reads like a changelog line
// instead of a bare hash. Falls back to commitRef (short SHA) when the subject or the
// HTML base is missing. The subject is UNTRUSTED, escaped via mdInline.
func commitLabel(info *PRInfo) string {
	subj := strings.TrimSpace(info.HeadSubject)
	if subj == "" || info.HTMLBase == "" || info.HeadSHA == "" {
		return commitRef(info)
	}
	if r := []rune(subj); len(r) > 72 {
		subj = strings.TrimRight(string(r[:72]), " ") + "…"
	}
	return fmt.Sprintf("[%s](<%s/commit/%s>)", mdInline(subj), info.HTMLBase, info.HeadSHA)
}

// RenderSummary builds the PR summary upserted as the single miucr issue comment:
// it leads with ReviewMarker (the owning sentinel) + the hidden runs token, then a
// `## Code Review` header, the Reviews-(N)/last-commit identity line, the shields
// severity badges, confidence, walkthrough prose, Important Files Changed, the
// collapsed Review internals details, and a footer.
func RenderSummary(info *PRInfo, findings []engine.Finding, stats map[string]any, omittedInline int) string {
	return RenderSummaryWithOverflow(info, findings, stats, omittedInline, nil, nil)
}

// RenderSummaryWithOverflow is RenderSummary plus a collapsible <details> block that
// lists each capped/omitted inline finding (severity, category, file:line, rationale,
// blob permalink) so nothing is silently dropped.
func RenderSummaryWithOverflow(info *PRInfo, findings []engine.Finding, stats map[string]any, omittedInline int, omitted []engine.Finding, categoryURLs map[string]string) string {
	return RenderSummaryFull(info, findings, stats, omittedInline, omitted, categoryURLs, SummaryOptions{})
}

// SummaryOptions bundles the additive reviewer-trust inputs to RenderSummaryFull:
// the same review pass's walkthrough/per-file digest/diagram plus local diff +
// review_id data. Grouping them in a struct keeps the call site readable and makes
// it hard to transpose the same-typed walkthrough/diagram strings or the
// diffs/reviewID pair. The zero value reproduces the legacy summary byte-for-byte.
type SummaryOptions struct {
	Diffs         []diff.Diff
	ReviewID      string
	Walkthrough   string
	FileSummaries map[string]string
	Diagram       string
	// Confidence (1-5) is the model's merge-safety confidence; 0 => derive from findings.
	Confidence       int
	ConfidenceReason string
	// RuleCitations grounds an omitted finding's cited rule stem in the overflow
	// list (validated/linked in the wire layer; an unmatched stem is dropped).
	RuleCitations map[string]RuleCitation
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

	// Keep the H2 small; severity chips ride the compact quote line below, not the header.
	b.WriteString("## Code Review\n\n")

	if info.HeadSHA != "" {
		if info.ReviewCount > 0 {
			fmt.Fprintf(&b, "**Reviews (%d)** · Last reviewed commit: %s\n\n", info.ReviewCount, commitLabel(info))
		} else {
			fmt.Fprintf(&b, "Last reviewed commit: %s\n\n", commitLabel(info))
		}
	}

	lead := severityCounts(findings)
	if lead != "" {
		lead += fmt.Sprintf(" · %d finding%s", len(findings), plural(len(findings)))
	} else {
		lead = "<sub><sub>![no issues found](https://img.shields.io/badge/no_issues_found-brightgreen?style=flat)</sub></sub>"
	}
	fmt.Fprintf(&b, "> %s\n\n", lead)
	renderConfidence(&b, opts.Confidence, opts.ConfidenceReason, findings)

	renderWalkthrough(&b, opts.Walkthrough)
	renderDiagram(&b, opts.Diagram)

	if info.IsFork {
		b.WriteString("> Source: fork (comments posted to the base repo)\n\n")
	}
	if omittedInline > 0 {
		fmt.Fprintf(&b, "> Omitted inline: %d finding%s over the %d-comment limit were not posted inline\n\n", omittedInline, plural(omittedInline), maxInlineComments)
	}

	if len(omitted) > 0 {
		renderOverflow(&b, info, omitted, categoryURLs, opts.RuleCitations)
	}

	renderPresentation(&b, info, findings, opts.Diffs, opts.FileSummaries)

	renderHandoffAndInternals(&b, info, stats, opts.Diffs)

	fmt.Fprintf(&b, "\n<sub>Reviewed commit %s · Posted by miu-cr</sub>", commitRef(info))
	return b.String()
}

// plural returns "s" unless n == 1, for "N finding(s)" phrasing.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// renderHandoffAndInternals writes ONE collapsed <details> combining the agent
// handoff (how to re-run / pick up the review) and the review metadata (Files, Churn,
// Effort, Context) with a one-line meaning each, so the block is a single secondary
// section. review_id is NOT shown: it only resolves on the machine + store that ran
// the review, so it is meaningless in a posted comment (it stays in the JSON envelope).
// All values are trusted (ints, the PR URL, fixed effort/context enums).
func renderHandoffAndInternals(b *strings.Builder, info *PRInfo, stats map[string]any, diffs []diff.Diff) {
	b.WriteString("\n<details>\n<summary>Agent handoff & review internals</summary>\n\n")

	// Handoff only makes sense for a real PR target (a re-run URL); the legacy non-PR
	// body has neither a URL nor diffs, so it skips the handoff and keeps just internals.
	if url := prURL(info); url != "" || len(diffs) > 0 {
		b.WriteString("**Hand off to an agent** · pick up or re-run this review\n\n")
		if url != "" {
			fmt.Fprintf(b, "- Run locally: `miucr review --pr %s`\n", strings.ReplaceAll(url, "`", "'"))
		} else {
			b.WriteString("- Run locally: `miucr review --pr <pr-url>`\n")
		}
		b.WriteString("- MCP: call `review_run` from an agent host (add `-o json` for a machine-readable envelope).\n\n")
	}

	b.WriteString("**Review internals** · how miucr sized this review\n\n")
	files, adds, dels := diffStats(diffs)
	if files == 0 {
		files = statIntVal(stats, "files_reviewed")
	}
	fmt.Fprintf(b, "- **Files** `%d` · files changed in this diff\n", files)
	if adds > 0 || dels > 0 {
		fmt.Fprintf(b, "- **Churn** `+%d / −%d` · lines added / removed\n", adds, dels)
		fmt.Fprintf(b, "- **Effort** %s · estimated review size from files + churn\n", effortBadge(effortSize(files, adds+dels)))
	}
	lvl := truncationLevel(stats)
	fmt.Fprintf(b, "- **Context** %s · %s\n", contextBadge(lvl), contextNote(lvl))
	b.WriteString("\n</details>\n")
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

// contextBadge renders the diff-context level (full vs truncated) as a shields badge:
// green when the model saw the whole diff, yellow when it was truncated.
func contextBadge(level string) string {
	color := "yellow"
	if level == "full" {
		color = "brightgreen"
	}
	return fmt.Sprintf("<sub><sub>![%s](https://img.shields.io/badge/context-%s-%s?style=flat)</sub></sub>", level, level, color)
}

// contextNote explains the context level in one line.
func contextNote(level string) string {
	if level == "full" {
		return "the model saw the complete diff"
	}
	return "the diff was truncated to fit the model context"
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

// renderOverflow appends a collapsible block listing the omitted inline findings.
func renderOverflow(b *strings.Builder, info *PRInfo, omitted []engine.Finding, categoryURLs map[string]string, cites map[string]RuleCitation) {
	fmt.Fprintf(b, "\n<details>\n<summary>Omitted inline findings (%d)</summary>\n\n", len(omitted))
	for _, f := range omitted {
		sev := strings.ToUpper(strings.TrimSpace(f.Severity))
		if sev == "" {
			sev = "NOTE"
		}
		// Neutralize the chars that could break the `code-span` or the [link text](url):
		// a backtick closes the span, brackets break the link text.
		file := strings.NewReplacer("`", "'", "[", "(", "]", ")").Replace(f.File)
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
func mdInline(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.NewReplacer(
		"<", "&lt;", ">", "&gt;", "`", "\\`", "[", "\\[", "]", "\\]", "*", "\\*", "_", "\\_", "|", "\\|", "#", "\\#",
	).Replace(s)
}

// mdProse escapes the structural breakout vectors in untrusted model prose (a
// finding's rationale): the HTML/comment vectors (< >, neutralizing </details>,
// the <!-- miucr:fp --> sentinel, <script>) AND backticks (so a ``` fence in the
// rationale can't open a code block that swallows the suggestion/patch fence
// rendered right after it). It does NOT collapse whitespace or escape brackets/
// links, so prose stays readable. GitHub renders &lt;/&gt; back to </>.
func mdProse(s string) string {
	return strings.NewReplacer("<", "&lt;", ">", "&gt;", "`", "\\`").Replace(s)
}

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
