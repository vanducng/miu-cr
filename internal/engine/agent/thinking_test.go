package agent

import (
	stdctx "context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openai/openai-go/v3/shared"

	"github.com/vanducng/miu-cr/internal/config"
)

func TestThinkingSettingParse(t *testing.T) {
	tests := []struct {
		in     string
		wantOn bool
		effort string
	}{
		{"off", false, ""},
		{"", true, "medium"},
		{"auto", true, "medium"},
		{"low", true, "low"},
		{"high", true, "high"},
	}
	for _, tt := range tests {
		on, eff := thinkingSetting(tt.in)
		if on != tt.wantOn || (on && eff != tt.effort) {
			t.Fatalf("thinkingSetting(%q) = (%v,%q), want (%v,%q)", tt.in, on, eff, tt.wantOn, tt.effort)
		}
	}
}

func TestSupportsAnthropicThinking(t *testing.T) {
	for _, m := range []string{"claude-sonnet-4-5", "claude-opus-4-1", "claude-3-7-sonnet-latest"} {
		if !supportsAnthropicThinking(m) {
			t.Fatalf("want thinking-capable: %s", m)
		}
	}
	for _, m := range []string{"claude-3-5-sonnet", "glm-4.6", "gpt-4o"} {
		if supportsAnthropicThinking(m) {
			t.Fatalf("want NOT thinking-capable: %s", m)
		}
	}
}

func TestIsOpenAIReasoningModel(t *testing.T) {
	for _, m := range []string{"o1", "o3-mini", "o4-mini", "gpt-5", "gpt-5.5"} {
		if !isOpenAIReasoningModel(m) {
			t.Fatalf("want reasoning model: %s", m)
		}
	}
	for _, m := range []string{"gpt-4o", "gpt-4.1", "glm-4.6", "gpt-5-chat-latest"} {
		if isOpenAIReasoningModel(m) {
			t.Fatalf("want NOT reasoning model: %s", m)
		}
	}
}

func TestAnthropicThinkingEnabledOmitsTemperature(t *testing.T) {
	fc := &fakeAnthropic{responses: []string{textMessage(`{"findings":[]}`)}}
	a := &anthropicAgent{client: fc, model: "claude-sonnet-4-5", temperature: 0, thinking: "auto"}
	if _, err := a.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir()}); err != nil {
		t.Fatalf("Review: %v", err)
	}
	p := fc.seen[0]
	budget := p.Thinking.GetBudgetTokens()
	if budget == nil {
		t.Fatalf("thinking-capable model must enable extended thinking")
	}
	if p.Temperature.Valid() {
		t.Fatalf("temperature must be omitted when thinking is on, got %v", p.Temperature)
	}
	if p.MaxTokens <= *budget {
		t.Fatalf("max_tokens (%d) must exceed the thinking budget (%d)", p.MaxTokens, *budget)
	}
}

func TestAnthropicThinkingOffUsesTemperature(t *testing.T) {
	fc := &fakeAnthropic{responses: []string{textMessage(`{"findings":[]}`)}}
	a := &anthropicAgent{client: fc, model: "claude-sonnet-4-5", temperature: 0, thinking: "off"}
	if _, err := a.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir()}); err != nil {
		t.Fatalf("Review: %v", err)
	}
	p := fc.seen[0]
	if p.Thinking.GetBudgetTokens() != nil {
		t.Fatalf("thinking=off must not enable thinking")
	}
	if !p.Temperature.Valid() {
		t.Fatalf("thinking=off must apply temperature")
	}
}

func TestOpenAIReasoningModelOmitsTemperatureSetsEffort(t *testing.T) {
	fc := &fakeOpenAI{responses: []string{textCompletion(`{"findings":[]}`)}}
	a := &openaiAgent{client: fc, model: "o3-mini", temperature: 0, thinking: "high"}
	if _, err := a.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir()}); err != nil {
		t.Fatalf("Review: %v", err)
	}
	p := fc.seen[0]
	if p.Temperature.Valid() {
		t.Fatalf("reasoning model must omit temperature (it would 400), got %v", p.Temperature)
	}
	if p.ReasoningEffort != shared.ReasoningEffortHigh {
		t.Fatalf("want reasoning_effort high, got %q", p.ReasoningEffort)
	}
	// Reasoning models require max_completion_tokens, not max_tokens (which they 400 on).
	if !p.MaxCompletionTokens.Valid() {
		t.Fatalf("reasoning model must send max_completion_tokens")
	}
	if p.MaxTokens.Valid() {
		t.Fatalf("reasoning model must NOT send max_tokens (it would 400), got %v", p.MaxTokens)
	}
}

func TestCodexSetsReasoningEffort(t *testing.T) {
	var got codexReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		io.WriteString(w, codexMessageResp(codexFindingsJSON))
	}))
	defer srv.Close()
	creds := Credentials{
		Kind: config.KindOpenAI, Backend: "codex", OAuthToken: "t", OAuthAccountID: "a",
		BaseURL: srv.URL, Model: "gpt-test", HTTPClient: srv.Client(), Thinking: "high",
	}
	a, _ := newCodexAgentFromNew(t, creds)
	if _, err := a.Review(stdctx.Background(), Context{Text: "diff --git a/x b/x"}); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if got.Reasoning == nil || got.Reasoning.Effort != "high" {
		t.Fatalf("codex must set reasoning effort, got %+v", got.Reasoning)
	}
}

func TestResolveThinkingFromConfig(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-x")
	creds, err := resolveWith(config.Merge(config.Defaults(), config.Config{Review: config.Review{Thinking: "high"}}), ResolveInput{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.Thinking != "high" {
		t.Fatalf("config thinking must reach credentials, got %q", creds.Thinking)
	}
}
