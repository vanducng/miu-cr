package agent

import (
	"fmt"
	"os"
	"strings"

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
}

// ResolveInput carries the CLI flag values (all optional) into resolution.
type ResolveInput struct {
	Provider  string // profile name: "anthropic" | "openai" | <configured> | "auto" | ""
	APIKey    string // --api-key
	BaseURL   string // --base-url
	AuthToken string // --auth-token
	Model     string // --model
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
			Hint:    "export ANTHROPIC_API_KEY=... or run with --api-key",
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
	apiKey := firstNonEmpty(in.APIKey, os.Getenv("OPENAI_API_KEY"), profileSecret(prof))
	if apiKey == "" {
		return Credentials{}, &clierr.CLIError{
			Code:    "agent.no_credentials",
			Message: "no OpenAI API key: set OPENAI_API_KEY, configure a provider in " + config.FilePathOrEmpty() + ", or pass --api-key",
			Hint:    "export OPENAI_API_KEY=... or run with --api-key",
			Exit:    1,
		}
	}
	baseURL := firstNonEmpty(in.BaseURL, os.Getenv("OPENAI_BASE_URL"), prof.BaseURL, config.DefaultOpenAIBaseURL)
	model := firstNonEmpty(in.Model, os.Getenv("OPENAI_MODEL"), prof.Model, config.DefaultOpenAIModel)
	return Credentials{
		Kind:    config.KindOpenAI,
		APIKey:  apiKey,
		BaseURL: baseURL,
		Model:   model,
	}, nil
}

// profileSecret returns a profile's literal AuthToken, else the value of the
// env var it names via AuthEnv.
func profileSecret(prof config.Provider) string {
	if s := strings.TrimSpace(prof.AuthToken); s != "" {
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
