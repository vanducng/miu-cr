package agent

import (
	"errors"
	"testing"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	"github.com/vanducng/miu-cr/internal/config"
)

// clearProviderEnv unsets every credential env var so each case starts clean.
func clearProviderEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"ANTHROPIC_API_KEY", "ANTHROPIC_MODEL", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN",
		"OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_MODEL", "ZAI_API_KEY", "GATEWAY_TOKEN",
	} {
		t.Setenv(k, "")
	}
}

func TestResolveFlagWinsOverEnv(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	creds, err := resolveWith(config.Defaults(), ResolveInput{APIKey: "flag-key"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.Kind != config.KindAnthropic {
		t.Fatalf("want anthropic, got %q", creds.Kind)
	}
	if creds.APIKey != "flag-key" {
		t.Fatalf("flag must win: got %q", creds.APIKey)
	}
	if creds.Model != config.DefaultAnthropicModel {
		t.Fatalf("default model expected, got %q", creds.Model)
	}
}

func TestResolveEnvKeyAndModel(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	t.Setenv("ANTHROPIC_MODEL", "custom-model")
	creds, err := resolveWith(config.Defaults(), ResolveInput{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
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
	_, err := resolveWith(config.Defaults(), ResolveInput{APIKey: "   ", Provider: "anthropic"})
	if err == nil {
		t.Fatal("expected error for missing credentials")
	}
	var cerr *clierr.CLIError
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *clierr.CLIError, got %T", err)
	}
	if cerr.Code != "agent.no_credentials" {
		t.Fatalf("unexpected code %q", cerr.Code)
	}
}

// z.ai via a CONFIG PROFILE (no hardcoding): a named anthropic-kind profile with
// a gateway base_url and auth_env resolves to the Anthropic kind, sends the key
// as a Bearer auth token, and never as x-api-key.
func TestResolveZAIViaConfigProfile(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("ZAI_API_KEY", "zai-secret")
	cfg := config.Merge(config.Defaults(), config.Config{
		Providers: map[string]config.Provider{
			"zai": {Kind: config.KindAnthropic, BaseURL: "https://api.z.ai/api/anthropic", Model: "glm-4.6", AuthEnv: "ZAI_API_KEY"},
		},
	})
	creds, err := resolveWith(cfg, ResolveInput{Provider: "zai"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.Kind != config.KindAnthropic {
		t.Fatalf("z.ai profile must route through anthropic, got %q", creds.Kind)
	}
	if creds.AuthToken != "zai-secret" {
		t.Fatalf("profile key must become Bearer AuthToken, got %q", creds.AuthToken)
	}
	if creds.APIKey != "" {
		t.Fatalf("APIKey must be empty on the bearer path, got %q", creds.APIKey)
	}
	if creds.BaseURL != "https://api.z.ai/api/anthropic" {
		t.Fatalf("profile base URL not honored, got %q", creds.BaseURL)
	}
	if creds.Model != "glm-4.6" {
		t.Fatalf("profile model not honored, got %q", creds.Model)
	}
}

// A literal auth_token in a profile is honored when its named env var is unset.
func TestResolveProfileLiteralAuthToken(t *testing.T) {
	clearProviderEnv(t)
	cfg := config.Merge(config.Defaults(), config.Config{
		Providers: map[string]config.Provider{
			"gw": {Kind: config.KindAnthropic, BaseURL: "https://gw.example/anthropic", AuthToken: "literal-bearer"},
		},
	})
	creds, err := resolveWith(cfg, ResolveInput{Provider: "gw"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.AuthToken != "literal-bearer" || creds.BaseURL != "https://gw.example/anthropic" {
		t.Fatalf("literal profile auth not honored: %+v", creds)
	}
}

// env overrides a profile credential (layering: flags > env > file > defaults).
func TestResolveEnvBeatsProfile(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "env-token")
	cfg := config.Merge(config.Defaults(), config.Config{
		Providers: map[string]config.Provider{
			"gw": {Kind: config.KindAnthropic, BaseURL: "https://gw.example/anthropic", AuthToken: "file-token"},
		},
	})
	creds, err := resolveWith(cfg, ResolveInput{Provider: "gw"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.AuthToken != "env-token" {
		t.Fatalf("env must override file profile token, got %q", creds.AuthToken)
	}
}

// Explicit Anthropic base URL + auth token via env (a generic gateway).
func TestResolveAnthropicBaseURLAndAuthToken(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("ANTHROPIC_BASE_URL", "https://gateway.example/anthropic")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "bearer-xyz")
	creds, err := resolveWith(config.Defaults(), ResolveInput{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
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
	creds, err := resolveWith(config.Defaults(), ResolveInput{BaseURL: "https://flag.example", AuthToken: "flag-token"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
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
	creds, err := resolveWith(config.Defaults(), ResolveInput{Provider: "openai"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.Kind != config.KindOpenAI {
		t.Fatalf("want openai, got %q", creds.Kind)
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

// A configured OpenAI-compatible gateway: kind openai with a custom base_url and
// a profile key, no env at all.
func TestResolveOpenAICompatProfile(t *testing.T) {
	clearProviderEnv(t)
	cfg := config.Merge(config.Defaults(), config.Config{
		Providers: map[string]config.Provider{
			"deepseek": {Kind: config.KindOpenAI, BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-chat", AuthEnv: "GATEWAY_TOKEN"},
		},
	})
	t.Setenv("GATEWAY_TOKEN", "ds-key")
	creds, err := resolveWith(cfg, ResolveInput{Provider: "deepseek"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.Kind != config.KindOpenAI || creds.APIKey != "ds-key" {
		t.Fatalf("openai-compat profile not resolved: %+v", creds)
	}
	if creds.BaseURL != "https://api.deepseek.com/v1" || creds.Model != "deepseek-chat" {
		t.Fatalf("openai-compat base/model not honored: %+v", creds)
	}
}

func TestResolveOpenAIMissingKey(t *testing.T) {
	clearProviderEnv(t)
	_, err := resolveWith(config.Defaults(), ResolveInput{Provider: "openai"})
	var cerr *clierr.CLIError
	if !errors.As(err, &cerr) || cerr.Code != "agent.no_credentials" {
		t.Fatalf("expected no_credentials, got %v", err)
	}
}

// --auth-token is Anthropic-only: pairing it with an OpenAI provider is a typed
// error, not a silent ignore.
func TestResolveAuthTokenOpenAIRejected(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "oai-key")
	_, err := resolveWith(config.Defaults(), ResolveInput{Provider: "openai", AuthToken: "should-not-be-here"})
	var cerr *clierr.CLIError
	if !errors.As(err, &cerr) || cerr.Code != "agent.auth_token_unsupported" {
		t.Fatalf("expected auth_token_unsupported, got %v", err)
	}
}

// An unknown --provider name (no built-in, no profile) is a typed error.
func TestResolveUnknownProvider(t *testing.T) {
	clearProviderEnv(t)
	_, err := resolveWith(config.Defaults(), ResolveInput{Provider: "nope"})
	var cerr *clierr.CLIError
	if !errors.As(err, &cerr) || cerr.Code != "agent.unknown_provider" {
		t.Fatalf("expected unknown_provider, got %v", err)
	}
}

// auto-detect: OPENAI_API_KEY present and no Anthropic credential => openai.
func TestResolveAutoDetectOpenAI(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "oai-key")
	creds, err := resolveWith(config.Defaults(), ResolveInput{Provider: "auto"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.Kind != config.KindOpenAI {
		t.Fatalf("auto must pick openai, got %q", creds.Kind)
	}
}

// auto-detect: both keys present => anthropic (the sensible default).
func TestResolveAutoDetectPrefersAnthropic(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "oai-key")
	t.Setenv("ANTHROPIC_API_KEY", "ant-key")
	creds, err := resolveWith(config.Defaults(), ResolveInput{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.Kind != config.KindAnthropic {
		t.Fatalf("auto must prefer anthropic when both set, got %q", creds.Kind)
	}
}

// config.default_provider selects the profile when nothing else forces a choice.
func TestResolveDefaultProviderFromConfig(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "oai-key")
	cfg := config.Defaults()
	cfg.DefaultProvider = "openai"
	// Anthropic key also present, so env auto-detect would NOT force openai;
	// the config default decides.
	t.Setenv("ANTHROPIC_API_KEY", "ant-key")
	creds, err := resolveWith(cfg, ResolveInput{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.Kind != config.KindOpenAI {
		t.Fatalf("config default_provider must select openai, got %q", creds.Kind)
	}
}

// Tokens must never appear persisted; resolution only returns in-memory creds.
func TestResolveDoesNotEmitPersistableFields(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sekret")
	creds, err := resolveWith(config.Defaults(), ResolveInput{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.APIKey != "sekret" {
		t.Fatalf("expected in-memory key, got %q", creds.APIKey)
	}
}

// Resolve (full path, no config file present) falls back to built-in defaults
// and resolves an Anthropic key from env. Isolate HOME so a developer machine's
// real ~/.config/miu/cr/config.toml can't perturb the result.
func TestResolveEndToEndDefaults(t *testing.T) {
	clearProviderEnv(t)
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("ANTHROPIC_API_KEY", "ant-key")
	creds, err := Resolve(ResolveInput{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if creds.Kind != config.KindAnthropic || creds.APIKey != "ant-key" {
		t.Fatalf("end-to-end resolve: %+v", creds)
	}
}
