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
	// DefaultCodexModel is used for the codex backend (ChatGPT-plan OAuth path);
	// the codex backend rejects api.openai.com models like gpt-4o and only allows
	// the codex model line the account is entitled to.
	DefaultCodexModel = "gpt-5.5"
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
	// Auth explicitly pins the method (OpenAI): "oauth" (use `miucr login`/the
	// ChatGPT plan, never an API key) | "api_key" (use a key, never OAuth) | ""
	// (intent-ordered auto: --api-key/profile key > OAuth login > OPENAI_API_KEY).
	Auth string `toml:"auth,omitempty"`
}

// Store selects the persistence backend. DSN is never persisted to disk by
// miucr itself and is always redacted in errors/logs; prefer the MIUCR_PG_DSN
// env var so the password need not sit in plaintext config.
type Store struct {
	Backend string `toml:"backend,omitempty"` // "sqlite" (default) | "postgres"; any other value is rejected (config.invalid)
	DSN     string `toml:"dsn,omitempty"`     // postgres DSN; env MIUCR_PG_DSN wins
}

// DefaultEmbeddingModel and DefaultEmbeddingDim are the built-in defaults for the
// opt-in semantic layer; text-embedding-3-small at 1536 dims matches OpenAI's
// default and the pgvector column dim templated in the store (M7/P2).
const (
	DefaultEmbeddingModel = "text-embedding-3-small"
	DefaultEmbeddingDim   = 1536
	// MaxEmbeddingDim is pgvector's hard ceiling for a vector(N) column; a dim
	// outside [1,MaxEmbeddingDim] is rejected before any DDL is rendered.
	MaxEmbeddingDim = 16000
)

// Embedding configures the opt-in semantic-recall layer (M7). It is OFF unless
// Enabled is explicitly true AND the store backend is postgres — never enabled
// by provider-presence, so copying an example config cannot silently start
// sending code-derived text off-box. The credential is resolved at runtime from
// the same env/flag chain as the LLM provider and is never persisted/redacted;
// BaseURL is a non-secret endpoint override for self-hosted/compatible APIs.
type Embedding struct {
	Enabled  bool   `toml:"enabled"`
	Provider string `toml:"provider,omitempty"` // profile kind hint: "openai" (default); reserved
	Model    string `toml:"model,omitempty"`
	BaseURL  string `toml:"base_url,omitempty"`
	Dim      int    `toml:"dim,omitempty"`
}

// Github configures GitHub authentication. Mode defaults to "pat" (the
// pre-M8 PAT/anonymous behavior). Mode "app" opts into GitHub App installation
// auth: AppID + InstallationID + PrivateKeyPath. PrivateKeyPath is a PATH to a
// PEM file (never inline PEM — RedactString cannot mask a multi-line key); the
// key is read at startup, parsed, and the raw bytes zeroed, and is never logged.
type Github struct {
	Mode           string `toml:"mode,omitempty"`             // "pat" (default) | "app"
	AppID          string `toml:"app_id,omitempty"`           // GitHub App ID (App mode)
	InstallationID string `toml:"installation_id,omitempty"`  // numeric installation id (App mode)
	PrivateKeyPath string `toml:"private_key_path,omitempty"` // PATH to the App private-key PEM; never inline PEM
}

// History configures the local review-history store written by every `miucr
// review` run (on by default). A config `enabled = false` opts out globally (the
// per-run opt-out is --no-save); Enabled is a *bool so an absent key inherits the
// default-on, distinct from an explicit false. MaxRecords>0 auto-prunes the
// oldest records after each save.
type History struct {
	Enabled    *bool `toml:"enabled,omitempty"`
	MaxRecords int   `toml:"max_records,omitempty"`
}

// On reports whether history persistence is enabled: nil (unset) inherits the
// default-on, an explicit false disables it.
func (h History) On() bool { return h.Enabled == nil || *h.Enabled }

// Config is the layered configuration: a set of named provider profiles plus
// the profile to use when none is selected on the command line.
type Config struct {
	DefaultProvider string              `toml:"default_provider"`
	Providers       map[string]Provider `toml:"providers"`
	Store           Store               `toml:"store"`
	Embedding       Embedding           `toml:"embedding"`
	Github          Github              `toml:"github"`
	History         History             `toml:"history"`
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
		Store:     Store{Backend: "sqlite"},
		Embedding: Embedding{Enabled: false, Model: DefaultEmbeddingModel, Dim: DefaultEmbeddingDim},
		Github:    Github{Mode: "pat"},
		History:   History{},
	}
}
