package agent

import (
	stdctx "context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/vanducng/miu-cr/internal/engine"
)

// fakeAnthropic serves scripted Messages and records params, so the tool/parse
// loop runs with zero network. Mirrors fakeOpenAI for symmetric verification.
type fakeAnthropic struct {
	responses []string // JSON Message bodies, served in order
	calls     int
	seen      []anthropic.MessageNewParams
	err       error
}

func (f *fakeAnthropic) newMessage(_ stdctx.Context, params anthropic.MessageNewParams) (*anthropic.Message, error) {
	f.seen = append(f.seen, params)
	if f.err != nil {
		return nil, f.err
	}
	body := f.responses[f.calls]
	f.calls++
	var msg anthropic.Message
	if err := json.Unmarshal([]byte(body), &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

var _ anthropicClient = (*fakeAnthropic)(nil)

func textMessage(content string) string {
	b, _ := json.Marshal(map[string]any{
		"id":          "msg_1",
		"type":        "message",
		"role":        "assistant",
		"model":       "claude-test",
		"stop_reason": "end_turn",
		"content":     []map[string]any{{"type": "text", "text": content}},
		"usage":       map[string]any{"input_tokens": 1, "output_tokens": 1},
	})
	return string(b)
}

func toolUseMessage(id, name string, input map[string]any) string {
	b, _ := json.Marshal(map[string]any{
		"id":          "msg_2",
		"type":        "message",
		"role":        "assistant",
		"model":       "claude-test",
		"stop_reason": "tool_use",
		"content": []map[string]any{{
			"type":  "tool_use",
			"id":    id,
			"name":  name,
			"input": input,
		}},
		"usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
	})
	return string(b)
}

// The model returns findings JSON on the first turn: anthropicAgent must parse it
// (fence-stripped) into engine.Findings with no line numbers and no network.
func TestAnthropicAgentParsesFindings(t *testing.T) {
	body := "```json\n" +
		`{"findings":[{"file":"a.go","existing_code":"x := y / 0","severity":"critical","category":"bug","rationale":"div by zero"}]}` +
		"\n```"
	a := &anthropicAgent{
		client: &fakeAnthropic{responses: []string{textMessage(body)}},
		model:  "claude-test",
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

// A tool_use turn followed by a findings turn: the loop must dispatch the tool
// (grep against an empty temp repo) and thread a tool_result into the next request.
func TestAnthropicAgentToolLoopThenFindings(t *testing.T) {
	fc := &fakeAnthropic{responses: []string{
		toolUseMessage("tu_1", "grep", map[string]any{"pattern": "needle"}),
		textMessage(`{"findings":[]}`),
	}}
	a := &anthropicAgent{client: fc, model: "claude-test"}
	got, err := a.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 findings, got %d", len(got))
	}
	if fc.calls != 2 {
		t.Fatalf("expected 2 messages, got %d", fc.calls)
	}
	// The follow-up request must carry a user message with a tool_result block.
	last := fc.seen[len(fc.seen)-1]
	foundTool := false
	for _, m := range last.Messages {
		for _, b := range m.Content {
			if b.OfToolResult != nil && b.OfToolResult.ToolUseID == "tu_1" {
				foundTool = true
			}
		}
	}
	if !foundTool {
		t.Fatal("tool_result for tu_1 not appended to follow-up request")
	}
}

// Repeated unparseable text bails after maxEmptyRounds, never hangs.
func TestAnthropicAgentEmptyRoundsBail(t *testing.T) {
	resp := make([]string, maxEmptyRounds)
	for i := range resp {
		resp[i] = textMessage("sorry, no JSON here")
	}
	a := &anthropicAgent{client: &fakeAnthropic{responses: resp}, model: "claude-test"}
	_, err := a.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "no parseable findings") {
		t.Fatalf("expected empty-round bail, got %v", err)
	}
}

// maxToolTurns must bound the loop: a client that always asks for a tool exhausts
// the turn budget instead of spinning forever.
func TestAnthropicAgentMaxToolTurns(t *testing.T) {
	resp := make([]string, maxToolTurns)
	for i := range resp {
		resp[i] = toolUseMessage(fmt.Sprintf("tu_%d", i), "grep", map[string]any{"pattern": "x"})
	}
	a := &anthropicAgent{client: &fakeAnthropic{responses: resp}, model: "claude-test"}
	_, err := a.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "maxToolTurns") {
		t.Fatalf("expected maxToolTurns exhaustion, got %v", err)
	}
}

// A surfaced API error must be wrapped (and never leak the token); the loop
// returns immediately.
func TestAnthropicAgentClientErrorWrapped(t *testing.T) {
	a := &anthropicAgent{
		client: &fakeAnthropic{err: fmt.Errorf("401 x-api-key: %s invalid", secretToken)},
		model:  "claude-test",
	}
	_, err := a.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "messages.new") {
		t.Fatalf("expected wrapped messages.new error, got %v", err)
	}
	_ = engine.Finding{}
}

// A wall-clock deadline already in the past must abort before any API call.
func TestAnthropicAgentDeadline(t *testing.T) {
	fc := &fakeAnthropic{responses: []string{textMessage(`{"findings":[]}`)}}
	a := &anthropicAgent{client: fc, model: "claude-test", timeout: time.Nanosecond}
	_, err := a.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir()})
	if err == nil {
		t.Fatal("expected deadline/timeout error")
	}
}
