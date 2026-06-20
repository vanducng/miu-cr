package agent

import (
	"os"
	"strings"

	"github.com/vanducng/miu-cr/internal/cli"
)

// Provider selects which LLM backend the review pass uses.
type Provider string

const (
	ProviderAnthropic Provider = "anthropic"
	ProviderOpenAI    Provider = "openai"
	ProviderAuto      Provider = "auto"
)

// Pinned default models per provider. Override via env.
const (
	defaultModel       = "claude-sonnet-4-5-20250929" // Anthropic; override ANTHROPIC_MODEL
	defaultOpenAIModel = "gpt-4o"                     // OpenAI; override OPENAI_MODEL
	openAIBaseURL      = "https://api.openai.com/v1"
)

// Credentials is the resolved, in-memory-only auth for one LLM call. Tokens are
// NEVER persisted to disk or the store.
type Credentials struct {
	Provider Provider
	APIKey   string
	Model    string
	// BaseURL overrides the provider endpoint. For Anthropic this routes the
	// official SDK at an Anthropic-compatible gateway (e.g. z.ai/glm). Empty
	// means the SDK/provider default.
	BaseURL string
	// AuthToken, when set on the Anthropic path, is sent as a Bearer token
	// (Authorization header) instead of x-api-key. Used by gateways like z.ai.
	AuthToken string
}

// ResolveInput carries the CLI flag values (all optional) into resolution.
type ResolveInput struct {
	Provider  string // "anthropic" | "openai" | "auto" | "" (== auto)
	APIKey    string // --api-key
	BaseURL   string // --base-url
	AuthToken string // --auth-token
	Model     string // --model
}

// Resolve picks the provider (flag → auto-detect from env) and the matching
// credentials. Flags win over env. Missing credentials return a typed
// *cli.CLIError. Nothing here is persisted.
func Resolve(in ResolveInput) (Credentials, error) {
	p := normalizeProvider(in.Provider)
	if p == ProviderAuto {
		p = detectProvider(in)
	}
	switch p {
	case ProviderOpenAI:
		return resolveOpenAI(in)
	default:
		return resolveAnthropic(in)
	}
}

func normalizeProvider(s string) Provider {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "openai":
		return ProviderOpenAI
	case "anthropic":
		return ProviderAnthropic
	default:
		return ProviderAuto
	}
}

// detectProvider chooses a provider when none is given: an explicit OpenAI flag
// or OPENAI_API_KEY (with no Anthropic key present) selects OpenAI; otherwise
// Anthropic. Anthropic is the sensible default since it backs both the native
// API and Anthropic-compatible gateways (z.ai/glm).
func detectProvider(in ResolveInput) Provider {
	anthropicKey := strings.TrimSpace(in.APIKey) != "" ||
		strings.TrimSpace(in.AuthToken) != "" ||
		envSet("ANTHROPIC_API_KEY") || envSet("ANTHROPIC_AUTH_TOKEN") || envSet("ZAI_API_KEY")
	openaiKey := envSet("OPENAI_API_KEY")
	if openaiKey && !anthropicKey {
		return ProviderOpenAI
	}
	return ProviderAnthropic
}

func resolveAnthropic(in ResolveInput) (Credentials, error) {
	authToken := firstNonEmpty(in.AuthToken, os.Getenv("ANTHROPIC_AUTH_TOKEN"))
	apiKey := firstNonEmpty(in.APIKey, os.Getenv("ANTHROPIC_API_KEY"))
	baseURL := firstNonEmpty(in.BaseURL, os.Getenv("ANTHROPIC_BASE_URL"))

	// z.ai exposes an Anthropic-compatible gateway: ZAI_API_KEY + the gateway
	// base URL, sent as a Bearer auth token. Only used when no explicit
	// Anthropic credential/base URL was provided.
	if apiKey == "" && authToken == "" {
		if zai := strings.TrimSpace(os.Getenv("ZAI_API_KEY")); zai != "" {
			authToken = zai
			if baseURL == "" {
				baseURL = "https://api.z.ai/api/anthropic"
			}
		}
	}

	if apiKey == "" && authToken == "" {
		return Credentials{}, &cli.CLIError{
			Code:    "agent.no_credentials",
			Message: "no Anthropic credentials: set ANTHROPIC_API_KEY (or ANTHROPIC_AUTH_TOKEN / ZAI_API_KEY) or pass --api-key / --auth-token",
			Hint:    "export ANTHROPIC_API_KEY=... or run with --api-key",
			Exit:    1,
		}
	}

	model := firstNonEmpty(in.Model, os.Getenv("ANTHROPIC_MODEL"), defaultModel)
	return Credentials{
		Provider:  ProviderAnthropic,
		APIKey:    apiKey,
		AuthToken: authToken,
		BaseURL:   baseURL,
		Model:     model,
	}, nil
}

func resolveOpenAI(in ResolveInput) (Credentials, error) {
	apiKey := firstNonEmpty(in.APIKey, os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		return Credentials{}, &cli.CLIError{
			Code:    "agent.no_credentials",
			Message: "no OpenAI API key: set OPENAI_API_KEY or pass --api-key",
			Hint:    "export OPENAI_API_KEY=... or run with --api-key",
			Exit:    1,
		}
	}
	baseURL := firstNonEmpty(in.BaseURL, os.Getenv("OPENAI_BASE_URL"), openAIBaseURL)
	model := firstNonEmpty(in.Model, os.Getenv("OPENAI_MODEL"), defaultOpenAIModel)
	return Credentials{
		Provider: ProviderOpenAI,
		APIKey:   apiKey,
		BaseURL:  baseURL,
		Model:    model,
	}, nil
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
