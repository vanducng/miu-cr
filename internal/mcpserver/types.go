package mcpserver

import (
	"time"

	"github.com/vanducng/miu-cr/internal/engine"
)

type reviewRunInput struct {
	Staged      bool   `json:"staged,omitempty" jsonschema:"Review staged changes against the index."`
	From        string `json:"from,omitempty" jsonschema:"Range mode: base ref (use with to)."`
	To          string `json:"to,omitempty" jsonschema:"Range mode: target ref (use with from)."`
	Commit      string `json:"commit,omitempty" jsonschema:"Review a single commit against its parent."`
	Gate        string `json:"gate,omitempty" jsonschema:"Severity gate: none|info|low|medium|high|critical. Defaults to high."`
	Expand      *int   `json:"expand,omitempty" jsonschema:"Context lines above/below each hunk in the new-content window. Defaults to 5; 0 disables."`
	TokenBudget int    `json:"token_budget,omitempty" jsonschema:"Approximate token budget; over budget degrades context. 0 disables."`
}

type reviewRunOutput struct {
	ID       string           `json:"id,omitempty"`
	Findings []engine.Finding `json:"findings"`
	Stats    map[string]any   `json:"stats"`
}

type reviewGetInput struct {
	ID string `json:"id" jsonschema:"The review id returned by a prior review_run."`
}

type reviewGetOutput struct {
	ID        string           `json:"id"`
	RepoDir   string           `json:"repo_dir"`
	Mode      string           `json:"mode"`
	HeadSHA   string           `json:"head_sha,omitempty"`
	CreatedAt time.Time        `json:"created_at"`
	Findings  []engine.Finding `json:"findings"`
	Stats     map[string]any   `json:"stats"`
}
