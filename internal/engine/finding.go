package engine

// Finding is one reviewer observation. The model emits it WITHOUT line numbers;
// the engine re-anchors from QuotedCode against the reviewed revision.
type Finding struct {
	File           string `json:"file"`
	Line           int    `json:"line"`
	EndLine        int    `json:"end_line"`
	Severity       string `json:"severity"`
	Category       string `json:"category"`
	Rationale      string `json:"rationale"`
	SuggestedPatch string `json:"suggested_patch"`
	QuotedCode     string `json:"quoted_code"` // OCR ExistingCode: code the finding refers to, used as the anchor.
}

// ReviewResult is the engine output: the persisted id (empty when no Store is
// wired), anchored findings, plus run stats.
type ReviewResult struct {
	ID       string         `json:"id,omitempty"`
	Findings []Finding      `json:"findings"`
	Stats    map[string]any `json:"stats"`
}
