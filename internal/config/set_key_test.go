package config

import (
	"errors"
	"testing"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
)

func init() {
	// validators for review enums (mirrors what the cli layer injects at startup)
	SetReviewValidators(
		func(s string) bool {
			return s == "none" || s == "low" || s == "medium" || s == "high" || s == "critical"
		},
		func(s string) bool { return s == "added" || s == "diff_context" || s == "file" || s == "nofilter" },
		func(s string) bool {
			return s == "info" || s == "low" || s == "medium" || s == "high" || s == "critical"
		},
	)
}

func TestSetKeyHappyPaths(t *testing.T) {
	var cfg Config
	for _, tc := range []struct{ key, val string }{
		{"default_provider", "zai"},
		{"review.gate", "high"},
		{"store.backend", "postgres"},
		{"github.mode", "app"},
		{"embedding.enabled", "true"},
		{"embedding.dim", "1536"},
		{"providers.zai.kind", "anthropic"},
		{"providers.zai.model", "glm-5.2"},
		{"providers.zai.auth_env", "ZAI_API_KEY"},
	} {
		if err := SetKey(&cfg, tc.key, tc.val); err != nil {
			t.Fatalf("SetKey(%q,%q): %v", tc.key, tc.val, err)
		}
	}
	if cfg.DefaultProvider != "zai" || cfg.Review.Gate != "high" || cfg.Store.Backend != "postgres" {
		t.Fatalf("scalars not set: %+v", cfg)
	}
	if cfg.Providers["zai"].Model != "glm-5.2" || cfg.Providers["zai"].AuthEnv != "ZAI_API_KEY" {
		t.Fatalf("provider not set: %+v", cfg.Providers["zai"])
	}
}

func TestSetKeyRejectsSecrets(t *testing.T) {
	var cfg Config
	for _, key := range []string{"store.dsn", "providers.zai.auth_token"} {
		err := SetKey(&cfg, key, "supersecret")
		var ce *clierr.CLIError
		if !errors.As(err, &ce) || ce.Code != "config.invalid" {
			t.Fatalf("SetKey(%q) must reject as config.invalid, got %v", key, err)
		}
		if ce.Message == "" || containsSecret(ce.Message) {
			t.Fatalf("SetKey(%q) error leaked the value: %q", key, ce.Message)
		}
	}
}

func containsSecret(s string) bool {
	for i := 0; i+11 <= len(s); i++ {
		if s[i:i+11] == "supersecret" {
			return true
		}
	}
	return false
}

func TestSetKeyValidatesEnums(t *testing.T) {
	var cfg Config
	for _, tc := range []struct{ key, val string }{
		{"review.gate", "hihg"},
		{"store.backend", "mysql"},
		{"github.mode", "oauth"},
		{"providers.x.kind", "claude"},
		{"embedding.dim", "abc"},
		{"unknown.key", "x"},
	} {
		err := SetKey(&cfg, tc.key, tc.val)
		var ce *clierr.CLIError
		if !errors.As(err, &ce) || ce.Code != "config.invalid" {
			t.Fatalf("SetKey(%q,%q) must be config.invalid, got %v", tc.key, tc.val, err)
		}
	}
}

// Regression: config set review.* must survive Save->Load (savedConfig once dropped Review).
func TestSetKeyReviewPersistsThroughSave(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	var cfg Config
	if err := SetKey(&cfg, "review.gate", "high"); err != nil {
		t.Fatal(err)
	}
	if err := SetKey(&cfg, "default_provider", "zai"); err != nil {
		t.Fatal(err)
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Review.Gate != "high" {
		t.Fatalf("review.gate dropped on Save->Load: %+v", got.Review)
	}
	if got.DefaultProvider != "zai" {
		t.Fatalf("default_provider dropped: %q", got.DefaultProvider)
	}
}
