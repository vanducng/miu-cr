package agent

import (
	stdctx "context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	"github.com/vanducng/miu-cr/internal/config"
)

// Credentials is the resolved, in-memory-only auth for one LLM call. Tokens are
// NEVER persisted to disk or the store.
type Credentials struct {
	Kind   config.Kind
	APIKey string
	Model  string
	// Temperature is the LLM sampling temperature for the review pass (from
	// [review].temperature; 0 by default for deterministic, stable findings).
	// Applied only when thinking is OFF for this model — thinking forces temp 1.
	Temperature float64
	// Thinking is the [review].thinking setting (auto|off|low|medium|high; ""→auto).
	// Each backend decides if its model SUPPORTS thinking; when on it sends the
	// provider's extended-thinking/reasoning params and omits temperature.
	Thinking string
	// BaseURL overrides the provider endpoint. For Anthropic this routes the
	// official SDK at an Anthropic-compatible gateway. Empty means the
	// SDK/provider default.
	BaseURL string
	// AuthToken, when set on the Anthropic path, is sent as a Bearer token
	// (Authorization header) instead of x-api-key. Used by Anthropic-compatible
	// gateways.
	AuthToken string

	AuthSource     string
	AuthSourceName string

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
		return Credentials{}, err // config.Load already returns a typed config.invalid CLIError
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
	var (
		creds Credentials
		err   error
	)
	switch prof.Kind {
	case config.KindOpenAI:
		creds, err = resolveOpenAI(in, prof)
	case config.KindAnthropic:
		creds, err = resolveAnthropic(in, prof)
	default:
		return Credentials{}, &clierr.CLIError{
			Code:    "agent.unknown_kind",
			Message: fmt.Sprintf("provider %q has unknown kind %q", name, prof.Kind),
			Hint:    "kind must be anthropic or openai",
			Exit:    1,
		}
	}
	if err != nil {
		return Credentials{}, err
	}
	// [review].temperature (nil → 0, the deterministic default) applies when
	// thinking is off; [review].thinking ("" → auto) drives extended thinking.
	if cfg.Review.Temperature != nil {
		creds.Temperature = *cfg.Review.Temperature
	}
	creds.Thinking = cfg.Review.Thinking
	return creds, nil
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
	authMode := normalizeAuthMode(prof.Auth)
	switch authMode {
	case "", "api_key", "bearer":
	case "oauth":
		return Credentials{}, invalidAuthMode(authMode, "oauth is only valid for kind = \"openai\"")
	default:
		return Credentials{}, invalidAuthMode(authMode, "use \"api_key\", \"bearer\", or omit for legacy auto")
	}

	authToken := firstCredential(
		credential{Value: in.AuthToken, Source: "flag", Name: "--auth-token"},
		envCredential("ANTHROPIC_AUTH_TOKEN"),
	)
	apiKey := firstCredential(
		credential{Value: in.APIKey, Source: "flag", Name: "--api-key"},
		envCredential("ANTHROPIC_API_KEY"),
	)
	var profile credential
	if apiKey.Value == "" && authToken.Value == "" {
		var err error
		profile, err = profileCredential(resolveContext(in), prof)
		if err != nil {
			return Credentials{}, err
		}
		if profile.Value != "" {
			if authMode == "api_key" {
				apiKey = profile
			} else {
				authToken = profile
			}
		}
	}
	baseURL := firstNonEmpty(in.BaseURL, os.Getenv("ANTHROPIC_BASE_URL"), prof.BaseURL)

	if apiKey.Value == "" && authToken.Value == "" {
		return Credentials{}, &clierr.CLIError{
			Code:    "agent.no_credentials",
			Message: "no Anthropic credentials: set ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN, configure a provider in " + config.FilePathOrEmpty() + ", or pass --api-key / --auth-token",
			Hint:    "export ANTHROPIC_API_KEY=... or run with --api-key; see config.example.toml for provider profiles (auth_env/auth_command)",
			Details: authDetails("anthropic", config.KindAnthropic, profile),
			Exit:    1,
		}
	}

	// A Bearer auth_token only makes sense for an Anthropic-compatible gateway,
	// which requires a base_url. Without one it would be sent to api.anthropic.com
	// (which uses x-api-key, not Bearer), leaking the token and failing the call.
	if authToken.Value != "" && baseURL == "" {
		return Credentials{}, &clierr.CLIError{
			Code:    "agent.auth_token_requires_base_url",
			Message: "profile Bearer credential is for an Anthropic-compatible gateway, but no base_url is configured",
			Hint:    "set base_url on the provider profile (or ANTHROPIC_BASE_URL), or use an API key (ANTHROPIC_API_KEY / --api-key)",
			Details: authDetails("anthropic", config.KindAnthropic, authToken),
			Exit:    1,
		}
	}

	model := firstNonEmpty(in.Model, os.Getenv("ANTHROPIC_MODEL"), prof.Model, config.DefaultAnthropicModel)
	source := apiKey
	if authToken.Value != "" {
		source = authToken
	}
	return Credentials{
		Kind:           config.KindAnthropic,
		APIKey:         apiKey.Value,
		AuthToken:      authToken.Value,
		BaseURL:        baseURL,
		Model:          model,
		AuthSource:     source.Source,
		AuthSourceName: source.Name,
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
	// preDefaultBase is the explicitly-configured endpoint, BEFORE the
	// DefaultOpenAIBaseURL fallback. An api-key (non-OAuth) profile with NONE set
	// would ship the key to api.openai.com: a key-leak for a custom keyed
	// kind=openai gateway profile that forgot base_url. The built-in openai profile
	// sets prof.BaseURL=DefaultOpenAIBaseURL (provider.go), so it passes. Mirrors
	// the symmetric Anthropic auth_token guard above.
	preDefaultBase := firstNonEmpty(in.BaseURL, os.Getenv("OPENAI_BASE_URL"), prof.BaseURL)
	baseURL := firstNonEmpty(preDefaultBase, config.DefaultOpenAIBaseURL)
	model := firstNonEmpty(in.Model, os.Getenv("OPENAI_MODEL"), prof.Model, config.DefaultOpenAIModel)
	gatewayBaseRequired := func() (Credentials, error) {
		return Credentials{}, &clierr.CLIError{
			Code:    "config.invalid",
			Message: "an openai-kind gateway profile with an api key must set base_url; without one the key would be sent to api.openai.com",
			Hint:    "set base_url for an openai-kind gateway profile (or OPENAI_BASE_URL / --base-url)",
			Exit:    2,
		}
	}
	apiKeyCreds := func(k credential) (Credentials, error) {
		if preDefaultBase == "" {
			creds, err := gatewayBaseRequired()
			if ce, ok := err.(*clierr.CLIError); ok {
				ce.Details = authDetails("openai", config.KindOpenAI, k)
			}
			return creds, err
		}
		return Credentials{Kind: config.KindOpenAI, APIKey: k.Value, BaseURL: baseURL, Model: model, AuthSource: k.Source, AuthSourceName: k.Name}, nil
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

	if k := strings.TrimSpace(in.APIKey); k != "" {
		return apiKeyCreds(credential{Value: k, Source: "flag", Name: "--api-key"})
	}

	switch authMode := normalizeAuthMode(prof.Auth); authMode {
	case "oauth":
		if hasProfileCredentialSource(prof) {
			return Credentials{}, invalidAuthMode(authMode, "remove auth_env/auth_command/auth_token when auth = \"oauth\"")
		}
		creds, ok, err := tryOAuth()
		if err != nil {
			return Credentials{}, err
		}
		if ok {
			return creds, nil
		}
		return noCred("provider auth = \"oauth\" but no `miucr login` session — run `miucr login --provider openai`")
	case "api_key", "apikey", "key":
		profile, err := profileCredential(resolveContext(in), prof)
		if err != nil {
			return Credentials{}, err
		}
		if k := firstCredential(profile, envCredential("OPENAI_API_KEY")); k.Value != "" {
			return apiKeyCreds(k)
		}
		return noCred("provider auth = \"api_key\" but no key — set OPENAI_API_KEY, a profile auth_env, or pass --api-key")
	case "bearer":
		return Credentials{}, invalidAuthMode(authMode, "bearer is only valid for kind = \"anthropic\"")
	case "":
	default:
		return Credentials{}, invalidAuthMode(authMode, "use \"oauth\", \"api_key\", or omit for auto")
	}

	// Unset auth tries profile, OAuth, then OPENAI_API_KEY.
	profile, err := profileCredential(resolveContext(in), prof)
	if err != nil {
		return Credentials{}, err
	}
	if k := profile; k.Value != "" {
		return apiKeyCreds(k)
	}
	creds, ok, oauthErr := tryOAuth()
	if ok {
		return creds, nil
	}
	if k := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); k != "" {
		creds, kerr := apiKeyCreds(credential{Value: k, Source: "env", Name: "OPENAI_API_KEY"})
		if kerr == nil {
			return creds, nil
		}
		if oauthErr != nil {
			if ce, ok := kerr.(*clierr.CLIError); ok && ce.Cause == nil {
				ce.Cause = oauthErr
			}
		}
		return Credentials{}, kerr
	}
	if oauthErr != nil {
		return Credentials{}, oauthErr
	}
	return noCred("no OpenAI credential: run `miucr login` to use your ChatGPT plan, set OPENAI_API_KEY, configure a provider in " + config.FilePathOrEmpty() + ", or pass --api-key")
}

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
	// Keep api.openai.com defaults out of the codex backend.
	model := firstNonEmpty(in.Model, os.Getenv("MIUCR_CODEX_MODEL"), codexConfigModel(prof.Model), config.DefaultCodexModel)
	return Credentials{
		Kind:           config.KindOpenAI,
		Backend:        "codex",
		OAuthToken:     cred.AccessToken,
		OAuthAccountID: cred.AccountID,
		OAuthRefresh:   cred.Refresh,
		BaseURL:        cred.BackendBaseURL,
		Model:          model,
		AuthSource:     "oauth",
		AuthSourceName: "miucr login",
	}, true, nil
}

