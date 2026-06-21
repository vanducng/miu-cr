package agent

import (
	stdctx "context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	openai "github.com/openai/openai-go/v3"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine"
)

// fakeOpenAI returns scripted completions and records the params it saw, so the
// tool loop and parse path run with zero network.
type fakeOpenAI struct {
	responses []string // JSON ChatCompletion bodies, served in order
	calls     int
	seen      []openai.ChatCompletionNewParams
}

func (f *fakeOpenAI) create(_ stdctx.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	f.seen = append(f.seen, params)
	body := f.responses[f.calls]
	f.calls++
	var cc openai.ChatCompletion
	if err := json.Unmarshal([]byte(body), &cc); err != nil {
		return nil, err
	}
	return &cc, nil
}

var _ openaiClient = (*fakeOpenAI)(nil)

func textCompletion(content string) string {
	b, _ := json.Marshal(map[string]any{
		"id":      "cmpl-1",
		"object":  "chat.completion",
		"created": 1,
		"model":   "gpt-test",
		"choices": []map[string]any{{
			"index":         0,
			"finish_reason": "stop",
			"message":       map[string]any{"role": "assistant", "content": content},
		}},
	})
	return string(b)
}

func toolCallCompletion(id, name, args string) string {
	b, _ := json.Marshal(map[string]any{
		"id":      "cmpl-2",
		"object":  "chat.completion",
		"created": 1,
		"model":   "gpt-test",
		"choices": []map[string]any{{
			"index":         0,
			"finish_reason": "tool_calls",
			"message": map[string]any{
				"role": "assistant",
				"tool_calls": []map[string]any{{
					"id":       id,
					"type":     "function",
					"function": map[string]any{"name": name, "arguments": args},
				}},
			},
		}},
	})
	return string(b)
}

// The model returns findings JSON on the first turn: openaiAgent must parse it
// (fence-stripped) into engine.Findings with no line numbers and no network.
func TestOpenAIAgentParsesFindings(t *testing.T) {
	body := "```json\n" +
		`{"findings":[{"file":"a.go","existing_code":"x := y / 0","severity":"critical","category":"bug","rationale":"div by zero"}]}` +
		"\n```"
	a := &openaiAgent{
		client: &fakeOpenAI{responses: []string{textCompletion(body)}},
		model:  "gpt-test",
	}
	got, err := a.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 finding, got %d", len(got))
	}
	f := got[0]
	if f.File != "a.go" || f.Severity != "critical" || f.Category != "bug" {
		t.Fatalf("bad finding: %+v", f)
	}
	if f.QuotedCode != "x := y / 0" {
		t.Fatalf("existing_code not mapped: %q", f.QuotedCode)
	}
	if f.Line != 0 || f.EndLine != 0 {
		t.Fatalf("agent must emit no line numbers: %+v", f)
	}
}

// Hits-injected: a non-empty SemanticContext must reach the OpenAI user turn
// (the advisory header + body), proving the lockstep field threads openai.go's
// BuildUserPrompt call. Mirrors the Anthropic provider test.
func TestOpenAIAgentInjectsSemanticContext(t *testing.T) {
	fc := &fakeOpenAI{responses: []string{textCompletion(`{"findings":[]}`)}}
	a := &openaiAgent{client: fc, model: "gpt-test"}
	advisory := "- [bug] prior off-by-one"
	if _, err := a.Review(stdctx.Background(), Context{Text: "ctx", SemanticContext: advisory, RepoDir: t.TempDir()}); err != nil {
		t.Fatalf("Review: %v", err)
	}
	raw, _ := json.Marshal(fc.seen[0])
	if !strings.Contains(string(raw), semanticAdvisoryHeader) {
		t.Fatalf("advisory header missing from OpenAI user turn: %s", raw)
	}
	if !strings.Contains(string(raw), advisory) {
		t.Fatalf("advisory body missing from OpenAI user turn: %s", raw)
	}
}

// A tool_use turn followed by a findings turn: the loop must dispatch the tool
// (grep with no matches against an empty temp repo) and parse the next turn.
func TestOpenAIAgentToolLoopThenFindings(t *testing.T) {
	fc := &fakeOpenAI{responses: []string{
		toolCallCompletion("call_1", "grep", `{"pattern":"needle"}`),
		textCompletion(`{"findings":[]}`),
	}}
	a := &openaiAgent{client: fc, model: "gpt-test"}
	got, err := a.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 findings, got %d", len(got))
	}
	if fc.calls != 2 {
		t.Fatalf("expected 2 completions, got %d", fc.calls)
	}
	// Second request must carry a tool-role message answering call_1.
	last := fc.seen[len(fc.seen)-1]
	foundTool := false
	for _, m := range last.Messages {
		if m.OfTool != nil && m.OfTool.ToolCallID == "call_1" {
			foundTool = true
		}
	}
	if !foundTool {
		t.Fatal("tool result message for call_1 not appended to follow-up request")
	}
}

