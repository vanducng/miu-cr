package github

import (
	"fmt"
	"strings"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/diff"
)

// severityOrder ranks severities high→low for a stable histogram.
var severityOrder = []string{"critical", "high", "medium", "low", "info"}

// priorityBadge maps an internal severity to the display-only emoji + P-level
// badge (Codex/Graphite convention): critical→🔴 P0, high→🟠 P1, medium→🟡 P2,
// low→🔵 P3, info→⚪ P4. An unknown/empty severity falls back to ⚪ P4 so a finding
// never renders a blank badge. DISPLAY ONLY — severity stays the gate/SARIF
// source-of-truth (severityOrder/severityRank are untouched).
// severityMeta is the single source-of-truth severity→(emoji, P-level) map.
// info + any unknown fold to ⚪ P4. priorityBadge + severityEmoji both derive from it.
func severityMeta(sev string) (emoji, plevel string) {
	switch strings.ToLower(strings.TrimSpace(sev)) {
	case "critical":
		return "🔴", "P0"
	case "high":
		return "🟠", "P1"
	case "medium":
		return "🟡", "P2"
	case "low":
		return "🔵", "P3"
	default: // info + unknown
		return "⚪", "P4"
	}
}

func priorityBadge(sev string) string {
	e, p := severityMeta(sev)
	return e + " **" + p + "**"
}

// severityEmoji is the compact (emoji-only) form of priorityBadge for count
// chips: critical→🔴, high→🟠, medium→🟡, low→🔵, info/unknown→⚪.
func severityEmoji(sev string) string {
	e, _ := severityMeta(sev)
	return e
}

// severityCounts renders the emoji-count chips for findings, critical/high first
// (severityOrder), e.g. "🟡 2 · 🔵 1". Unknown severities fold into the info (⚪)
// chip. Returns "" when there are no findings so callers can omit the chip line.
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
			chips = append(chips, fmt.Sprintf("%s %d", severityEmoji(sev), n))
		}
	}
	return strings.Join(chips, " · ")
}

// RenderSummary builds the PR summary that becomes the CreateReview BODY: it leads
// with ReviewMarker (identifies the review as ours for alreadyPostedAtSHA) and a
// Codex-style `Reviewed commit` line, then an emoji-severity header + count, a
// compact metadata quote, confidence, the walkthrough prose, the Important Files
// Changed table, an optional omitted-inline note, and a per-commit footer.
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

	if chips := severityCounts(findings); chips != "" {
		fmt.Fprintf(&b, "## Code Review · %s  (%d finding%s)\n\n", chips, len(findings), plural(len(findings)))
	} else {
		b.WriteString("## Code Review · ✅ no findings\n\n")
	}

	if info.HeadSHA != "" {
		fmt.Fprintf(&b, "Reviewed commit: `%s`\n\n", info.HeadSHA)
	}

	renderMetaQuote(&b, stats, opts.Diffs)
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

	renderPresentation(&b, info, findings, opts.Diffs, opts.ReviewID, opts.FileSummaries)

	fmt.Fprintf(&b, "\n<sub>Reviewed commit `%s` · Posted by miu-cr</sub>", info.HeadSHA)
	return b.String()
}

// plural returns "s" unless n == 1, for "N finding(s)" phrasing.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// renderMetaQuote writes the compact one-line metadata blockquote relocating the
// effort/size/files/churn/context that previously lived in the severity list +
// effort badge: `> <files> files · +<adds>/−<dels> · effort <L> · context <full>`.
// File/churn/effort come from opts.Diffs; with no diffs it falls back to the
// stats files_reviewed count and omits the churn/effort segments.
func renderMetaQuote(b *strings.Builder, stats map[string]any, diffs []diff.Diff) {
	files, adds, dels := diffStats(diffs)
	var seg []string
	if files > 0 {
		seg = append(seg, fmt.Sprintf("%d file%s", files, plural(files)))
		seg = append(seg, fmt.Sprintf("+%d/−%d", adds, dels))
		seg = append(seg, fmt.Sprintf("effort %s", effortSize(files, adds+dels)))
	} else {
		n := statIntVal(stats, "files_reviewed")
		seg = append(seg, fmt.Sprintf("%d file%s", n, plural(n)))
	}
	seg = append(seg, fmt.Sprintf("context %s", truncationLevel(stats)))
	fmt.Fprintf(b, "> %s\n\n", strings.Join(seg, " · "))
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
// Markdown breakout chars. '#' is escaped too — collapsed text placed at the start
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
