package engine

// Finding is one reviewer observation. The model emits it WITHOUT line numbers;
// the engine re-anchors from QuotedCode against the reviewed revision.
type Finding struct {
	File           string `json:"file"`
	Line           int    `json:"line"`
	EndLine        int    `json:"end_line"`
	Title          string `json:"title,omitempty"` // optional short scannable summary the model emits in the same pass.
	Rule           string `json:"rule,omitempty"`  // optional bare stem of the injected rule that motivated this finding; validated/linked downstream.
	Severity       string `json:"severity"`
	Category       string `json:"category"`
	Rationale      string `json:"rationale"`
	SuggestedPatch string `json:"suggested_patch"`
	QuotedCode     string `json:"quoted_code"` // OCR ExistingCode: code the finding refers to, used as the anchor.
}

// ReviewOutput is one review pass's parsed result: the strict findings array
// (primary) plus the optional, additive walkthrough/per-file digest the same
// pass may emit. Empty Walkthrough/FileSummaries => a back-compatible response.
type ReviewOutput struct {
	Findings      []Finding
	Walkthrough   string
	FileSummaries map[string]string
	// Diagram is the optional mermaid change diagram (opt-in via --walkthrough-diagram).
	// Empty unless the model emitted one on a diagram-requested pass.
	Diagram string
}

// ReviewResult is the engine output: the persisted id (empty when no Store is
// wired), anchored findings, plus run stats. Walkthrough/FileSummaries ride the
// same review pass (additive; empty when the model omits them).
type ReviewResult struct {
	ID            string            `json:"id,omitempty"`
	Findings      []Finding         `json:"findings"`
	Walkthrough   string            `json:"walkthrough,omitempty"`
	FileSummaries map[string]string `json:"file_summaries,omitempty"`
	Diagram       string            `json:"diagram,omitempty"`
	Stats         map[string]any    `json:"stats"`
}
