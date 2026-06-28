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
		return fmt.Sprintf("grep: invalid arguments: %v", err), true
	}
	if strings.TrimSpace(args.Pattern) == "" {
		return "grep requires a non-empty \"pattern\"", true
	}
	label := grepLabel(args)
	progress(tc, "→ grep "+label)
	record(tc, turn, "grep", label)
	out, err := enginectx.Grep(ctx, tc.RepoDir, tc.Rev, args.Pattern, tc.Runner, args.File)
	if err != nil {
		return fmt.Sprintf("grep failed: %v", err), true
	}
	if out == "" {
		return "(no matches)", false
	}
	return out, false
}

func grepLabel(a grepArgs) string {
	if strings.TrimSpace(a.File) == "" {
		return a.Pattern
	}
	return fmt.Sprintf("%s in %s", a.Pattern, a.File)
}
