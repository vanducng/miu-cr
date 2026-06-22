package agent

import (
	stdctx "context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	"github.com/vanducng/miu-cr/internal/config"
)

// Credentials is the resolved, in-memory-only auth for one LLM call. Tokens are
// NEVER persisted to disk or the store.
type Credentials struct {
	Kind   config.Kind
	APIKey string
	Model  string
	// BaseURL overrides the provider endpoint. For Anthropic this routes the
	// official SDK at an Anthropic-compatible gateway. Empty means the
	// SDK/provider default.
	BaseURL string
	// AuthToken, when set on the Anthropic path, is sent as a Bearer token
	// (Authorization header) instead of x-api-key. Used by Anthropic-compatible
	// gateways.
	AuthToken string

	// Backend, when "codex", routes to the codex Responses backend (the OAuth /
	// ChatGPT-plan path) instead of the openai-go SDK. The fields below carry the
	// OAuth credential; they are set ONLY when no explicit key/env/profile key
	// won. Tokens here are in-memory only.
	Backend        string // "" (default) | "codex"
	OAuthToken     string
	OAuthAccountID string
	OAuthRefresh   func(ctx stdctx.Context) (string, error)
	HTTPClient     *http.Client // test seam for the codex backend
}

// ResolveInput carries the CLI flag values (all optional) into resolution.
type ResolveInput struct {
	// Ctx bounds the OAuth resolution (which may do a network token refresh) so it
	// respects cancellation/timeout. nil falls back to context.Background().
	Ctx stdctx.Context

	Provider  string // profile name: "anthropic" | "openai" | <configured> | "auto" | ""
	APIKey    string // --api-key
	BaseURL   string // --base-url
	AuthToken string // --auth-token
	Model     string // --model

	// OAuthResolver, when set, supplies the cached `miucr login` credential for
	// the OpenAI path. It is injected by the cli/config layer so this package
	// performs no filesystem access of its own. It is consulted ONLY when no
	// explicit --api-key / OPENAI_API_KEY / profile key is present, so an explicit
	// key always wins. ok=false means no usable cached credential.
	OAuthResolver func(ctx stdctx.Context) (OAuthCredential, bool, error)
}

// OAuthCredential is the resolved login credential the cli layer passes in,
// mirroring oauth.Resolved without coupling the resolver signature to that pkg.
type OAuthCredential struct {
	AccessToken    string
	AccountID      string
	BackendBaseURL string
	Refresh        func(ctx stdctx.Context) (string, error)
}

// Resolve loads the layered config and resolves credentials for the selected
// provider profile. Flags > env > config-file profile > built-in defaults.
// Missing credentials return a typed *clierr.CLIError. Nothing is persisted.
func Resolve(in ResolveInput) (Credentials, error) {
	cfg, err := config.Load()
	if err != nil {
		return Credentials{}, &clierr.CLIError{
			Code:    "config.invalid",
			Message: err.Error(),
			Hint:    "fix or remove " + config.FilePathOrEmpty(),
			Exit:    1,
		}
	}
	return resolveWith(cfg, in)
}

// resolveWith is Resolve with the config injected, so tests can exercise profile
// selection without touching the filesystem.
func resolveWith(cfg config.Config, in ResolveInput) (Credentials, error) {
	name := pickProviderName(cfg, in)
	prof, ok := cfg.Providers[name]
	if !ok {
		return Credentials{}, &clierr.CLIError{
			Code:    "agent.unknown_provider",
			Message: fmt.Sprintf("unknown provider %q", name),
			Hint:    "use a built-in (anthropic, openai) or configure it in " + config.FilePathOrEmpty(),
			Exit:    1,
		}
	}
	switch prof.Kind {
	case config.KindOpenAI:
		return resolveOpenAI(in, prof)
	case config.KindAnthropic:
		return resolveAnthropic(in, prof)
	default:
		return Credentials{}, &clierr.CLIError{
			Code:    "agent.unknown_kind",
			Message: fmt.Sprintf("provider %q has unknown kind %q", name, prof.Kind),
			Hint:    "kind must be anthropic or openai",
			Exit:    1,
		}
	}
}

// pickProviderName selects the profile: an explicit --provider name wins;
// otherwise env-based auto-detect, falling back to config.DefaultProvider.
func pickProviderName(cfg config.Config, in ResolveInput) string {
	if p := strings.ToLower(strings.TrimSpace(in.Provider)); p != "" && p != "auto" {
		return p
	}
	return autoDetectName(cfg, in)
}

