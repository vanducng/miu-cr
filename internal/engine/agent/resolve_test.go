package agent

import (
	"errors"
	"testing"

	"github.com/vanducng/miu-cr/internal/cli"
)

// clearProviderEnv unsets every credential env var so each case starts clean.
func clearProviderEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"ANTHROPIC_API_KEY", "ANTHROPIC_MODEL", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN",
		"ZAI_API_KEY", "OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_MODEL",
	} {
		t.Setenv(k, "")
	}
}

func TestResolveFlagWinsOverEnv(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	creds, err := Resolve(ResolveInput{APIKey: "flag-key"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if creds.Provider != ProviderAnthropic {
		t.Fatalf("want anthropic, got %q", creds.Provider)
	}
	if creds.APIKey != "flag-key" {
		t.Fatalf("flag must win: got %q", creds.APIKey)
	}
	if creds.Model != defaultModel {
		t.Fatalf("default model expected, got %q", creds.Model)
	}
}

func TestResolveEnvKeyAndModel(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	t.Setenv("ANTHROPIC_MODEL", "custom-model")
	creds, err := Resolve(ResolveInput{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if creds.APIKey != "env-key" {
		t.Fatalf("env key expected, got %q", creds.APIKey)
	}
	if creds.Model != "custom-model" {
		t.Fatalf("env model expected, got %q", creds.Model)
	}
}

func TestResolveMissingKeyTypedError(t *testing.T) {
	clearProviderEnv(t)
	_, err := Resolve(ResolveInput{APIKey: "   ", Provider: "anthropic"})
	if err == nil {
		t.Fatal("expected error for missing credentials")
	}
	var cerr *cli.CLIError
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *cli.CLIError, got %T", err)
	}
	if cerr.Code != "agent.no_credentials" {
		t.Fatalf("unexpected code %q", cerr.Code)
	}
}

// z.ai: ZAI_API_KEY routes through the Anthropic client as a Bearer auth token
// against the Anthropic-compatible gateway base URL.
func TestResolveZAIGateway(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("ZAI_API_KEY", "zai-secret")
	creds, err := Resolve(ResolveInput{Model: "glm-5.2"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if creds.Provider != ProviderAnthropic {
		t.Fatalf("z.ai must route through anthropic, got %q", creds.Provider)
	}
	if creds.AuthToken != "zai-secret" {
		t.Fatalf("ZAI key must become AuthToken, got %q", creds.AuthToken)
	}
	if creds.APIKey != "" {
		t.Fatalf("APIKey must be empty for the bearer-token path, got %q", creds.APIKey)
	}
	if creds.BaseURL != "https://api.z.ai/api/anthropic" {
		t.Fatalf("z.ai base URL not set, got %q", creds.BaseURL)
	}
	if creds.Model != "glm-5.2" {
		t.Fatalf("model flag not honored, got %q", creds.Model)
	}
}

// Explicit Anthropic base URL + auth token via env (gateway not z.ai).
func TestResolveAnthropicBaseURLAndAuthToken(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("ANTHROPIC_BASE_URL", "https://gateway.example/anthropic")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "bearer-xyz")
	creds, err := Resolve(ResolveInput{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if creds.BaseURL != "https://gateway.example/anthropic" {
		t.Fatalf("base URL not resolved, got %q", creds.BaseURL)
	}
	if creds.AuthToken != "bearer-xyz" {
		t.Fatalf("auth token not resolved, got %q", creds.AuthToken)
	}
}

// Flags override env on the gateway path too.
func TestResolveBaseURLAuthTokenFlagsWin(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("ANTHROPIC_BASE_URL", "https://env.example")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "env-token")
	creds, err := Resolve(ResolveInput{BaseURL: "https://flag.example", AuthToken: "flag-token"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if creds.BaseURL != "https://flag.example" || creds.AuthToken != "flag-token" {
		t.Fatalf("flags must win: %+v", creds)
	}
}

func TestResolveOpenAIExplicit(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "oai-key")
	t.Setenv("OPENAI_BASE_URL", "https://oai.example/v1")
	t.Setenv("OPENAI_MODEL", "gpt-test")
	creds, err := Resolve(ResolveInput{Provider: "openai"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if creds.Provider != ProviderOpenAI {
		t.Fatalf("want openai, got %q", creds.Provider)
	}
	if creds.APIKey != "oai-key" {
		t.Fatalf("openai key, got %q", creds.APIKey)
	}
	if creds.BaseURL != "https://oai.example/v1" {
		t.Fatalf("openai base URL, got %q", creds.BaseURL)
	}
	if creds.Model != "gpt-test" {
		t.Fatalf("openai model, got %q", creds.Model)
	}
}

func TestResolveOpenAIMissingKey(t *testing.T) {
	clearProviderEnv(t)
	_, err := Resolve(ResolveInput{Provider: "openai"})
	var cerr *cli.CLIError
	if !errors.As(err, &cerr) || cerr.Code != "agent.no_credentials" {
		t.Fatalf("expected no_credentials, got %v", err)
	}
}

// auto-detect: OPENAI_API_KEY present and no Anthropic credential => openai.
func TestResolveAutoDetectOpenAI(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "oai-key")
	creds, err := Resolve(ResolveInput{Provider: "auto"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if creds.Provider != ProviderOpenAI {
		t.Fatalf("auto must pick openai, got %q", creds.Provider)
	}
}

// auto-detect: both keys present => anthropic (the sensible default).
func TestResolveAutoDetectPrefersAnthropic(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "oai-key")
	t.Setenv("ANTHROPIC_API_KEY", "ant-key")
	creds, err := Resolve(ResolveInput{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if creds.Provider != ProviderAnthropic {
		t.Fatalf("auto must prefer anthropic when both set, got %q", creds.Provider)
	}
}

// auto-detect: only ZAI_API_KEY => anthropic gateway, not openai.
func TestResolveAutoDetectZAIIsAnthropic(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "oai-key")
	t.Setenv("ZAI_API_KEY", "zai-key")
	creds, err := Resolve(ResolveInput{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if creds.Provider != ProviderAnthropic {
		t.Fatalf("ZAI presence must keep anthropic, got %q", creds.Provider)
	}
}

// Tokens must never appear persisted; resolution only returns in-memory creds.
// (Belt-and-suspenders: the store layer never sees Credentials at all.)
func TestResolveDoesNotEmitPersistableFields(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sekret")
	creds, err := Resolve(ResolveInput{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if creds.APIKey != "sekret" {
		t.Fatalf("expected in-memory key, got %q", creds.APIKey)
	}
}
