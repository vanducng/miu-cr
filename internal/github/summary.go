package github

import (
	"fmt"
	"strings"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/diff"
)

// severityOrder ranks severities high→low for a stable histogram.
var severityOrder = []string{"critical", "high", "medium", "low", "info"}

// RenderSummary builds the PR summary body WITHOUT the sentinel — UpsertSummaryComment
// owns prepending SummarySentinel as the first line, so the body must not repeat it.
// It emits a severity histogram, truncation level, head SHA, files reviewed, an optional
// omitted-inline note, and a short footer.
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
	b.WriteString("## Code Review\n\n")

	renderWalkthrough(&b, opts.Walkthrough)
	renderDiagram(&b, opts.Diagram)

	counts := map[string]int{}
	for _, f := range findings {
		sev := strings.ToLower(strings.TrimSpace(f.Severity))
		if sev == "" {
			sev = "info"
		}
		counts[sev]++
	}

	if len(findings) == 0 {
		b.WriteString("No findings.\n\n")
	} else {
		fmt.Fprintf(&b, "**%d finding(s):**\n\n", len(findings))
		for _, sev := range severityOrder {
			if n := counts[sev]; n > 0 {
				fmt.Fprintf(&b, "- %s: %d\n", sev, n)
			}
		}
		for sev, n := range counts {
			if !known(sev) {
				fmt.Fprintf(&b, "- %s: %d\n", sev, n)
			}
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "- Head: `%s`\n", info.HeadSHA)
	fmt.Fprintf(&b, "- Files reviewed: %s\n", statInt(stats, "files_reviewed"))
	fmt.Fprintf(&b, "- Context: %s\n", truncationLevel(stats))
	if info.IsFork {
		b.WriteString("- Source: fork (comments posted to the base repo)\n")
	}
	if omittedInline > 0 {
		fmt.Fprintf(&b, "- Omitted inline: %d finding(s) over the %d-comment limit were not posted inline\n", omittedInline, maxInlineComments)
	}

	if len(omitted) > 0 {
		renderOverflow(&b, info, omitted, categoryURLs)
	}

	renderPresentation(&b, info, findings, opts.Diffs, opts.ReviewID, opts.FileSummaries)

	b.WriteString("\n<sub>Posted by miu-cr. Re-runs edit this summary and skip already-posted inline comments.</sub>")
	return b.String()
}

// renderOverflow appends a collapsible block listing the omitted inline findings.
func renderOverflow(b *strings.Builder, info *PRInfo, omitted []engine.Finding, categoryURLs map[string]string) {
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
		fmt.Fprintf(b, "- **%s**%s %s — %s\n", mdInline(sev), cat, loc, mdInline(f.Rationale))
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