var plaintextAuthTokenWarn sync.Once

const (
	authCommandTimeout     = 10 * time.Second
	authCommandOutputLimit = 16 * 1024
)

func resolveContext(in ResolveInput) stdctx.Context {
	if in.Ctx != nil {
		return in.Ctx
	}
	return stdctx.Background()
}

type credential struct {
	Value  string
	Source string
	Name   string
}

func profileCredential(ctx stdctx.Context, prof config.Provider) (credential, error) {
	if s := strings.TrimSpace(prof.AuthToken); s != "" {
		plaintextAuthTokenWarn.Do(func() {
			fmt.Fprintln(os.Stderr, "miu-cr: warning: provider auth_token is stored in plaintext on disk; prefer auth_env or auth_command")
		})
		return credential{Value: s, Source: "auth_token", Name: "auth_token"}, nil
	}
	if prof.AuthEnv != "" {
		if s := strings.TrimSpace(os.Getenv(prof.AuthEnv)); s != "" {
			return credential{Value: s, Source: "auth_env", Name: prof.AuthEnv}, nil
		}
	}
	if len(prof.AuthCommand) != 0 {
		secret, err := runAuthCommand(ctx, prof.AuthCommand)
		if err != nil {
			return credential{}, err
		}
		return credential{Value: secret, Source: "auth_command", Name: strings.TrimSpace(prof.AuthCommand[0])}, nil
	}
	if prof.AuthEnv != "" {
		return credential{Source: "auth_env", Name: prof.AuthEnv}, nil
	}
	return credential{}, nil
}

