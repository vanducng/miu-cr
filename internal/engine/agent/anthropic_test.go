package agent

import (
	stdctx "context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine"
)

// fakeAnthropic serves scripted Messages and records params, so the tool/parse
// loop runs with zero network. Mirrors fakeOpenAI for symmetric verification.
type fakeAnthropic struct {
	responses []string // JSON Message bodies, served in order
	calls     int
	seen      []anthropic.MessageNewParams
	err       error
	errs      []error
}

func (f *fakeAnthropic) newMessage(_ stdctx.Context, params anthropic.MessageNewParams) (*anthropic.Message, error) {
	f.seen = append(f.seen, params)
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		if err != nil {
			return nil, err
		}
	}
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

func fastProviderRetry(maxRetries int) config.ProviderRetry {
	return config.ProviderRetry{MaxRetries: &maxRetries, InitialBackoff: "0s", MaxBackoff: "0s", MaxElapsed: "0s"}
}

func containsProgress(progress []string, needle string) bool {
	for _, s := range progress {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

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
	fc := &fakeAnthropic{responses: []string{textMessage(body)}}
	a := &anthropicAgent{client: fc, model: "claude-test"}
	out, err := a.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	got := out.Findings
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
	if !fc.seen[0].Temperature.Valid() || fc.seen[0].Temperature.Value != 0 {
		t.Fatalf("review temperature = %+v, want 0", fc.seen[0].Temperature)
	}
}

func TestAnthropicAgentRetriesProviderOverloadThenSucceeds(t *testing.T) {
	req, resp := fakeReqResp(529)
	fc := &fakeAnthropic{
		errs:      []error{&anthropic.Error{StatusCode: 529, Request: req, Response: resp}, nil},
		responses: []string{textMessage(`{"findings":[]}`)},
	}
	var progress []string
	a := &anthropicAgent{client: fc, model: "claude-test"}
	out, err := a.Review(stdctx.Background(), Context{
		Text:          "ctx",
		RepoDir:       t.TempDir(),
		ProviderRetry: fastProviderRetry(2),
		Progress:      func(s string) { progress = append(progress, s) },
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(out.Findings) != 0 {
		t.Fatalf("findings = %d, want 0", len(out.Findings))
	}
	if len(fc.seen) != 2 || fc.calls != 1 {
		t.Fatalf("seen=%d calls=%d, want 2 attempts/1 success", len(fc.seen), fc.calls)
	}
	if !containsProgress(progress, "provider retry 1/2") {
		t.Fatalf("retry progress missing: %#v", progress)
	}
}

// Hits-injected: a non-empty SemanticContext must reach the Anthropic user turn
// (the advisory header + body), proving the lockstep field threads agent.go's
// BuildUserPrompt call. Mirrors the OpenAI provider test.
func TestAnthropicAgentInjectsSemanticContext(t *testing.T) {
	fc := &fakeAnthropic{responses: []string{textMessage(`{"findings":[]}`)}}
	a := &anthropicAgent{client: fc, model: "claude-test"}
	advisory := "- [bug] prior off-by-one"
	if _, err := a.Review(stdctx.Background(), Context{Text: "ctx", SemanticContext: advisory, RepoDir: t.TempDir()}); err != nil {
		t.Fatalf("Review: %v", err)
	}
	raw, _ := json.Marshal(fc.seen[0])
	if !strings.Contains(string(raw), semanticAdvisoryHeader) {
		t.Fatalf("advisory header missing from Anthropic user turn: %s", raw)
	}
	if !strings.Contains(string(raw), advisory) {
		t.Fatalf("advisory body missing from Anthropic user turn: %s", raw)
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
	out, err := a.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(out.Findings) != 0 {
		t.Fatalf("want 0 findings, got %d", len(out.Findings))
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

// toolHungryAnthropic always asks for a tool WHILE tools are offered; once the
// loop withdraws them on the forced final turn it returns findings (if scripted),
// modelling a real model that keeps exploring a large diff until forced to answer.
type toolHungryAnthropic struct {
	findings string // returned once tools are withdrawn; "" => degenerate (never finalizes)
	calls    int
}

func (f *toolHungryAnthropic) newMessage(_ stdctx.Context, params anthropic.MessageNewParams) (*anthropic.Message, error) {
	f.calls++
	body := toolUseMessage(fmt.Sprintf("tu_%d", f.calls), "grep", map[string]any{"pattern": "x"})
	if len(params.Tools) == 0 && f.findings != "" {
		body = textMessage(f.findings)
	}
	var msg anthropic.Message
	if err := json.Unmarshal([]byte(body), &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

var _ anthropicClient = (*toolHungryAnthropic)(nil)

// On a large diff the model may keep requesting tools to the budget. The loop
// must FORCE finalization on the last turn (withdraw tools + nudge) so the model
// is driven to emit findings, a real review, not a hard maxToolTurns failure.
func TestAnthropicAgentForcedFinalizeReturnsFindings(t *testing.T) {
	fc := &toolHungryAnthropic{findings: `{"findings":[{"file":"a.go","existing_code":"x","severity":"warning","category":"bug","rationale":"r"}]}`}
	a := &anthropicAgent{client: fc, model: "claude-test"}
	out, err := a.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	// Findings only come back if the loop withdrew tools on the final turn.
	if len(out.Findings) != 1 {
		t.Fatalf("want 1 finding from forced finalize, got %d", len(out.Findings))
	}
	if fc.calls != maxToolTurns {
		t.Fatalf("expected %d calls (explore to budget then finalize), got %d", maxToolTurns, fc.calls)
	}
}

// Genuinely-degenerate case: the model returns neither tools-less findings nor
// parseable JSON even when forced. The loop must still error (never silently
// return empty findings, which would read as "no issues").
func TestAnthropicAgentForcedFinalizeStillErrors(t *testing.T) {
	fc := &toolHungryAnthropic{} // never finalizes
	a := &anthropicAgent{client: fc, model: "claude-test"}
	_, err := a.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "finalization") {
		t.Fatalf("expected forced-finalization error, got %v", err)
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

// A proven 401 from the SDK surfaces the typed auth_failed code through Review,
// with a login hint and no leaked token.
func TestAnthropicAgentClassifies401(t *testing.T) {
	req, resp := fakeReqResp(401)
	a := &anthropicAgent{
		client: &fakeAnthropic{err: &anthropic.Error{StatusCode: 401, Request: req, Response: resp}},
		model:  "claude-test",
	}
	_, err := a.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir()})
	ce, ok := err.(*clierr.CLIError)
	if !ok {
		t.Fatalf("want *clierr.CLIError, got %T: %v", err, err)
	}
	if ce.Code != "agent.auth_failed" || ce.Hint == "" {
		t.Fatalf("got %+v, want auth_failed+hint", ce)
	}
}

// RepairPatch issues one tools-less, low-token completion and returns the
// fence-stripped reply; the request must carry repairSystemPrompt + the span and
// NO tools.
func TestAnthropicAgentRepairPatch(t *testing.T) {
	fc := &fakeAnthropic{responses: []string{textMessage("```go\nval, ok := m[key]\n```")}}
	a := &anthropicAgent{client: fc, model: "claude-test"}
	out, _, err := a.RepairPatch(stdctx.Background(), RepairRequest{Span: "val := m[key]", Rationale: "missing check", Category: "bug", Severity: "high"})
	if err != nil {
		t.Fatalf("RepairPatch: %v", err)
	}
	if out != "val, ok := m[key]" {
		t.Fatalf("reply not fence-stripped: %q", out)
	}
	raw, _ := json.Marshal(fc.seen[0])
	if !strings.Contains(string(raw), "val := m[key]") {
		t.Fatalf("span missing from repair request: %s", raw)
	}
	if fc.seen[0].MaxTokens != repairMaxTokens {
		t.Fatalf("repair must use low max tokens, got %d", fc.seen[0].MaxTokens)
	}
	if !fc.seen[0].Temperature.Valid() || fc.seen[0].Temperature.Value != 0 {
		t.Fatalf("repair temperature = %+v, want 0", fc.seen[0].Temperature)
	}
	if len(fc.seen[0].Tools) != 0 {
		t.Fatalf("repair must offer no tools, got %d", len(fc.seen[0].Tools))
	}
	if got := fc.seen[0].System[0].Text; got != repairSystemPrompt {
		t.Fatalf("repair must use repairSystemPrompt, got %q", got)
	}
}

// A surfaced API error from RepairPatch must be wrapped through the same
// classifier as Review (consistent error taxonomy).
func TestAnthropicAgentRepairPatchErrorWrapped(t *testing.T) {
	a := &anthropicAgent{
		client: &fakeAnthropic{err: fmt.Errorf("401 x-api-key: %s invalid", secretToken)},
		model:  "claude-test",
	}
	_, _, err := a.RepairPatch(stdctx.Background(), RepairRequest{Span: "x"})
	if err == nil || !strings.Contains(err.Error(), "messages.new") {
		t.Fatalf("expected wrapped error, got %v", err)
	}
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
