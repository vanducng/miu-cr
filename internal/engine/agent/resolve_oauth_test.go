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

func TestResolveEnvKeyBeatsOAuth(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	creds, err := resolveWith(config.Defaults(), ResolveInput{
		Provider:      "openai",
		OAuthResolver: codexResolver(),
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if creds.Backend == "codex" || creds.APIKey != "sk-env" {
		t.Errorf("OPENAI_API_KEY must win: %+v", creds)
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