func runAuthCommand(ctx stdctx.Context, argv []string) (string, error) {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return "", &clierr.CLIError{Code: "config.invalid", Message: "auth_command must be a non-empty argv array", Hint: "set auth_command = [\"gopass\", \"show\", \"-o\", \"path/to/secret\"]", Exit: 2, Details: map[string]any{"auth_source": "auth_command"}}
	}
	args := append([]string(nil), argv...)
	args[0] = strings.TrimSpace(args[0])
	ctx, cancel := stdctx.WithTimeout(ctx, authCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	var stdout cappedBuffer
	stdout.limit = authCommandOutputLimit
	cmd.Stdout = &stdout
	err := cmd.Run()
	if err != nil {
		switch ctx.Err() {
		case stdctx.DeadlineExceeded:
			return "", &clierr.CLIError{Code: "agent.auth_command_failed", Message: "auth_command timed out", Hint: "make the credential command non-interactive or increase shell/keychain availability", Exit: 1, Cause: err, Details: map[string]any{"auth_source": "auth_command", "auth_command": args[0]}}
		case stdctx.Canceled:
			return "", &clierr.CLIError{Code: "agent.auth_command_cancelled", Message: "auth_command cancelled", Hint: "the parent context was cancelled", Exit: 1, Cause: err, Details: map[string]any{"auth_source": "auth_command", "auth_command": args[0]}}
		}
		return "", &clierr.CLIError{Code: "agent.auth_command_failed", Message: "auth_command failed", Hint: "run the configured auth_command directly; miu-cr omits stderr because it may contain secrets", Exit: 1, Cause: err, Details: map[string]any{"auth_source": "auth_command", "auth_command": args[0]}}
	}
	secret := strings.TrimSpace(stdout.String())
	if secret == "" {
		return "", &clierr.CLIError{Code: "agent.auth_command_failed", Message: "auth_command printed no credential", Hint: "ensure the command prints the token to stdout", Exit: 1, Details: map[string]any{"auth_source": "auth_command", "auth_command": args[0]}}
	}
	if strings.ContainsAny(secret, "\r\n") {
		return "", &clierr.CLIError{Code: "agent.auth_command_failed", Message: "auth_command printed multiple lines", Hint: "ensure the command prints exactly one credential line", Exit: 1, Details: map[string]any{"auth_source": "auth_command", "auth_command": args[0]}}
	}
	return secret, nil
}

