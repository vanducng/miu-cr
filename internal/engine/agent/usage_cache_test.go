package agent

import (
	stdctx "context"
	"encoding/json"
	"testing"
)

func anthMsgWithUsage(content string, in, out, cacheRead, cacheCreate int64) string {
	b, _ := json.Marshal(map[string]any{
		"id":          "msg_u",
		"type":        "message",
		"role":        "assistant",
		"model":       "claude-test",
		"stop_reason": "end_turn",
		"content":     []map[string]any{{"type": "text", "text": content}},
		"usage": map[string]any{
			"input_tokens":                in,
			"output_tokens":               out,
			"cache_read_input_tokens":     cacheRead,
			"cache_creation_input_tokens": cacheCreate,
		},
	})
	return string(b)
}

func openAICompletionWithUsage(content string, prompt, completion, cached int64) string {
	b, _ := json.Marshal(map[string]any{
		"id":      "cmpl_u",
		"object":  "chat.completion",
		"created": 1,
		"model":   "gpt-test",
		"choices": []map[string]any{{
			"index":         0,
			"finish_reason": "stop",
			"message":       map[string]any{"role": "assistant", "content": content},
		}},
		"usage": map[string]any{
			"prompt_tokens":         prompt,
			"completion_tokens":     completion,
			"total_tokens":          prompt + completion,
			"prompt_tokens_details": map[string]any{"cached_tokens": cached},
		},
	})
	return string(b)
}

// Anthropic reports cache as separate buckets OUTSIDE input_tokens; the agent must
// keep InputTokens uncached and capture both cache buckets, not drop them.
func TestAnthropicAgentCapturesCacheUsage(t *testing.T) {
	body := anthMsgWithUsage(`{"findings":[]}`, 10, 20, 300, 50)
	a := &anthropicAgent{client: &fakeAnthropic{responses: []string{body}}, model: "claude-test"}
	out, err := a.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	u := out.Usage
	if u.InputTokens != 10 || u.OutputTokens != 20 || u.CacheReadTokens != 300 || u.CacheCreationTokens != 50 {
		t.Fatalf("anthropic usage not normalized: %+v", u)
	}
	if u.TotalInputTokens() != 360 || u.TotalTokens() != 380 {
		t.Fatalf("totals wrong: in=%d total=%d", u.TotalInputTokens(), u.TotalTokens())
	}
	if got := u.CacheHitRatio(); got < 0.83 || got > 0.84 { // 300/360
		t.Fatalf("cache-hit ratio = %v, want ~0.833", got)
	}
}

// OpenAI reports cached_tokens as a SUB-COUNT of prompt_tokens; the agent must
// subtract it so InputTokens is the uncached remainder (no double-count).
func TestOpenAIAgentNormalizesCacheUsage(t *testing.T) {
	body := openAICompletionWithUsage(`{"findings":[]}`, 1000, 50, 800)
	a := &openaiAgent{client: &fakeOpenAI{responses: []string{body}}, model: "gpt-test"}
	out, err := a.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	u := out.Usage
	if u.InputTokens != 200 || u.OutputTokens != 50 || u.CacheReadTokens != 800 || u.CacheCreationTokens != 0 {
		t.Fatalf("openai usage not normalized: %+v", u)
	}
	if u.TotalInputTokens() != 1000 {
		t.Fatalf("total input = %d, want 1000 (uncached+cached)", u.TotalInputTokens())
	}
	if got := u.CacheHitRatio(); got != 0.8 { // 800/1000
		t.Fatalf("cache-hit ratio = %v, want 0.8", got)
	}
}
