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
// with its own base_url/model and a credential reference. The credential source
// can be AuthEnv, AuthCommand, or legacy AuthToken. Standard env vars
// (ANTHROPIC_API_KEY, …) and CLI flags still override a profile credential.
type Provider struct {
	Kind        Kind     `toml:"kind"`
	BaseURL     string   `toml:"base_url,omitempty"`
	Model       string   `toml:"model,omitempty"`
	AuthToken   string   `toml:"auth_token,omitempty"`   // literal credential; wins over AuthEnv/AuthCommand
	AuthEnv     string   `toml:"auth_env,omitempty"`     // NAME of an env var holding the credential
	AuthCommand []string `toml:"auth_command,omitempty"` // argv command; stdout is the credential
	// Auth pins the credential method: "oauth", "api_key", "bearer", or "" for
	// legacy auto behavior.
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
// Enabled is explicitly true AND the store backend is postgres, never enabled
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
// PEM file (never inline PEM, RedactString cannot mask a multi-line key); the
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

// Review carries review-attribute defaults plus presentation-only options. The
// Gate/FilterMode/MinSeverity/Timeout/Suggest fields are CLI defaults: an
// explicit flag always wins (review.go checks cmd.Flags().Changed); an unset flag
// falls back to these. An empty/nil value means "no config default" (use the
// flag default). Suggest is a *bool so an absent key is distinct from an explicit
// false (mirrors History.Enabled). Timeout is a Go duration string ("300s").
// CategoryURLs maps a finding Category (matched case-insensitively) to a docs URL
// so a mapped category renders as a clickable link in PR comments/summary and
// sets the SARIF helpUri. This struct is TRUSTED config only (user file +
// built-in defaults), never sourced from repo .miu/cr/rules, so a fork-PR rule
// cannot inject a link or override a review default.
type Review struct {
	Gate         string            `toml:"gate,omitempty"`
	FilterMode   string            `toml:"filter_mode,omitempty"`
	MinSeverity  string            `toml:"min_severity,omitempty"`
	Timeout      string            `toml:"timeout,omitempty"`
	Suggest      *bool             `toml:"suggest,omitempty"`
	PatchRepair  *bool             `toml:"patch_repair,omitempty"`
	CategoryURLs map[string]string `toml:"category_urls,omitempty"`
}

// Config is the layered configuration: a set of named provider profiles plus
// the profile to use when none is selected on the command line.
type Config struct {
	DefaultProvider string              `toml:"default_provider"`
	Providers       map[string]Provider `toml:"providers"`
	Store           Store               `toml:"store"`
	Embedding       Embedding           `toml:"embedding"`
	Github          Github              `toml:"github"`
	History         History             `toml:"history"`
	Review          Review              `toml:"review"`
}

// Defaults returns the built-in configuration: the two first-class kinds as
// like-named profiles. Specific vendors (z.ai/GLM, etc.) are intentionally NOT
// baked in here, add them via the config file (see docs).
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
