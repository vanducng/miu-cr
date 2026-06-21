package config

// Kind is a first-class provider family with a registered Agent constructor.
// New vendors are added as named profiles of an existing kind (config only);
// a new kind needs a constructor registered in the agent package.
type Kind string

const (
	KindAnthropic Kind = "anthropic"
	KindOpenAI    Kind = "openai"
)

// Default endpoints and models per built-in kind. The Anthropic base URL is
// empty so the SDK uses its own default.
const (
	DefaultAnthropicModel = "claude-sonnet-4-5-20250929"
	DefaultOpenAIModel    = "gpt-4o"
	DefaultOpenAIBaseURL  = "https://api.openai.com/v1"
)

// Provider is one named provider profile. A vendor (z.ai/GLM, DeepSeek, a
// self-hosted gateway, …) is just a profile of kind "anthropic" or "openai"
// with its own base_url/model and a credential reference. The credential is
// either a literal AuthToken or AuthEnv, the NAME of an env var holding it;
// AuthToken/AuthEnv is sent as a Bearer token on the Anthropic path and as the
// API key on the OpenAI path. Standard env vars (ANTHROPIC_API_KEY, …) and CLI
// flags still override a profile credential.
//
// Precedence when both are set: AuthToken (the literal) wins over AuthEnv (see
// resolve's profileSecret). Prefer AuthEnv — it keeps the secret out of the
// plaintext config file.
type Provider struct {
	Kind      Kind   `toml:"kind"`
	BaseURL   string `toml:"base_url,omitempty"`
	Model     string `toml:"model,omitempty"`
	AuthToken string `toml:"auth_token,omitempty"` // literal credential; wins over AuthEnv when both set
	AuthEnv   string `toml:"auth_env,omitempty"`   // NAME of an env var holding the credential (preferred)
}

// Store selects the persistence backend. DSN is never persisted to disk by
// miucr itself and is always redacted in errors/logs; prefer the MIUCR_PG_DSN
// env var so the password need not sit in plaintext config.
type Store struct {
	Backend string `toml:"backend,omitempty"` // "sqlite" (default) | "postgres"; any other value is rejected (config.invalid)
	DSN     string `toml:"dsn,omitempty"`     // postgres DSN; env MIUCR_PG_DSN wins
}

// Config is the layered configuration: a set of named provider profiles plus
// the profile to use when none is selected on the command line.
type Config struct {
	DefaultProvider string              `toml:"default_provider"`
	Providers       map[string]Provider `toml:"providers"`
	Store           Store               `toml:"store"`
}

// Defaults returns the built-in configuration: the two first-class kinds as
// like-named profiles. Specific vendors (z.ai/GLM, etc.) are intentionally NOT
// baked in here — add them via the config file (see docs).
func Defaults() Config {
	return Config{
		DefaultProvider: string(KindAnthropic),
		Providers: map[string]Provider{
			string(KindAnthropic): {Kind: KindAnthropic, Model: DefaultAnthropicModel},
			string(KindOpenAI):    {Kind: KindOpenAI, BaseURL: DefaultOpenAIBaseURL, Model: DefaultOpenAIModel},
		},
		Store: Store{Backend: "sqlite"},
	}
}
