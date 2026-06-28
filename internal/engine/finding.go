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
	// Confidence (1-5) is the model's confidence the change is safe to merge; 0 = not emitted.
	Confidence       int
	ConfidenceReason string
	// Usage is the token consumption of this pass, summed across tool turns (and
	// subagent passes via mergeSubagentOutputs). Zero when the backend/fake omits it.
	Usage Usage
}

// Usage is the LLM token consumption for a review, broken down so the quota path
// meters total input (incl. cache) and callers can derive a cache-hit ratio.
// InputTokens is the UNCACHED new input only. Backends normalize to this: Anthropic
// reports cache as separate buckets outside input_tokens, while OpenAI reports
// cached_tokens as a sub-count of prompt_tokens — so the OpenAI backend subtracts it
// to keep InputTokens the uncached remainder. Zero when the backend/fake omits it.
type Usage struct {
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
}

// Add accumulates another pass's usage into u (all four buckets).
func (u *Usage) Add(o Usage) {
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.CacheReadTokens += o.CacheReadTokens
	u.CacheCreationTokens += o.CacheCreationTokens
}

// TotalInputTokens is all input processed: uncached new input plus both cache buckets.
func (u Usage) TotalInputTokens() int64 {
	return u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens
}

// TotalTokens is total input (incl. cache) plus output — what the tokens quota meters.
func (u Usage) TotalTokens() int64 { return u.TotalInputTokens() + u.OutputTokens }

// CacheHitRatio is cache-read over total input (0 when there is no input). It is the
// usage-optimization signal: how much input was served from the prompt cache.
func (u Usage) CacheHitRatio() float64 {
	ti := u.TotalInputTokens()
	if ti == 0 {
		return 0
	}
	return float64(u.CacheReadTokens) / float64(ti)
}

// ReviewResult is the engine output: the persisted id (empty when no Store is
// wired), anchored findings, plus run stats. Walkthrough/FileSummaries ride the
// same review pass (additive; empty when the model omits them).
type ReviewResult struct {
	ID               string            `json:"id,omitempty"`
	Findings         []Finding         `json:"findings"`
	Walkthrough      string            `json:"walkthrough,omitempty"`
	FileSummaries    map[string]string `json:"file_summaries,omitempty"`
	Diagram          string            `json:"diagram,omitempty"`
	Confidence       int               `json:"confidence,omitempty"`
	ConfidenceReason string            `json:"confidence_reason,omitempty"`
	Stats            map[string]any    `json:"stats"`
}