// autoDetectName picks OpenAI only when an OpenAI key is present and no Anthropic
// credential is; otherwise it defers to config.DefaultProvider (Anthropic by
// default), the sensible base since it backs the native API and gateways alike.
//
// --api-key applies to the selected/default provider: with no --provider and no
// OpenAI-forcing env, that's Anthropic (or config default_provider). To send
// --api-key to OpenAI, pass --provider openai. We deliberately do NOT sniff the
// key's prefix to guess the vendor.
func autoDetectName(cfg config.Config, in ResolveInput) string {
	hasAnthropic := strings.TrimSpace(in.APIKey) != "" ||
		strings.TrimSpace(in.AuthToken) != "" ||
		envSet("ANTHROPIC_API_KEY") || envSet("ANTHROPIC_AUTH_TOKEN")
	if envSet("OPENAI_API_KEY") && !hasAnthropic {
		return string(config.KindOpenAI)
	}
	if d := strings.TrimSpace(cfg.DefaultProvider); d != "" && d != "auto" {
		return d
	}
	return string(config.KindAnthropic)
}

func resolveAnthropic(in ResolveInput, prof config.Provider) (Credentials, error) {
	// Profile credential (auth_token/auth_env) is always a Bearer auth token on
	// the Anthropic path; the x-api-key comes from --api-key / ANTHROPIC_API_KEY.
	authToken := firstNonEmpty(in.AuthToken, os.Getenv("ANTHROPIC_AUTH_TOKEN"), profileSecret(prof))
	apiKey := firstNonEmpty(in.APIKey, os.Getenv("ANTHROPIC_API_KEY"))
	baseURL := firstNonEmpty(in.BaseURL, os.Getenv("ANTHROPIC_BASE_URL"), prof.BaseURL)

	if apiKey == "" && authToken == "" {
		return Credentials{}, &clierr.CLIError{
			Code:    "agent.no_credentials",
			Message: "no Anthropic credentials: set ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN, configure a provider in " + config.FilePathOrEmpty() + ", or pass --api-key / --auth-token",
			Hint:    "export ANTHROPIC_API_KEY=... or run with --api-key; see config.example.toml for provider profiles (e.g. a gateway via auth_env)",
			Exit:    1,
		}
	}

	// A Bearer auth_token only makes sense for an Anthropic-compatible gateway,
	// which requires a base_url. Without one it would be sent to api.anthropic.com
	// (which uses x-api-key, not Bearer) — leaking the token and failing the call.
	if authToken != "" && baseURL == "" {
		return Credentials{}, &clierr.CLIError{
			Code:    "agent.auth_token_requires_base_url",
			Message: "auth_token/auth_env is a Bearer token for an Anthropic-compatible gateway, but no base_url is configured",
			Hint:    "set base_url on the provider profile (or ANTHROPIC_BASE_URL), or use an API key (ANTHROPIC_API_KEY / --api-key)",
			Exit:    1,
		}
	}

	model := firstNonEmpty(in.Model, os.Getenv("ANTHROPIC_MODEL"), prof.Model, config.DefaultAnthropicModel)
	return Credentials{
		Kind:      config.KindAnthropic,
		APIKey:    apiKey,
		AuthToken: authToken,
		BaseURL:   baseURL,
		Model:     model,
	}, nil
}

