package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/diff"
)

func registerTools(server *mcp.Server, deps Deps, opts Options, policy safetyPolicy) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "review_run",
		Description: "Review local git changes (staged, a range, or a single commit) and return gated findings. Findings are anchored to line numbers from the reviewed revision.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in reviewRunInput) (*mcp.CallToolResult, reviewRunOutput, error) {
		expand := 5
		if in.Expand != nil {
			expand = clampExpand(*in.Expand)
		}
		gate := in.Gate
		if gate == "" {
			gate = "high"
		}
		if err := engine.ValidateInvocation(in.Staged, in.From, in.To, in.Commit, gate); err != nil {
			return nil, reviewRunOutput{}, policy.toolErr("review.invalid_request", err)
		}
		res, err := deps.Engine.Review(ctx, engine.Request{
			Mode:         modeFor(in),
			Staged:       in.Staged,
			From:         in.From,
			To:           in.To,
			Commit:       in.Commit,
			Gate:         gate,
			RepoDir:      ".",
			ExpandWindow: expand,
			TokenBudget:  in.TokenBudget,
		})
		if err != nil {
			return nil, reviewRunOutput{}, policy.toolErr("review.run_failed", err)
		}
		out := reviewRunOutput{ID: res.ID, Findings: res.Findings, Stats: res.Stats}
		return nil, out, policy.enforceBytes(out)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "review_get",
		Description: "Fetch a persisted review by id, as returned from a prior review_run.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in reviewGetInput) (*mcp.CallToolResult, reviewGetOutput, error) {
		rec, err := deps.Engine.GetReview(ctx, in.ID)
		if err != nil {
			return nil, reviewGetOutput{}, policy.toolErr("review.get_failed", err)
		}
		out := reviewGetOutput{
			ID:        rec.ID,
			RepoDir:   rec.RepoDir,
			Mode:      rec.Mode,
			HeadSHA:   rec.HeadSHA,
			CreatedAt: rec.CreatedAt,
			Findings:  rec.Findings,
			Stats:     rec.Stats,
		}
		return nil, out, policy.enforceBytes(out)
	})
}

// clampExpand bounds the caller-supplied context-window size to [0,50]:
// negatives disable expansion, oversized values are capped.
func clampExpand(n int) int {
	if n < 0 {
		return 0
	}
	if n > 50 {
		return 50
	}
	return n
}

func modeFor(in reviewRunInput) diff.Mode {
	switch {
	case in.Commit != "":
		return diff.ModeCommit
	case in.From != "" || in.To != "":
		return diff.ModeRange
	default:
		return diff.ModeStaged
	}
}
