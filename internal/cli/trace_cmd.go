package cli

import (
	"encoding/json"
	"io"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine"
)

// newTraceSink returns a trace Sink that writes each captured step to w as one
// NDJSON line ({"step":...,"payload":...}). Used by --trace to stream the live
// trace to stderr; local-only, never stdout. The whole line is run through
// config.RedactString so a token embedded in the diff/prompt free-text can't
// reach the terminal even live (the persisted copy is redacted separately). The
// Sink may fire from the agent's tool loop, so writes are mutex-serialized.
func newTraceSink(w io.Writer) func(step string, payload any) {
	var mu sync.Mutex
	return func(step string, payload any) {
		line, err := json.Marshal(traceStep{Step: step, Payload: payload})
		if err != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		_, _ = io.WriteString(w, config.RedactString(string(line))+"\n")
	}
}

// traceCommand renders the full review trace of a saved review:
// `miucr trace <id>` loads the review's redacted trace_json from history and
// shows the ordered steps (system prompt → diff identification → selected files
// → injected rules → user prompt → raw response → tool calls). The trace is
// LOCAL-only — it is read from the local history store, never re-fetched from a
// provider and never posted; secrets were redacted at persist.
func traceCommand(_ *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trace <id>",
		Short: "Show the full review trace of a saved review (system prompt, diff, rules, prompts, response)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(args[0])
			if id == "" {
				return &CLIError{Code: "trace.id_required", Message: "a non-empty review id is required", Hint: "miucr trace <id>", Exit: 2}
			}
			st, closeStore, err := openHistory(cmd.Context())
			if err != nil {
				return err
			}
			defer closeStore()
			rec, err := st.GetReview(cmd.Context(), id)
			if err != nil {
				return &CLIError{Code: "trace.not_found", Message: err.Error(), Hint: "run `miucr history` to list ids", Exit: 2, Details: map[string]any{"id": id}}
			}
			tr, perr := parseTrace(rec.TraceJSON)
			if perr != nil {
				return &CLIError{Code: "trace.corrupt", Message: "stored trace is not valid JSON: " + perr.Error(), Exit: 1, Details: map[string]any{"id": id}}
			}
			if prettyOutput {
				return renderTrace(cmd.OutOrStdout(), id, tr)
			}
			data := traceData(id, tr)
			summary := map[string]any{"id": id, "steps": len(traceSteps(tr))}
			return writeSuccess(cmd.OutOrStdout(), "trace", "trace.show", data, summary)
		},
	}
	return cmd
}

// parseTrace decodes a redacted trace_json blob. An empty blob (an old review or
// a --no-save run) yields a zero trace, not an error.
func parseTrace(blob string) (engine.ReviewTrace, error) {
	var tr engine.ReviewTrace
	if strings.TrimSpace(blob) == "" {
		return tr, nil
	}
	if err := json.Unmarshal([]byte(blob), &tr); err != nil {
		return engine.ReviewTrace{}, err
	}
	return tr, nil
}

// traceStep is one ordered entry in the trace.show data: a step name and its
// recorded payload (already redacted at persist).
type traceStep struct {
	Step    string `json:"step"`
	Payload any    `json:"payload"`
}

// traceSteps returns the trace as ordered steps mirroring the review pipeline:
// system prompt → diff identification → selected files → injected rules → user
// prompt → model/provider → raw response → tool calls. Empty steps are omitted.
func traceSteps(tr engine.ReviewTrace) []traceStep {
	var steps []traceStep
	add := func(name string, payload any, present bool) {
		if present {
			steps = append(steps, traceStep{Step: name, Payload: payload})
		}
	}
	add("system_prompt", tr.SystemPrompt, tr.SystemPrompt != "")
	add("diff_meta", tr.DiffMeta, tr.DiffMeta != engine.DiffMeta{})
	add("selected_files", tr.SelectedFiles, len(tr.SelectedFiles) > 0)
	add("injected_rules", tr.InjectedRules, len(tr.InjectedRules) > 0)
	add("user_prompt", tr.UserPrompt, tr.UserPrompt != "")
	add("model", map[string]string{"provider": tr.Provider, "model": tr.Model}, tr.Provider != "" || tr.Model != "")
	add("final_response", tr.FinalResponse, tr.FinalResponse != "")
	add("tool_calls", tr.Turns, len(tr.Turns) > 0)
	return steps
}

func traceData(id string, tr engine.ReviewTrace) map[string]any {
	return map[string]any{"id": id, "steps": traceSteps(tr)}
}

func renderTrace(w io.Writer, id string, tr engine.ReviewTrace) error {
	ew := &errWriter{w: w}
	ew.printf("trace for review %s\n\n", id)
	steps := traceSteps(tr)
	if len(steps) == 0 {
		ew.printf("(no trace recorded for this review)\n")
		return ew.err
	}
	for _, s := range steps {
		ew.printf("=== %s ===\n", s.Step)
		renderTracePayload(ew, s.Payload)
		ew.printf("\n")
	}
	return ew.err
}

func renderTracePayload(ew *errWriter, payload any) {
	switch v := payload.(type) {
	case string:
		ew.printf("%s\n", v)
	case []string:
		for _, line := range v {
			ew.printf("- %s\n", line)
		}
	case engine.DiffMeta:
		ew.printf("base:   %s\nhead:   %s\nsource: %s\n", v.BaseSHA, v.HeadSHA, v.Source)
	case []engine.RuleRef:
		for _, r := range v {
			ew.printf("- %s (%s)\n", r.Stem, r.Provenance)
		}
	case map[string]string:
		ew.printf("provider: %s  model: %s\n", v["provider"], v["model"])
	case []engine.TurnRecord:
		for _, tr := range v {
			ew.printf("- [%d] %s %s\n", tr.Turn, tr.Tool, tr.Args)
		}
	default:
		ew.printf("%v\n", v)
	}
}
