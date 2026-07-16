package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
	"github.com/vanducng/miu-cr/internal/engine/tools/symbolcontext"
)

type TraceRecorder interface {
	RecordTool(turn int, tool, args string)
	RecordToolResult(turn int, tool, args, result string, isErr bool)
}

type Context struct {
	RepoDir  string
	Rev      string
	Runner   *gitcmd.Runner
	Progress func(string)
	Trace    TraceRecorder
	// Index is the optional per-review symbol index shared by every
	// symbol_context call; nil keeps per-call scanning.
	Index *symbolcontext.Index
}

type Spec struct {
	Name        string
	Description string
	Properties  map[string]any
	Required    []string
}

func Specs() []Spec {
	return []Spec{fileReadSpec(), grepSpec(), symbolSpec()}
}

func Execute(ctx context.Context, cfg config.SymbolContext, tc Context, turn int, name string, input json.RawMessage) (string, bool) {
	if tc.Runner == nil {
		tc.Runner = gitcmd.New()
	}
	switch name {
	case "file_read":
		return runFileRead(ctx, tc, turn, input)
	case "grep":
		return runGrep(ctx, tc, turn, input)
	case "symbol_context":
		return symbolcontext.Run(ctx, cfg, symbolcontext.Context{
			RepoDir:  tc.RepoDir,
			Rev:      tc.Rev,
			Runner:   tc.Runner,
			Progress: tc.Progress,
			Trace:    tc.Trace,
			Index:    tc.Index,
		}, turn, input)
	default:
		out := fmt.Sprintf("unknown tool %q", name)
		record(tc, turn, name, "(unknown tool)")
		recordResult(tc, turn, name, "(unknown tool)", out, true)
		return out, true
	}
}

func symbolSpec() Spec {
	spec := symbolcontext.ToolSpec()
	return Spec{
		Name:        spec.Name,
		Description: spec.Description,
		Properties:  spec.Properties,
		Required:    spec.Required,
	}
}

func progress(tc Context, msg string) {
	if tc.Progress != nil {
		tc.Progress(msg)
	}
}

func record(tc Context, turn int, tool, args string) {
	if tc.Trace != nil {
		tc.Trace.RecordTool(turn, tool, args)
	}
}

func recordResult(tc Context, turn int, tool, args, result string, isErr bool) {
	if tc.Trace != nil {
		tc.Trace.RecordToolResult(turn, tool, args, result, isErr)
	}
}
