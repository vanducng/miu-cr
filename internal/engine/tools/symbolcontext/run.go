package symbolcontext

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

const (
	defaultMaxBytes       = 16000
	truncateMarker        = "\n...(truncated)"
	NoSymbolContextMarker = "(no symbol context)"
	NoSymbolsFoundMarker  = "(no symbols found)"
)

type TraceRecorder interface {
	RecordTool(turn int, tool, args string)
}

type Context struct {
	RepoDir  string
	Rev      string
	Runner   *gitcmd.Runner
	Progress func(string)
	Trace    TraceRecorder
}

type Args struct {
	Relation string `json:"relation"`
	Symbol   string `json:"symbol"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Limit    int    `json:"limit"`
}

func Run(ctx context.Context, cfg config.SymbolContext, tc Context, turn int, input json.RawMessage) (string, bool) {
	var args Args
	if err := json.Unmarshal(input, &args); err != nil {
		return "symbol_context: invalid arguments: " + config.RedactString(err.Error()), true
	}
	args.Relation = strings.TrimSpace(args.Relation)
	if args.Relation == "" {
		return "symbol_context requires a non-empty \"relation\"", true
	}
	label := label(args)
	progress(tc, "→ symbol_context "+label)
	record(tc, turn, "symbol_context", label)
	out, err := scan(ctx, cfg, tc, args)
	if err != nil {
		return "symbol_context failed: " + config.RedactString(err.Error()), true
	}
	if strings.TrimSpace(out) == "" {
		return NoSymbolContextMarker, false
	}
	return capUTF8(out, cfg.MaxBytesOrDefault(defaultMaxBytes)), false
}

func label(a Args) string {
	target := strings.TrimSpace(a.Symbol)
	if target == "" {
		target = strings.TrimSpace(a.File)
	}
	if a.Line > 0 && strings.TrimSpace(a.File) != "" {
		target = fmt.Sprintf("%s:%d", a.File, a.Line)
	}
	if target == "" {
		return a.Relation
	}
	return a.Relation + " " + target
}

func capUTF8(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= len(truncateMarker) {
		return validUTF8Prefix(s, max)
	}
	cut := utf8BoundaryString(s, max-len(truncateMarker))
	if cut <= 0 {
		return ""
	}
	return s[:cut] + truncateMarker
}

func validUTF8Prefix(s string, max int) string {
	if max <= 0 {
		return ""
	}
	cut := max
	if cut > len(s) {
		cut = len(s)
	}
	cut = utf8BoundaryString(s, cut)
	return s[:cut]
}

func utf8BoundaryString(s string, cut int) int {
	if cut > len(s) {
		cut = len(s)
	}
	for cut > 0 && cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return cut
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
