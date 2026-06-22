package agent

import (
	stdctx "context"
	"errors"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	"github.com/vanducng/miu-cr/internal/config"
)

func codexResolver() func(stdctx.Context) (OAuthCredential, bool, error) {
	return func(stdctx.Context) (OAuthCredential, bool, error) {
		return OAuthCredential{
			AccessToken:    "oauth-tok",
			AccountID:      "acct-1",
			BackendBaseURL: "https://chatgpt.com/backend-api/codex",
			Refresh:        func(stdctx.Context) (string, error) { return "new", nil },
		}, true, nil
	}
}

func TestResolveOAuthRoutesToCodexBackend(t *testing.T) {
	clearProviderEnv(t)
	creds, err := resolveWith(config.Defaults(), ResolveInput{
		Provider:      "openai",
		OAuthResolver: codexResolver(),
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.Backend != "codex" {
		t.Fatalf("Backend = %q, want codex", creds.Backend)
	}
	if creds.OAuthToken != "oauth-tok" || creds.OAuthAccountID != "acct-1" {
		t.Errorf("oauth fields = %+v", creds)
	}
	if creds.BaseURL != "https://chatgpt.com/backend-api/codex" {
		t.Errorf("BaseURL = %q", creds.BaseURL)
	}
}

func TestResolveExplicitKeyBeatsOAuth(t *testing.T) {
	clearProviderEnv(t)
	called := false
	resolver := func(stdctx.Context) (OAuthCredential, bool, error) {
		called = true
		return OAuthCredential{AccessToken: "oauth-tok"}, true, nil
	}
	creds, err := resolveWith(config.Defaults(), ResolveInput{
		Provider:      "openai",
		APIKey:        "sk-explicit",
		OAuthResolver: resolver,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.Backend == "codex" {
		t.Fatal("explicit --api-key must not route to the codex backend")
	}
	if creds.APIKey != "sk-explicit" {
		t.Errorf("APIKey = %q", creds.APIKey)
	}
	if called {
		t.Error("OAuthResolver must not be consulted when an explicit key is present")
	}
}

func TestResolveOAuthBeatsAmbientEnvKey(t *testing.T) {
	// A deliberate `miucr login` (OAuth) must win over an ambient OPENAI_API_KEY
	// (commonly set for other tools), so a stray env var can't silently route a
	// review to the billed API instead of the user's ChatGPT plan.
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	creds, err := resolveWith(config.Defaults(), ResolveInput{
		Provider:      "openai",
		OAuthResolver: codexResolver(),
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.Backend != "codex" {
		t.Errorf("OAuth login must beat an ambient OPENAI_API_KEY: %+v", creds)
	}
}

func TestResolveAmbientEnvKeyUsedWithoutOAuth(t *testing.T) {
	// With no login, the ambient OPENAI_API_KEY is still the zero-config fallback.
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	noCred := func(stdctx.Context) (OAuthCredential, bool, error) { return OAuthCredential{}, false, nil }
	creds, err := resolveWith(config.Defaults(), ResolveInput{Provider: "openai", OAuthResolver: noCred})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.Backend == "codex" || creds.APIKey != "sk-env" {
		t.Errorf("ambient OPENAI_API_KEY should be the fallback when not logged in: %+v", creds)
	}
}

func TestResolveNoOAuthNoKeyTypedError(t *testing.T) {
	clearProviderEnv(t)
	noCred := func(stdctx.Context) (OAuthCredential, bool, error) {
		return OAuthCredential{}, false, nil
	}
	_, err := resolveWith(config.Defaults(), ResolveInput{Provider: "openai", OAuthResolver: noCred})
	if err == nil {
		t.Fatal("expected no_credentials error")
	}
}

func TestResolveOAuthResolverErrorSurfaces(t *testing.T) {
	clearProviderEnv(t)
	const leaked = "sk-proj-ABCDEFGH1234567890leaked"
	boom := func(stdctx.Context) (OAuthCredential, bool, error) {
		return OAuthCredential{}, false, errors.New("disk gone: token " + leaked)
	}
	_, err := resolveWith(config.Defaults(), ResolveInput{Provider: "openai", OAuthResolver: boom})
	if err == nil {
		t.Fatal("expected error from resolver")
	}
	// The rendered envelope message stays RedactString-scrubbed.
	if strings.Contains(err.Error(), leaked) {
		t.Errorf("resolver error leaked a token: %v", err)
	}
}

// TestResolveOAuthErrorWrapsCause verifies the CLIError preserves the cause chain
// (errors.Is/As) while exposing a typed code.
func TestResolveOAuthErrorWrapsCause(t *testing.T) {
	clearProviderEnv(t)
	sentinel := errors.New("token endpoint unreachable")
	boom := func(stdctx.Context) (OAuthCredential, bool, error) {
		return OAuthCredential{}, false, sentinel
	}
	_, err := resolveWith(config.Defaults(), ResolveInput{Provider: "openai", OAuthResolver: boom})
	if err == nil {
		t.Fatal("expected error from resolver")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("cause not wrapped: errors.Is(err, sentinel) = false; err=%v", err)
	}
	var cliErr *clierr.CLIError
	if !errors.As(err, &cliErr) || cliErr.Code != "agent.oauth_unavailable" {
		t.Errorf("err = %v, want code agent.oauth_unavailable", err)
	}
}

// TestResolveThreadsContextToResolver verifies the request context (not Background)
// reaches the OAuth resolver so refresh respects cancellation.
func TestResolveThreadsContextToResolver(t *testing.T) {
	clearProviderEnv(t)
	ctx, cancel := stdctx.WithCancel(stdctx.Background())
	cancel()
	var seen error
	resolver := func(c stdctx.Context) (OAuthCredential, bool, error) {
		seen = c.Err()
		return OAuthCredential{
			AccessToken:    "tok",
			BackendBaseURL: "https://backend.example/codex",
			Refresh:        func(stdctx.Context) (string, error) { return "", nil },
		}, true, nil
	}
	if _, err := resolveWith(config.Defaults(), ResolveInput{Ctx: ctx, Provider: "openai", OAuthResolver: resolver}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if seen == nil {
		t.Error("resolver did not receive the bounded context (got a fresh Background)")
	}
}

func openaiAuth(mode string) config.Config {
	cfg := config.Defaults()
	p := cfg.Providers["openai"]
	p.Auth = mode
	cfg.Providers["openai"] = p
	return cfg
}

func TestResolveAuthOAuthForcesOAuth(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	creds, err := resolveWith(openaiAuth("oauth"), ResolveInput{Provider: "openai", OAuthResolver: codexResolver()})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.Backend != "codex" {
		t.Errorf(`auth="oauth" must use OAuth even with an env key: %+v`, creds)
	}
}

func TestResolveAuthOAuthNoSessionErrors(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	noCred := func(stdctx.Context) (OAuthCredential, bool, error) { return OAuthCredential{}, false, nil }
	if _, err := resolveWith(openaiAuth("oauth"), ResolveInput{Provider: "openai", OAuthResolver: noCred}); err == nil {
		t.Fatal(`auth="oauth" with no login session must error, not use the env key`)
	}
}

func TestResolveAuthAPIKeyIgnoresOAuth(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	creds, err := resolveWith(openaiAuth("api_key"), ResolveInput{Provider: "openai", OAuthResolver: codexResolver()})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.Backend == "codex" || creds.APIKey != "sk-env" {
		t.Errorf(`auth="api_key" must use the key, not OAuth: %+v`, creds)
	}
}

func TestResolveOAuthErrorFallsBackToEnvKey(t *testing.T) {
	// A stale/expired OAuth session must NOT lock out a valid ambient OPENAI_API_KEY.
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	errResolver := func(stdctx.Context) (OAuthCredential, bool, error) {
		return OAuthCredential{}, false, errors.New("stale session")
	}
	creds, err := resolveWith(config.Defaults(), ResolveInput{Provider: "openai", OAuthResolver: errResolver})
	if err != nil {
		t.Fatalf("a stale OAuth session should fall back to the env key, got: %v", err)
	}
	if creds.Backend == "codex" || creds.APIKey != "sk-env" {
		t.Errorf("OAuth error should fall back to the ambient env key: %+v", creds)
	}
}
