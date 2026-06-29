package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

// redactTrace must blank secrets two ways: a token in a STRUCTURED free-text
// field (system/user prompt) AND a token embedded in the diff/prompt prose both
// run through config.RedactString, so neither survives in the marshaled JSON.
func TestRedactTraceCoversStructuredAndFreeText(t *testing.T) {
	const tok = "sk-ant-secrettoken1234567890"
	const dsnTok = "postgres://u:hunter2pw@h:5432/db"
	tr := ReviewTrace{
		SystemPrompt:  "system prompt with token=" + tok,
		UserPrompt:    "diff line: x_api_key=" + tok + " connecting " + dsnTok,
		FinalResponse: "the model echoed " + tok,
		InjectedRules: []RuleRef{{Stem: "auth token=" + tok, Provenance: "user"}},
		SelectedFiles: []string{"a.go"},
		Turns:         []TurnRecord{{Turn: 0, Tool: "grep", Args: "secret=" + tok, Result: "found " + tok}},
	}
	blob, err := json.Marshal(redactTrace(tr))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(blob)
	if strings.Contains(s, tok) {
		t.Fatalf("provider token leaked into trace_json:\n%s", s)
	}
	if strings.Contains(s, "hunter2pw") {
		t.Fatalf("DSN password leaked into trace_json:\n%s", s)
	}
}

// redactTrace redacts the reasoning text field (reasoning quotes diff content).
func TestRedactTraceRedactsReasoning(t *testing.T) {
	const tok = "sk-ant-secrettoken1234567890"
	tr := ReviewTrace{
		Reasoning: &TraceReasoning{
			Provider: "anthropic",
			Text:     "the diff shows sk-ant-api_key: " + tok,
			Tokens:   100,
		},
	}
	blob, err := json.Marshal(redactTrace(tr))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(blob)
	if strings.Contains(s, tok) {
		t.Fatalf("token leaked in reasoning text:\n%s", s)
	}
	// Provider and tokens must survive redaction (non-secret metadata).
	if !strings.Contains(s, "anthropic") {
		t.Errorf("provider must survive redaction: %s", s)
	}
}

// redactTrace redacts per-turn reason text (the assistant prose quotes diff content).
func TestRedactTraceRedactsTurnReason(t *testing.T) {
	const tok = "sk-ant-secrettoken1234567890"
	tr := ReviewTrace{
		TurnReasons: []TurnReason{
			{Turn: 0, Text: "I'll grep for the key sk-ant-api_key=" + tok},
			{Turn: 1, Text: "no secret here, just logic"},
		},
	}
	blob, err := json.Marshal(redactTrace(tr))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if s := string(blob); strings.Contains(s, tok) {
		t.Fatalf("token leaked in turn reason:\n%s", s)
	}
}

// RecordTurnReason appends + emits a turn_reason step, is nil-safe, skips empty
// text, and caps oversized prose to the tool-result byte bound.
func TestRecordTurnReason(t *testing.T) {
	var nilTrace *ReviewTrace
	nilTrace.RecordTurnReason(0, "x") // must not panic

	var steps []string
	tr := &ReviewTrace{Sink: func(step string, _ any) { steps = append(steps, step) }}
	tr.RecordTurnReason(0, "")               // empty: no-op
	tr.RecordTurnReason(0, "why I grep")     // recorded
	tr.RecordTurnReason(1, strings.Repeat("a", maxTraceToolResultBytes+500))

	if len(tr.TurnReasons) != 2 {
		t.Fatalf("want 2 turn reasons, got %d", len(tr.TurnReasons))
	}
	if strings.Join(steps, ",") != "turn_reason,turn_reason" {
		t.Fatalf("sink steps = %v", steps)
	}
	if got := len(tr.TurnReasons[1].Text); got > maxTraceToolResultBytes {
		t.Fatalf("turn reason not capped: %d bytes", got)
	}
	if !strings.HasSuffix(tr.TurnReasons[1].Text, "[truncated turn reason]...") {
		t.Fatalf("truncated turn reason lacks marker: %q", tr.TurnReasons[1].Text[len(tr.TurnReasons[1].Text)-40:])
	}
}

// The setters are nil-safe (a nil *ReviewTrace is a no-op) and Sink, when set,
// receives each recorded step.
func TestReviewTraceSettersNilSafeAndSink(t *testing.T) {
	var nilTrace *ReviewTrace
	nilTrace.SetSystemPrompt("x")
	nilTrace.SetModel("p", "m")
	nilTrace.SetDiffMeta(DiffMeta{HeadSHA: "h"})
	nilTrace.SetSelectedFiles([]string{"a.go"})
	nilTrace.SetInjectedRules([]RuleRef{{Stem: "r"}})
	nilTrace.RecordTool(0, "grep", "x") // must not panic
	nilTrace.RecordToolResult(0, "grep", "x", "y", false)

	var steps []string
	tr := &ReviewTrace{Sink: func(step string, _ any) { steps = append(steps, step) }}
	tr.SetSystemPrompt("sys")
	tr.SetModel("anthropic", "fable-1")
	tr.SetDiffMeta(DiffMeta{HeadSHA: "h"})
	tr.SetSelectedFiles([]string{"a.go"})
	tr.SetInjectedRules([]RuleRef{{Stem: "r", Provenance: "user"}})
	tr.SetPrompt("user")
	tr.RecordTool(0, "grep", "x")
	tr.RecordToolResult(0, "grep", "x", "y", false)
	tr.SetFinalResponse("done")

	want := []string{"system_prompt", "model", "diff_meta", "selected_files", "injected_rules", "user_prompt", "tool", "tool_result", "final_response"}
	if strings.Join(steps, ",") != strings.Join(want, ",") {
		t.Fatalf("sink steps = %v, want %v", steps, want)
	}
	if tr.SystemPrompt != "sys" || tr.Provider != "anthropic" || tr.Model != "fable-1" {
		t.Fatalf("setters did not record: %+v", tr)
	}
}

// SetModel/SetSystemPrompt are first-write-wins so the engine's req values are not
// clobbered by a backend's later defensive call.
func TestSetModelFirstWriteWins(t *testing.T) {
	tr := &ReviewTrace{}
	tr.SetModel("anthropic", "claude-x")
	tr.SetModel("openai", "gpt-x")
	if tr.Provider != "anthropic" || tr.Model != "claude-x" {
		t.Fatalf("first-write-wins violated: %q/%q", tr.Provider, tr.Model)
	}
	tr.SetSystemPrompt("first")
	tr.SetSystemPrompt("second")
	if tr.SystemPrompt != "first" {
		t.Fatalf("SetSystemPrompt not first-write-wins: %q", tr.SystemPrompt)
	}
}