// Repeated unparseable text bails after maxEmptyRounds, never hangs.
func TestOpenAIAgentEmptyRoundsBail(t *testing.T) {
	resp := make([]string, maxEmptyRounds)
	for i := range resp {
		resp[i] = textCompletion("sorry, no JSON here")
	}
	a := &openaiAgent{client: &fakeOpenAI{responses: resp}, model: "gpt-test"}
	_, err := a.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "no parseable findings") {
		t.Fatalf("expected empty-round bail, got %v", err)
	}
}

// toolHungryOpenAI always asks for a tool WHILE tools are offered; once the loop
// withdraws them on the forced final turn it returns findings (if scripted),
// modelling a real model exploring a large diff until forced to answer.
type toolHungryOpenAI struct {
	findings string // returned once tools are withdrawn; "" => degenerate (never finalizes)
	calls    int
}

func (f *toolHungryOpenAI) create(_ stdctx.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	f.calls++
	body := toolCallCompletion(fmt.Sprintf("call_%d", f.calls), "grep", `{"pattern":"x"}`)
	if len(params.Tools) == 0 && f.findings != "" {
		body = textCompletion(f.findings)
	}
	var cc openai.ChatCompletion
	if err := json.Unmarshal([]byte(body), &cc); err != nil {
		return nil, err
	}
	return &cc, nil
}

var _ openaiClient = (*toolHungryOpenAI)(nil)

// On a large diff the model may keep requesting tools to the budget. The loop
// must FORCE finalization on the last turn (withdraw tools + nudge) so it is
// driven to emit findings — a real review, not a hard maxToolTurns failure.
func TestOpenAIAgentForcedFinalizeReturnsFindings(t *testing.T) {
	fc := &toolHungryOpenAI{findings: `{"findings":[{"file":"a.go","existing_code":"x","severity":"warning","category":"bug","rationale":"r"}]}`}
	a := &openaiAgent{client: fc, model: "gpt-test"}
	got, err := a.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 finding from forced finalize, got %d", len(got))
	}
	if fc.calls != maxToolTurns {
		t.Fatalf("expected %d calls (explore to budget then finalize), got %d", maxToolTurns, fc.calls)
	}
}

// Genuinely-degenerate case: never finalizes even when forced → still errors,
// never silently returns empty findings.
func TestOpenAIAgentForcedFinalizeStillErrors(t *testing.T) {
	fc := &toolHungryOpenAI{}
	a := &openaiAgent{client: fc, model: "gpt-test"}
	_, err := a.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "finalization") {
		t.Fatalf("expected forced-finalization error, got %v", err)
	}
}

// New(creds) with the OpenAI kind must build an openaiAgent (interface
// satisfied) without touching the network.
func TestNewBuildsOpenAIAgent(t *testing.T) {
	creds := Credentials{Kind: config.KindOpenAI, APIKey: secretToken, Model: "gpt-test"}
	a, err := New(creds, 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := a.(*openaiAgent); !ok {
		t.Fatalf("expected *openaiAgent, got %T", a)
	}
	_ = engine.Finding{}
}

// The request must carry max_tokens (broadly compatible) and NOT the newer
// max_completion_tokens, so OpenAI-compatible gateways accept the call.
func TestOpenAIUsesMaxTokens(t *testing.T) {
	fc := &fakeOpenAI{responses: []string{textCompletion(`{"findings":[]}`)}}
	a := &openaiAgent{client: fc, model: "gpt-test"}
	if _, err := a.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir()}); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(fc.seen) == 0 {
		t.Fatal("no request captured")
	}
	p := fc.seen[0]
	if p.MaxTokens.Value != int64(maxTokens) {
		t.Fatalf("max_tokens not set: got %v, want %d", p.MaxTokens, maxTokens)
	}
	if p.MaxCompletionTokens.Valid() {
		t.Fatalf("max_completion_tokens must be unset for gateway compatibility, got %v", p.MaxCompletionTokens)
	}
}
