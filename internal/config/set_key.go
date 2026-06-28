package config

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
)

// SetKey applies a single dotted config key to cfg in place, validating the value and
// refusing any secret-bearing key (those are read from env at runtime, never persisted).
// The caller loads the current config, calls SetKey, then Save, so other keys are kept.
func SetKey(cfg *Config, key, value string) error {
	if key == "store.dsn" || strings.HasSuffix(key, ".auth_token") {
		// Never echo the value: it is a secret by definition, and RedactString can't
		// mask an arbitrary one.
		return &clierr.CLIError{
			Code:    "config.invalid",
			Message: "config set " + key + ": refusing to persist a secret",
			Hint:    "secrets are read from env at runtime; set the env var (e.g. MIUCR_PG_DSN) or an *_env name instead",
			Exit:    2,
		}
	}
	switch {
	case key == "default_provider":
		cfg.DefaultProvider = value
	case key == "review.gate":
		cfg.Review.Gate = value
		return ValidateReview(cfg.Review)
	case key == "review.filter_mode":
		cfg.Review.FilterMode = value
		return ValidateReview(cfg.Review)
	case key == "review.min_severity":
		cfg.Review.MinSeverity = value
		return ValidateReview(cfg.Review)
	case key == "review.format":
		cfg.Review.Format = value
		return ValidateReview(cfg.Review)
	case key == "review.prompt_format":
		cfg.Review.PromptFormat = value
		return ValidateReview(cfg.Review)
	case key == "review.timeout":
		cfg.Review.Timeout = value
		return ValidateReview(cfg.Review)
	case key == "review.expand":
		n, err := strconv.Atoi(value)
		if err != nil {
			return invalidKey(key, value, "an integer >= 0")
		}
		cfg.Review.Expand = &n
		return ValidateReview(cfg.Review)
	case key == "review.token_budget":
		n, err := strconv.Atoi(value)
		if err != nil {
			return invalidKey(key, value, "an integer >= 0")
		}
		cfg.Review.TokenBudget = &n
		return ValidateReview(cfg.Review)
	case key == "review.deep_context":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return invalidKey(key, value, "true|false")
		}
		cfg.Review.DeepContext = &b
		return ValidateReview(cfg.Review)
	case key == "review.context_hops":
		n, err := strconv.Atoi(value)
		if err != nil {
			return invalidKey(key, value, fmt.Sprintf("an integer in [0,%d]", maxReviewContextHops))
		}
		cfg.Review.ContextHops = &n
		return ValidateReview(cfg.Review)
	case key == "review.conversation":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return invalidKey(key, value, "true|false")
		}
		cfg.Review.Conversation = &b
		return ValidateReview(cfg.Review)
	case key == "store.backend":
		if value != "sqlite" && value != "postgres" {
			return invalidKey(key, value, "sqlite|postgres")
		}
		cfg.Store.Backend = value
	case key == "github.mode":
		if value != "pat" && value != "app" {
			return invalidKey(key, value, "pat|app")
		}
		cfg.Github.Mode = value
	case key == "github.app_id":
		cfg.Github.AppID = value
	case key == "github.installation_id":
		cfg.Github.InstallationID = value
	case key == "github.private_key_path":
		cfg.Github.PrivateKeyPath = value
	case key == "embedding.enabled":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return invalidKey(key, value, "true|false")
		}
		cfg.Embedding.Enabled = b
	case key == "embedding.model":
		cfg.Embedding.Model = value
	case key == "embedding.dim":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > MaxEmbeddingDim {
			return invalidKey(key, value, fmt.Sprintf("an integer in [1, %d]", MaxEmbeddingDim))
		}
		cfg.Embedding.Dim = n
	case strings.HasPrefix(key, "providers."):
		return setProviderKey(cfg, key, value)
	default:
		return invalidKey(key, value, "a known config key (see config.example.toml)")
	}
	return nil
}

func setProviderKey(cfg *Config, key, value string) error {
	parts := strings.Split(key, ".")
	if len(parts) != 3 || parts[1] == "" {
		return invalidKey(key, value, "providers.<name>.<field>")
	}
	name, field := parts[1], parts[2]
	if cfg.Providers == nil {
		cfg.Providers = map[string]Provider{}
	}
	p := cfg.Providers[name]
	switch field {
	case "kind":
		if value != "anthropic" && value != "openai" {
			return invalidKey(key, value, "anthropic|openai")
		}
		p.Kind = Kind(value)
	case "base_url":
		p.BaseURL = value
	case "model":
		p.Model = value
	case "auth_env":
		p.AuthEnv = value
	case "auth":
		if value != "" && value != "oauth" && value != "api_key" && value != "bearer" {
			return invalidKey(key, value, "oauth|api_key|bearer")
		}
		p.Auth = value
	default:
		return invalidKey(key, value, "kind|base_url|model|auth_env|auth")
	}
	cfg.Providers[name] = p
	return nil
}

func invalidKey(key, value, want string) error {
	return &clierr.CLIError{
		Code:    "config.invalid",
		Message: fmt.Sprintf("config set %s=%q: want %s", key, RedactString(value), want),
		Hint:    "run `miucr config show --all` to see valid keys; secrets stay in env",
		Exit:    2,
	}
}