func resolveOpenAI(in ResolveInput, prof config.Provider) (Credentials, error) {
	// --auth-token is Anthropic-only (Bearer gateway auth). The OpenAI SDK has no
	// such notion, so reject an explicit one rather than silently ignoring it.
	if strings.TrimSpace(in.AuthToken) != "" {
		return Credentials{}, &clierr.CLIError{
			Code:    "agent.auth_token_unsupported",
			Message: "--auth-token is only valid for Anthropic providers; OpenAI uses --api-key / OPENAI_API_KEY",
			Hint:    "drop --auth-token, or select an Anthropic provider",
			Exit:    1,
		}
	}
	baseURL := firstNonEmpty(in.BaseURL, os.Getenv("OPENAI_BASE_URL"), prof.BaseURL, config.DefaultOpenAIBaseURL)
	model := firstNonEmpty(in.Model, os.Getenv("OPENAI_MODEL"), prof.Model, config.DefaultOpenAIModel)
	apiKeyCreds := func(k string) Credentials {
		return Credentials{Kind: config.KindOpenAI, APIKey: k, BaseURL: baseURL, Model: model}
	}

	noCred := func(msg string) (Credentials, error) {
		return Credentials{}, &clierr.CLIError{
			Code: "agent.no_credentials", Message: msg,
			Hint: "run `miucr login` to review on your ChatGPT plan; or export OPENAI_API_KEY=... / pass --api-key; see config.example.toml",
			Exit: 1,
		}
	}
	tryOAuth := func() (Credentials, bool, error) {
		if in.OAuthResolver == nil {
			return Credentials{}, false, nil
		}
		return resolveOAuthCodex(in, prof)
	}

	// --api-key is the most explicit (per-call) credential and always wins.
	if k := strings.TrimSpace(in.APIKey); k != "" {
		return apiKeyCreds(k), nil
	}

	// `auth` pins the method explicitly when set.
	switch authMode := strings.ToLower(strings.TrimSpace(prof.Auth)); authMode {
	case "oauth":
		creds, ok, err := tryOAuth()
		if err != nil {
			return Credentials{}, err
		}
		if ok {
			return creds, nil
		}
		return noCred("provider auth = \"oauth\" but no `miucr login` session — run `miucr login --provider openai`")
	case "api_key", "apikey", "key":
		if k := firstNonEmpty(profileSecret(prof), os.Getenv("OPENAI_API_KEY")); k != "" {
			return apiKeyCreds(k), nil
		}
		return noCred("provider auth = \"api_key\" but no key — set OPENAI_API_KEY, a profile auth_env, or pass --api-key")
	}

	// auth unset: intent-ordered so an ambient OPENAI_API_KEY (often set for other
	// tools) never overrides a deliberate choice:
	//   1. a profile-configured key (auth_env/auth_token)
	//   2. a cached `miucr login` (OAuth) -> the codex/ChatGPT-plan backend
	//   3. zero-config fallback: an ambient OPENAI_API_KEY env var
	if k := profileSecret(prof); k != "" {
		return apiKeyCreds(k), nil
	}
	creds, ok, oauthErr := tryOAuth()
	if ok {
		return creds, nil
	}
	if k := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); k != "" {
		return apiKeyCreds(k), nil // fall back to the env key even if a stale OAuth session errored
	}
	if oauthErr != nil {
		return Credentials{}, oauthErr
	}
	return noCred("no OpenAI credential: run `miucr login` to use your ChatGPT plan, set OPENAI_API_KEY, configure a provider in " + config.FilePathOrEmpty() + ", or pass --api-key")
}

// resolveOAuthCodex turns an injected login credential into codex-backend
// Credentials. The codex backend rejects api.openai.com models (e.g. gpt-4o),
// so it defaults to DefaultCodexModel; an explicit --model still wins.
func resolveOAuthCodex(in ResolveInput, prof config.Provider) (Credentials, bool, error) {
	ctx := in.Ctx
	if ctx == nil {
		ctx = stdctx.Background()
	}
	cred, ok, err := in.OAuthResolver(ctx)
	if err != nil {
		return Credentials{}, false, &clierr.CLIError{
			Code:    "agent.oauth_unavailable",
			Message: "cached login credential could not be resolved: " + config.RedactString(err.Error()),
			Hint:    "run `miucr login` again, or set OPENAI_API_KEY / --api-key",
			Exit:    1,
			Cause:   err,
		}
	}
	if !ok {
		return Credentials{}, false, nil
	}
	// The codex backend only accepts codex models — NOT prof.Model (the openai
	// profile defaults to gpt-4o, an api.openai.com model the backend rejects).
	// Override via --model or MIUCR_CODEX_MODEL; the allow-list varies by plan.
	model := firstNonEmpty(in.Model, os.Getenv("MIUCR_CODEX_MODEL"), config.DefaultCodexModel)
	return Credentials{
		Kind:           config.KindOpenAI,
		Backend:        "codex",
		OAuthToken:     cred.AccessToken,
		OAuthAccountID: cred.AccountID,
		OAuthRefresh:   cred.Refresh,
		BaseURL:        cred.BackendBaseURL,
		Model:          model,
	}, true, nil
}

var plaintextAuthTokenWarn sync.Once

// profileSecret returns a profile's literal AuthToken (which wins over AuthEnv),
// else the value of the env var named by AuthEnv. A literal auth_token lives in
// plaintext on disk, so its first use emits a one-time stderr warning.
func profileSecret(prof config.Provider) string {
	if s := strings.TrimSpace(prof.AuthToken); s != "" {
		plaintextAuthTokenWarn.Do(func() {
			fmt.Fprintln(os.Stderr, "miu-cr: warning: provider auth_token is stored in plaintext on disk; prefer auth_env (the NAME of an env var holding the token)")
		})
		return s
	}
	if prof.AuthEnv != "" {
		return strings.TrimSpace(os.Getenv(prof.AuthEnv))
	}
	return ""
}

func envSet(k string) bool { return strings.TrimSpace(os.Getenv(k)) != "" }

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
