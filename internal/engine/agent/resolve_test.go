package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
		"MIUCR_CODEX_MODEL",
	} {
		t.Setenv(k, "")
	}
}

func credentialCommand(t *testing.T, script string) []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatalf("write auth command: %v", err)
	}
	return []string{path}
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
	t.Setenv("ANTHROPIC_API_KEY", "wrong-provider-secret")
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

func TestResolveAnthropicAuthCommandBearer(t *testing.T) {
	clearProviderEnv(t)
	cfg := config.Merge(config.Defaults(), config.Config{
		Providers: map[string]config.Provider{
			"zai": {
				Kind:        config.KindAnthropic,
				BaseURL:     "https://api.z.ai/api/anthropic",
				Model:       "glm-5.2",
				Auth:        "bearer",
				AuthCommand: credentialCommand(t, "printf 'cmd-secret\\n'\n"),
			},
		},
	})
	creds, err := resolveWith(cfg, ResolveInput{Provider: "zai"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.AuthToken != "cmd-secret" || creds.APIKey != "" {
		t.Fatalf("auth_command bearer must become AuthToken only: %+v", creds)
	}
}

func TestResolveAnthropicAuthCommandAPIKey(t *testing.T) {
	clearProviderEnv(t)
	cfg := config.Merge(config.Defaults(), config.Config{
		Providers: map[string]config.Provider{
			"anthropic-key": {
				Kind:        config.KindAnthropic,
				Model:       "claude-test",
				Auth:        "api_key",
				AuthCommand: credentialCommand(t, "printf 'cmd-secret\\n'\n"),
			},
		},
	})
	creds, err := resolveWith(cfg, ResolveInput{Provider: "anthropic-key"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.APIKey != "cmd-secret" || creds.AuthToken != "" {
		t.Fatalf("auth_command api_key must become APIKey only: %+v", creds)
	}
}

func TestResolveAnthropicAuthCommandSkippedWhenFlagWins(t *testing.T) {
	clearProviderEnv(t)
	cfg := config.Merge(config.Defaults(), config.Config{
		Providers: map[string]config.Provider{
			"gw": {
				Kind:        config.KindAnthropic,
				BaseURL:     "https://gw.example/anthropic",
				Auth:        "bearer",
				AuthCommand: credentialCommand(t, "echo should-not-run >&2\nexit 42\n"),
			},
		},
	})
	creds, err := resolveWith(cfg, ResolveInput{Provider: "gw", APIKey: "flag-key"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.APIKey != "flag-key" || creds.AuthToken != "" {
		t.Fatalf("flag key must win without running auth_command: %+v", creds)
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

func TestResolveProfileAuthCommand(t *testing.T) {
	clearProviderEnv(t)
	dir := t.TempDir()
	cmdPath := filepath.Join(dir, "token.sh")
	body := "#!/bin/sh\nprintf command-token\n"
	if runtime.GOOS == "windows" {
		cmdPath = filepath.Join(dir, "token.bat")
		body = "@echo off\r\n<nul set /p=command-token\r\n"
	}
	if err := os.WriteFile(cmdPath, []byte(body), 0o700); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	cfg := config.Merge(config.Defaults(), config.Config{
		Providers: map[string]config.Provider{
			"gw": {Kind: config.KindAnthropic, BaseURL: "https://gw.example/anthropic", AuthCommand: []string{cmdPath}},
		},
	})
	creds, err := resolveWith(cfg, ResolveInput{Provider: "gw"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.AuthToken != "command-token" || creds.AuthSource != "auth_command" || creds.AuthSourceName != cmdPath {
		t.Fatalf("auth_command not resolved with source: %+v", creds)
	}
}

func TestResolveProfileAuthCommandFailureOmitsStderr(t *testing.T) {
	clearProviderEnv(t)
	dir := t.TempDir()
	cmdPath := filepath.Join(dir, "fail.sh")
	body := "#!/bin/sh\necho secret-stderr >&2\nexit 7\n"
	if runtime.GOOS == "windows" {
		cmdPath = filepath.Join(dir, "fail.bat")
		body = "@echo secret-stderr 1>&2\r\nexit /b 7\r\n"
	}
	if err := os.WriteFile(cmdPath, []byte(body), 0o700); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	cfg := config.Merge(config.Defaults(), config.Config{
		Providers: map[string]config.Provider{
			"gw": {Kind: config.KindAnthropic, BaseURL: "https://gw.example/anthropic", AuthCommand: []string{cmdPath}},
		},
	})
	_, err := resolveWith(cfg, ResolveInput{Provider: "gw"})
	var cerr *clierr.CLIError
	if !errors.As(err, &cerr) || cerr.Code != "agent.auth_command_failed" {
		t.Fatalf("expected auth_command_failed, got %v", err)
	}
	if strings.Contains(cerr.Message, "secret-stderr") || strings.Contains(cerr.Hint, "secret-stderr") {
		t.Fatalf("stderr leaked: %+v", cerr)
	}
	if cerr.Details["auth_source"] != "auth_command" || cerr.Details["auth_command"] != cmdPath {
		t.Fatalf("details missing source: %+v", cerr.Details)
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
	if creds.AuthSource != "env" || creds.AuthSourceName != "ANTHROPIC_AUTH_TOKEN" {
		t.Fatalf("env source not captured: %+v", creds)
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

func TestResolveOpenAIAuthCommandAPIKey(t *testing.T) {
	clearProviderEnv(t)
	cfg := config.Merge(config.Defaults(), config.Config{
		Providers: map[string]config.Provider{
			"gw": {
				Kind:        config.KindOpenAI,
				BaseURL:     "https://gw.example/v1",
				Model:       "m",
				Auth:        "api_key",
				AuthCommand: credentialCommand(t, "printf 'cmd-secret\\n'\n"),
			},
		},
	})
	creds, err := resolveWith(cfg, ResolveInput{Provider: "gw", OAuthResolver: codexResolver()})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.Backend == "codex" || creds.APIKey != "cmd-secret" {
		t.Fatalf("auth_command must use API-key path, not OAuth: %+v", creds)
	}
}

func TestResolveOpenAIAuthCommandFailureDoesNotUseOAuth(t *testing.T) {
	clearProviderEnv(t)
	called := false
	resolver := func(context.Context) (OAuthCredential, bool, error) {
		called = true
		return OAuthCredential{AccessToken: "oauth-tok"}, true, nil
	}
	cfg := config.Merge(config.Defaults(), config.Config{
		Providers: map[string]config.Provider{
			"gw": {
				Kind:        config.KindOpenAI,
				BaseURL:     "https://gw.example/v1",
				Model:       "m",
				AuthCommand: credentialCommand(t, "printf 'locked secret store\\n' >&2\nexit 7\n"),
			},
		},
	})
	_, err := resolveWith(cfg, ResolveInput{Provider: "gw", OAuthResolver: resolver})
	var cerr *clierr.CLIError
	if !errors.As(err, &cerr) || cerr.Code != "agent.auth_command_failed" {
		t.Fatalf("expected auth_command failure, got %v", err)
	}
	if called {
		t.Fatal("failing auth_command must not silently fall back to OAuth")
	}
}

func TestRunAuthCommandRejectsMultiline(t *testing.T) {
	_, err := runAuthCommand(context.Background(), credentialCommand(t, "printf 'one\\ntwo\\n'\n"))
	var cerr *clierr.CLIError
	if !errors.As(err, &cerr) || cerr.Code != "agent.auth_command_failed" || !strings.Contains(cerr.Message, "multiple lines") {
		t.Fatalf("expected multiline auth_command failure, got %v", err)
	}
}

func TestRunAuthCommandOmitsStderr(t *testing.T) {
	const leaked = "synthetic-secret-value-123456"
	_, err := runAuthCommand(context.Background(), credentialCommand(t, "printf 'x-api-key: "+leaked+"\\n' >&2\nexit 7\n"))
	var cerr *clierr.CLIError
	if !errors.As(err, &cerr) || cerr.Code != "agent.auth_command_failed" {
		t.Fatalf("expected auth_command failure, got %v", err)
	}
	if strings.Contains(cerr.Message, leaked) || strings.Contains(cerr.Message, "x-api-key") {
		t.Fatalf("stderr secret leaked: %q", cerr.Message)
	}
}

func TestRunAuthCommandParentCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runAuthCommand(ctx, credentialCommand(t, "sleep 5\n"))
	var cerr *clierr.CLIError
	if !errors.As(err, &cerr) || cerr.Code != "agent.auth_command_cancelled" {
		t.Fatalf("expected auth_command_cancelled, got %v", err)
	}
}

func TestResolveOpenAIBearerRejected(t *testing.T) {
	clearProviderEnv(t)
	cfg := config.Defaults()
	p := cfg.Providers["openai"]
	p.Auth = "bearer"
	cfg.Providers["openai"] = p
	_, err := resolveWith(cfg, ResolveInput{Provider: "openai"})
	var cerr *clierr.CLIError
	if !errors.As(err, &cerr) || cerr.Code != "config.invalid" || cerr.Exit != 2 {
		t.Fatalf("expected config.invalid exit 2, got %v", err)
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

// Gateway key-leak guard: a custom kind=openai profile WITH a key but NO base_url
// must fail typed (config.invalid, exit 2) BEFORE the key is shipped to
// api.openai.com. The secret must never appear in the message or hint.
func TestResolveOpenAIGatewayKeyRequiresBaseURL(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("GATEWAY_TOKEN", "leak-me")
	cfg := config.Merge(config.Defaults(), config.Config{
		Providers: map[string]config.Provider{
			"badgw": {Kind: config.KindOpenAI, Model: "some-model", AuthEnv: "GATEWAY_TOKEN"},
		},
	})
	_, err := resolveWith(cfg, ResolveInput{Provider: "badgw"})
	var cerr *clierr.CLIError
	if !errors.As(err, &cerr) || cerr.Code != "config.invalid" {
		t.Fatalf("expected config.invalid, got %v", err)
	}
	if cerr.Exit != 2 {
		t.Fatalf("config.invalid must be exit 2, got %d", cerr.Exit)
	}
	if cerr.Hint == "" {
		t.Fatal("gateway guard must carry an actionable hint")
	}
	for _, s := range []string{cerr.Message, cerr.Hint} {
		if strings.Contains(s, "leak-me") {
			t.Fatalf("secret leaked into error: %q", s)
		}
	}
}

// The same custom profile PASSES once base_url is set, the guard only blocks the
// no-base_url case, never a legit gateway.
func TestResolveOpenAIGatewayWithBaseURLPasses(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("GATEWAY_TOKEN", "gw-key")
	cfg := config.Merge(config.Defaults(), config.Config{
		Providers: map[string]config.Provider{
			"gw": {Kind: config.KindOpenAI, BaseURL: "https://gw.example/v1", Model: "m", AuthEnv: "GATEWAY_TOKEN"},
		},
	})
	creds, err := resolveWith(cfg, ResolveInput{Provider: "gw"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.BaseURL != "https://gw.example/v1" || creds.APIKey != "gw-key" {
		t.Fatalf("legit gateway must resolve: %+v", creds)
	}
}

// The built-in openai profile sets prof.BaseURL=DefaultOpenAIBaseURL, so an
// OPENAI_API_KEY with no explicit base_url still PASSES the gateway guard.
func TestResolveOpenAIBuiltinKeyPassesGuard(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "oai-key")
	creds, err := resolveWith(config.Defaults(), ResolveInput{Provider: "openai"})
	if err != nil {
		t.Fatalf("built-in openai+key must pass the guard: %v", err)
	}
	if creds.BaseURL != config.DefaultOpenAIBaseURL || creds.APIKey != "oai-key" {
		t.Fatalf("built-in openai resolve wrong: %+v", creds)
	}
}

// An unknown provider `auth` value is config.invalid at exit 2 (was 1).
func TestResolveOpenAIUnknownAuthExit2(t *testing.T) {
	clearProviderEnv(t)
	cfg := config.Defaults()
	op := cfg.Providers["openai"]
	op.Auth = "bogus"
	cfg.Providers["openai"] = op
	_, err := resolveWith(cfg, ResolveInput{Provider: "openai"})
	var cerr *clierr.CLIError
	if !errors.As(err, &cerr) || cerr.Code != "config.invalid" {
		t.Fatalf("expected config.invalid, got %v", err)
	}
	if cerr.Exit != 2 {
		t.Fatalf("unknown-auth config.invalid must be exit 2, got %d", cerr.Exit)
	}
}

// codexOAuthCfg builds an openai profile pinned to OAuth (so resolution takes the
// codex backend path) with the given config model.
func codexOAuthCfg(model string) config.Config {
	cfg := config.Defaults()
	op := cfg.Providers["openai"]
	op.Auth = "oauth"
	op.Model = model
	cfg.Providers["openai"] = op
	return cfg
}

func fakeOAuthResolver(context.Context) (OAuthCredential, bool, error) {
	return OAuthCredential{AccessToken: "tok", AccountID: "acct", BackendBaseURL: "https://backend.example/codex"}, true, nil
}

// codex path: an EXPLICIT non-gpt-4o config model is honored over DefaultCodexModel.
func TestResolveCodexHonorsExplicitConfigModel(t *testing.T) {
	clearProviderEnv(t)
	creds, err := resolveWith(codexOAuthCfg("gpt-5-codex"), ResolveInput{Provider: "openai", OAuthResolver: fakeOAuthResolver})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.Backend != "codex" {
		t.Fatalf("want codex backend, got %q", creds.Backend)
	}
	if creds.Model != "gpt-5-codex" {
		t.Fatalf("explicit config model must win on codex path, got %q", creds.Model)
	}
}

// codex path: the merged gpt-4o default must NEVER leak to the codex backend -
// it is filtered and falls through to DefaultCodexModel (the gpt-4o-rejection bug).
func TestResolveCodexFiltersGPT4oDefault(t *testing.T) {
	clearProviderEnv(t)
	creds, err := resolveWith(codexOAuthCfg(config.DefaultOpenAIModel), ResolveInput{Provider: "openai", OAuthResolver: fakeOAuthResolver})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.Model == config.DefaultOpenAIModel {
		t.Fatalf("gpt-4o must never reach the codex backend, got %q", creds.Model)
	}
	if creds.Model != config.DefaultCodexModel {
		t.Fatalf("codex path must default to %q, got %q", config.DefaultCodexModel, creds.Model)
	}
}

// codex path: --model and MIUCR_CODEX_MODEL still win over a config model.
func TestResolveCodexFlagAndEnvWin(t *testing.T) {
	clearProviderEnv(t)
	creds, err := resolveWith(codexOAuthCfg("gpt-5-codex"), ResolveInput{Provider: "openai", Model: "gpt-flag", OAuthResolver: fakeOAuthResolver})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.Model != "gpt-flag" {
		t.Fatalf("--model must win on codex path, got %q", creds.Model)
	}

	t.Setenv("MIUCR_CODEX_MODEL", "gpt-env")
	creds, err = resolveWith(codexOAuthCfg("gpt-5-codex"), ResolveInput{Provider: "openai", OAuthResolver: fakeOAuthResolver})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.Model != "gpt-env" {
		t.Fatalf("MIUCR_CODEX_MODEL must win over config model, got %q", creds.Model)
	}
}

// The API-KEY openai path is unchanged: it keeps prof.Model + the gpt-4o default
// (codexConfigModel only narrows the codex backend, not the platform-key path).
func TestResolveOpenAIAPIKeyModelUnchanged(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "oai-key")
	creds, err := resolveWith(config.Defaults(), ResolveInput{Provider: "openai"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.Backend == "codex" {
		t.Fatalf("API-key path must not use the codex backend: %+v", creds)
	}
	if creds.Model != config.DefaultOpenAIModel {
		t.Fatalf("API-key openai path must keep the gpt-4o default, got %q", creds.Model)
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
