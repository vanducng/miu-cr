package agent

import (
	stdctx "context"
	"testing"

	"github.com/vanducng/miu-cr/internal/config"
)

func floatPtr(f float64) *float64 { return &f }

func TestAnthropicUsesTemperature(t *testing.T) {
	// Default (zero value) → temperature 0 in the request (deterministic reviews).
	fc := &fakeAnthropic{responses: []string{textMessage(`{"findings":[]}`)}}
	a := &anthropicAgent{client: fc, model: "claude-test"}
	if _, err := a.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir()}); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if got := fc.seen[0].Temperature; !got.Valid() || got.Value != 0 {
		t.Fatalf("default temperature must be 0, got %v", got)
	}
	// A configured temperature is forwarded.
	fc2 := &fakeAnthropic{responses: []string{textMessage(`{"findings":[]}`)}}
	a2 := &anthropicAgent{client: fc2, model: "claude-test", temperature: 0.3}
	if _, err := a2.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir()}); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if got := fc2.seen[0].Temperature; !got.Valid() || got.Value != 0.3 {
		t.Fatalf("configured temperature must be forwarded, got %v", got)
	}
}

func TestOpenAIUsesTemperature(t *testing.T) {
	fc := &fakeOpenAI{responses: []string{textCompletion(`{"findings":[]}`)}}
	a := &openaiAgent{client: fc, model: "gpt-test"}
	if _, err := a.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir()}); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if got := fc.seen[0].Temperature; !got.Valid() || got.Value != 0 {
		t.Fatalf("default temperature must be 0, got %v", got)
	}
	fc2 := &fakeOpenAI{responses: []string{textCompletion(`{"findings":[]}`)}}
	a2 := &openaiAgent{client: fc2, model: "gpt-test", temperature: 0.7}
	if _, err := a2.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir()}); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if got := fc2.seen[0].Temperature; !got.Valid() || got.Value != 0.7 {
		t.Fatalf("configured temperature must be forwarded, got %v", got)
	}
}

func TestResolveTemperatureFromConfig(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-x")
	// No [review].temperature → deterministic default 0.
	creds, err := resolveWith(config.Defaults(), ResolveInput{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.Temperature != 0 {
		t.Fatalf("default temperature must be 0, got %v", creds.Temperature)
	}
	// [review].temperature override flows onto the credentials.
	cfg := config.Merge(config.Defaults(), config.Config{Review: config.Review{Temperature: floatPtr(0.5)}})
	creds2, err := resolveWith(cfg, ResolveInput{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds2.Temperature != 0.5 {
		t.Fatalf("config temperature must reach credentials, got %v", creds2.Temperature)
	}
}