type cappedBuffer struct {
	buf   strings.Builder
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.limit <= 0 || b.buf.Len() >= b.limit {
		return n, nil
	}
	if remaining := b.limit - b.buf.Len(); len(p) > remaining {
		p = p[:remaining]
	}
	_, _ = b.buf.Write(p)
	return n, nil
}

func (b *cappedBuffer) String() string {
	return b.buf.String()
}

func normalizeAuthMode(auth string) string {
	return strings.ToLower(strings.TrimSpace(auth))
}

func hasProfileCredentialSource(prof config.Provider) bool {
	return strings.TrimSpace(prof.AuthToken) != "" || strings.TrimSpace(prof.AuthEnv) != "" || len(prof.AuthCommand) > 0
}

func invalidAuthMode(authMode, hint string) error {
	return &clierr.CLIError{
		Code:    "config.invalid",
		Message: "invalid provider auth " + strconv.Quote(authMode),
		Hint:    hint + "; configure auth in " + config.FilePathOrEmpty(),
		Exit:    2,
	}
}

func envCredential(name string) credential {
	if s := strings.TrimSpace(os.Getenv(name)); s != "" {
		return credential{Value: s, Source: "env", Name: name}
	}
	return credential{}
}

func firstCredential(vals ...credential) credential {
	for _, v := range vals {
		if strings.TrimSpace(v.Value) != "" {
			return v
		}
	}
	return credential{}
}

func authDetails(provider string, kind config.Kind, c credential) map[string]any {
	out := map[string]any{"provider": provider, "kind": string(kind)}
	if c.Source != "" {
		out["auth_source"] = c.Source
	}
	if c.Name != "" {
		out["auth_source_name"] = c.Name
	}
	return out
}

// codexConfigModel returns an explicitly-configured codex model, dropping the
// merged gpt-4o default (== DefaultOpenAIModel) which the codex backend rejects.
func codexConfigModel(m string) string {
	if m != "" && m != config.DefaultOpenAIModel {
		return m
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
