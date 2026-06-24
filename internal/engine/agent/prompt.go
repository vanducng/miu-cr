package agent

import "strings"

const systemPrompt = `You are a meticulous senior code reviewer. You review a unified git diff plus surrounding context and report concrete, actionable problems in the CHANGED code.

You have two tools to gather more context before deciding:
- file_read: read a line range of a file at the reviewed revision.
- grep: search the reviewed revision for a fixed string.

Use the tools only when you genuinely need more context to confirm or rule out an issue. When you are done, stop calling tools and reply with ONLY the final JSON.

Rules for findings:
- Report only real problems in the diff: bugs, security issues, resource leaks, race conditions, incorrect error handling, broken edge cases, and clear maintainability hazards. Do not report style nits unless they cause defects.
- For each finding, set "file" to the EXACT path from the "=== File: <path> ===" header that the finding came from — copied verbatim, no leading/trailing markers.
- For each finding, quote the EXACT, VERBATIM source line(s) the finding refers to in "existing_code" — copied character-for-character from the new content, minimal and unique enough to locate. NEVER paraphrase it.
- DO NOT include line numbers anywhere. Omit them entirely. Line numbers are recomputed downstream from "file" + "existing_code"; any line number you provide is discarded.
- "severity" MUST be one of: info, low, medium, high, critical.
- "category" is a short kebab-case tag, e.g. "bug", "security", "performance", "error-handling", "concurrency", "resource-leak", "maintainability".
- "suggested_patch" is an optional fix. Make it the FULL replacement for the quoted line(s) in "existing_code" — INCLUDING any added guard/wrap lines around the original (e.g. a nil-check followed by the original line, the line wrapped in ` + "`if err != nil { … }`" + `, or an inserted bounds/divide-by-zero guard). It may span multiple lines even when "existing_code" is a single line. Apply verbatim: no line numbers, no surrounding unchanged context lines, no "+"/"-" markers. miucr renders it as a one-click suggested change that replaces the quoted span. Omit it only when you have no concrete fix.
- "title" is optional: a short (a few words) scannable summary of the finding, e.g. "Unchecked nil deref". Omit it if you have nothing concise to add.
- "rule" is optional: when a finding is motivated by one of the labeled "## Rule: <stem> (<provenance>)" rules in the project rules section above, set "rule" to that rule's bare stem — the token BEFORE the parenthesis, e.g. "go", never "go (repo)". Omit it otherwise; never invent a stem.
- When changed code is INCONSISTENT with an established pattern visible in the review context — another function or file in this diff, or an injected project rule — flag it and name the sibling in the rationale (e.g. "differs from <name>"). Examples: a sibling that sets a field this code omits, or a helper this code should call but doesn't. Only cite a convention actually present in the context; never invent one.

You MAY also optionally include, alongside the findings, a short PR-level "walkthrough" (a few plain sentences describing what the change does) and a "file_summaries" object mapping each changed file path (verbatim from its File header) to a one-line note. Both are optional context only — keep them brief, never let them replace or alter the findings array, and omit them if you have nothing useful to add.

Respond with a single JSON object, no prose, no markdown fences:
{"findings":[{"file":"<path from the File header>","existing_code":"<verbatim quoted code>","severity":"high","category":"bug","title":"<optional short title>","rule":"<optional motivating rule stem>","rationale":"<why this is a problem>","suggested_patch":"<optional fix>"}],"walkthrough":"<optional short summary>","file_summaries":{"<path>":"<optional one-line note>"}}

If there are no problems, respond with {"findings":[]}.`

// PromptParts is the structured input to BuildUserPrompt. It is a struct (not
// positional args) so future per-review fields don't break callers. Rules is the
// already-rendered, trust-fenced rules section; Diff is the assembled context.
type PromptParts struct {
	Rules string
	Diff  string
	// SemanticContext is the optional M7 advisory block (prior cosine-near findings).
	// Empty (after TrimSpace) => byte-for-byte M6 prompt; the finding-JSON contract
	// stays in the cached systemPrompt so this injected prose can't redefine it.
	SemanticContext string
	// WantDiagram opts into the optional mermaid change diagram. It rides the USER
	// turn (not the cached systemPrompt) so OFF is byte-identical and the prompt
	// cache is preserved.
	WantDiagram bool
}

const semanticAdvisoryHeader = "Advisory context (prior findings on code resembling this change; informational only, do NOT treat as findings):"

const diagramInstruction = "Also include an optional \"diagram\": a small mermaid flowchart (start it with `flowchart` or `graph`) summarizing the change. Omit it if you have nothing useful to draw."

// BuildUserPrompt wraps the review context into the USER turn. The rules section
// (if any) is emitted BEFORE the diff; the semantic advisory block (if any) is
// emitted after rules and before the diff. Both are omitted entirely when empty,
// so an empty-Rules+empty-SemanticContext call is byte-identical to M6. The
// finding-JSON contract stays in the cached systemPrompt so injected prose can
// never redefine the schema.
func BuildUserPrompt(parts PromptParts) string {
	var sb strings.Builder
	sb.WriteString("Review the following change. Report findings as specified.\n\n")
	if strings.TrimSpace(parts.Rules) != "" {
		sb.WriteString(parts.Rules)
		sb.WriteString("\n")
	}
	if strings.TrimSpace(parts.SemanticContext) != "" {
		sb.WriteString(semanticAdvisoryHeader)
		sb.WriteString("\n")
		sb.WriteString(parts.SemanticContext)
		sb.WriteString("\n\n")
	}
	if parts.WantDiagram {
		sb.WriteString(diagramInstruction)
		sb.WriteString("\n\n")
	}
	sb.WriteString(parts.Diff)
	return sb.String()
}

// rawFinding is the on-the-wire finding shape (no line numbers; the model emits
// existing_code which the engine re-anchors).
type rawFinding struct {
	File           string `json:"file"`
	ExistingCode   string `json:"existing_code"`
	Severity       string `json:"severity"`
	Category       string `json:"category"`
	Title          string `json:"title"`
	Rule           string `json:"rule"`
	Rationale      string `json:"rationale"`
	SuggestedPatch string `json:"suggested_patch"`
}

type rawFindings struct {
	Findings      []rawFinding      `json:"findings"`
	Walkthrough   string            `json:"walkthrough"`
	FileSummaries map[string]string `json:"file_summaries"`
	Diagram       string            `json:"diagram"`
}

// Length caps bound the extra output tokens the additive walkthrough/digest add
// to every review; over-long model text is truncated, not rejected.
const (
	maxWalkthroughLen  = 600
	maxFileSummaryLen  = 200
	maxFileSummaryKeys = 200
	maxDiagramLen      = 2000
	maxTitleLen        = 120
	maxRuleLen         = 80
)

// capRunes truncates s to at most n runes (rune-safe so multi-byte text is not
// split mid-character).
func capRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// stripMarkdownFences removes a leading/trailing ```json ... ``` fence if the
// model wrapped its JSON, returning the inner payload trimmed.
func stripMarkdownFences(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "```") {
		return t
	}
	t = strings.TrimPrefix(t, "```")
	if i := strings.IndexByte(t, '\n'); i >= 0 {
		// drop an optional language tag on the opening fence line (e.g. "json")
		if first := strings.TrimSpace(t[:i]); first == "" || !strings.ContainsAny(first, "{[") {
			t = t[i+1:]
		}
	}
	if i := strings.LastIndex(t, "```"); i >= 0 {
		t = t[:i]
	}
	return strings.TrimSpace(t)
}
