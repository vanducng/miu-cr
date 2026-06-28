package agent

import (
	stdctx "context"
	"encoding/json"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine"
)

// thinkingMessage returns a fake Anthropic Message JSON that contains a thinking
// block followed by a text block (mirrors the real API when extended thinking is on).
func thinkingMessage(thinkingText, content string) string {
	b, _ := json.Marshal(map[string]any{
		"id":          "msg_think",
		"type":        "message",
		"role":        "assistant",
		"model":       "claude-test",
		"stop_reason": "end_turn",
		"content": []map[string]any{
			{"type": "thinking", "thinking": thinkingText, "signature": "sig"},
			{"type": "text", "text": content},
		},
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 20},
	})
	return string(b)
}

// textCompletionWithReasoningTokens returns a fake OpenAI ChatCompletion with
// reasoning_tokens set in completion_tokens_details.
func textCompletionWithReasoningTokens(content string, reasoningTokens int64) string {
	b, _ := json.Marshal(map[string]any{
		"id":      "cmpl-r",
		"object":  "chat.completion",
		"created": 1,
		"model":   "gpt-test",
		"choices": []map[string]any{{
			"index":         0,
			"finish_reason": "stop",
			"message":       map[string]any{"role": "assistant", "content": content},
		}},
		"usage": map[string]any{
			"prompt_tokens":     5,
			"completion_tokens": 10,
			"completion_tokens_details": map[string]any{
				"reasoning_tokens": reasoningTokens,
			},
		},
	})
	return string(b)
}

// Anthropic thinking block + thinking enabled + CaptureReasoning on → trace has reasoning step.
func TestReasoning_AnthropicCapturesThinkingBlock(t *testing.T) {
	wantThinking := "step by step: x is y because z"
	fc := &fakeAnthropic{responses: []string{thinkingMessage(wantThinking, `{"findings":[]}`)}}
	a := &anthropicAgent{client: fc, model: "claude-sonnet-4-5", thinking: "auto"}
	tr := &engine.ReviewTrace{}
	_, err := a.Review(stdctx.Background(), Context{
		Text:             "diff",
		RepoDir:          t.TempDir(),
		Trace:            tr,
		CaptureReasoning: true,
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if tr.Reasoning == nil {
		t.Fatal("reasoning not captured")
	}
	if tr.Reasoning.Provider != "anthropic" {
		t.Errorf("provider: got %q, want anthropic", tr.Reasoning.Provider)
	}
	if tr.Reasoning.Text != wantThinking {
		t.Errorf("reasoning text: got %q, want %q", tr.Reasoning.Text, wantThinking)
	}
}

// CaptureReasoning=false (default) → no reasoning step; trace otherwise unchanged.
func TestReasoning_OffByDefault_NoReasoningStep(t *testing.T) {
	fc := &fakeAnthropic{responses: []string{thinkingMessage("private thoughts", `{"findings":[]}`)}}
	a := &anthropicAgent{client: fc, model: "claude-sonnet-4-5", thinking: "auto"}
	tr := &engine.ReviewTrace{}
	_, err := a.Review(stdctx.Background(), Context{
		Text:             "diff",
		RepoDir:          t.TempDir(),
		Trace:            tr,
		CaptureReasoning: false, // explicit off
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if tr.Reasoning != nil {
		t.Fatalf("reasoning must not be captured when CaptureReasoning=false, got: %+v", tr.Reasoning)
	}
}

// OpenAI fake usage with reasoning_tokens → reasoning step with count + [hidden by provider].
func TestReasoning_OpenAICapturesTokenCount(t *testing.T) {
	fc := &fakeOpenAI{responses: []string{textCompletionWithReasoningTokens(`{"findings":[]}`, 42)}}
	a := &openaiAgent{client: fc, model: "o3-mini", thinking: "auto"}
	tr := &engine.ReviewTrace{}
	_, err := a.Review(stdctx.Background(), Context{
		Text:             "diff",
		RepoDir:          t.TempDir(),
		Trace:            tr,
		CaptureReasoning: true,
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if tr.Reasoning == nil {
		t.Fatal("reasoning not captured for OpenAI")
	}
	if tr.Reasoning.Provider != "openai" {
		t.Errorf("provider: got %q, want openai", tr.Reasoning.Provider)
	}
	if tr.Reasoning.Text != "[hidden by provider]" {
		t.Errorf("text: got %q, want [hidden by provider]", tr.Reasoning.Text)
	}
	if tr.Reasoning.Tokens != 42 {
		t.Errorf("tokens: got %d, want 42", tr.Reasoning.Tokens)
	}
}

// OpenAI with zero reasoning tokens (non-reasoning model or thinking off) → no step.
func TestReasoning_OpenAIZeroTokens_NoStep(t *testing.T) {
	fc := &fakeOpenAI{responses: []string{textCompletionWithReasoningTokens(`{"findings":[]}`, 0)}}
	a := &openaiAgent{client: fc, model: "gpt-4o"}
	tr := &engine.ReviewTrace{}
	_, err := a.Review(stdctx.Background(), Context{
		Text:             "diff",
		RepoDir:          t.TempDir(),
		Trace:            tr,
		CaptureReasoning: true,
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if tr.Reasoning != nil {
		t.Fatalf("reasoning must not be captured when reasoning_tokens=0, got: %+v", tr.Reasoning)
	}
}

// SetReasoning is first-non-empty-wins: multiple dispatch turns record only once.
func TestReasoning_FirstNonEmptyWins(t *testing.T) {
	tr := &engine.ReviewTrace{}
	tr.SetReasoning("anthropic", "first thoughts", 0)
	tr.SetReasoning("anthropic", "second thoughts", 0)
	if tr.Reasoning.Text != "first thoughts" {
		t.Errorf("want first thoughts, got %q", tr.Reasoning.Text)
	}
}

// SetReasoning with empty text is a no-op.
func TestReasoning_EmptyTextIsNoOp(t *testing.T) {
	tr := &engine.ReviewTrace{}
	tr.SetReasoning("anthropic", "", 0)
	if tr.Reasoning != nil {
		t.Fatalf("empty text must not set reasoning: %+v", tr.Reasoning)
	}
}

// SetReasoning is nil-safe.
func TestReasoning_NilTraceSafe(t *testing.T) {
	var tr *engine.ReviewTrace
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil Trace panicked: %v", r)
		}
	}()
	tr.SetReasoning("anthropic", "thoughts", 0) // must not panic
}
