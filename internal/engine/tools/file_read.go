package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	enginectx "github.com/vanducng/miu-cr/internal/engine/context"
)

type fileReadArgs struct {
	File  string `json:"file"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

func fileReadSpec() Spec {
	return Spec{
		Name:        "file_read",
		Description: "Read a line range of a file at the reviewed revision.",
		Properties: map[string]any{
			"file":  map[string]any{"type": "string", "description": "path to read"},
			"start": map[string]any{"type": "integer", "description": "1-based start line"},
			"end":   map[string]any{"type": "integer", "description": "1-based end line"},
		},
		Required: []string{"file"},
	}
}

func runFileRead(ctx context.Context, tc Context, turn int, input json.RawMessage) (string, bool) {
	var args fileReadArgs
	if err := json.Unmarshal(input, &args); err != nil {
		out := fmt.Sprintf("file_read: invalid arguments: %v", err)
		record(tc, turn, "file_read", "(invalid arguments)")
		recordResult(tc, turn, "file_read", "(invalid arguments)", out, true)
		return out, true
	}
	if strings.TrimSpace(args.File) == "" {
		out := "file_read requires a non-empty \"file\""
		record(tc, turn, "file_read", "(missing file)")
		recordResult(tc, turn, "file_read", "(missing file)", out, true)
		return out, true
	}
	label := fileReadLabel(args)
	progress(tc, "→ file_read "+label)
	record(tc, turn, "file_read", label)
	out, err := enginectx.ReadRange(ctx, tc.RepoDir, tc.Rev, args.File, args.Start, args.End, tc.Runner)
	if err != nil {
		out := fmt.Sprintf("file_read failed: %v", err)
		recordResult(tc, turn, "file_read", label, out, true)
		return out, true
	}
	if out == "" {
		out = "(no lines in range)"
	}
	recordResult(tc, turn, "file_read", label, out, false)
	return out, false
}

func fileReadLabel(a fileReadArgs) string {
	if a.Start <= 0 {
		return a.File
	}
	if a.End <= 0 {
		return fmt.Sprintf("%s:%d", a.File, a.Start)
	}
	return fmt.Sprintf("%s:%d-%d", a.File, a.Start, a.End)
}
