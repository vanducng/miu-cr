package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	enginectx "github.com/vanducng/miu-cr/internal/engine/context"
)

type grepArgs struct {
	Pattern string `json:"pattern"`
	File    string `json:"file"`
}

func grepSpec() Spec {
	return Spec{
		Name:        "grep",
		Description: "Search the reviewed revision for a fixed string.",
		Properties: map[string]any{
			"pattern": map[string]any{"type": "string", "description": "fixed string to search for"},
			"file":    map[string]any{"type": "string", "description": "optional file path to limit the search"},
		},
		Required: []string{"pattern"},
	}
}

func runGrep(ctx context.Context, tc Context, turn int, input json.RawMessage) (string, bool) {
	var args grepArgs
	if err := json.Unmarshal(input, &args); err != nil {
		out := fmt.Sprintf("grep: invalid arguments: %v", err)
		record(tc, turn, "grep", "(invalid arguments)")
		recordResult(tc, turn, "grep", "(invalid arguments)", out, true)
		return out, true
	}
	if strings.TrimSpace(args.Pattern) == "" {
		out := "grep requires a non-empty \"pattern\""
		record(tc, turn, "grep", "(missing pattern)")
		recordResult(tc, turn, "grep", "(missing pattern)", out, true)
		return out, true
	}
	label := grepLabel(args)
	progress(tc, "→ grep "+label)
	record(tc, turn, "grep", label)
	out, err := enginectx.Grep(ctx, tc.RepoDir, tc.Rev, args.Pattern, tc.Runner, args.File)
	if err != nil {
		out := fmt.Sprintf("grep failed: %v", err)
		recordResult(tc, turn, "grep", label, out, true)
		return out, true
	}
	if out == "" {
		out = "(no matches)"
	}
	recordResult(tc, turn, "grep", label, out, false)
	return out, false
}

func grepLabel(a grepArgs) string {
	if strings.TrimSpace(a.File) == "" {
		return a.Pattern
	}
	return fmt.Sprintf("%s in %s", a.Pattern, a.File)
}
