package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaults(t *testing.T) {
	d := Defaults()
	if d.DefaultProvider != "anthropic" {
		t.Fatalf("default_provider: want anthropic, got %q", d.DefaultProvider)
	}
	a, ok := d.Providers["anthropic"]
	if !ok || a.Kind != KindAnthropic || a.Model != DefaultAnthropicModel {
		t.Fatalf("anthropic default profile wrong: %+v ok=%v", a, ok)
	}
	o, ok := d.Providers["openai"]
	if !ok || o.Kind != KindOpenAI || o.BaseURL != DefaultOpenAIBaseURL || o.Model != DefaultOpenAIModel {
		t.Fatalf("openai default profile wrong: %+v ok=%v", o, ok)
	}
	// No vendor-specific profiles are baked into code defaults.
	for name := range d.Providers {
		if name != "anthropic" && name != "openai" {
			t.Fatalf("unexpected built-in profile %q (vendors must be config-only)", name)
		}
	}
}

func TestMergeFieldOverlayAndNewProfile(t *testing.T) {
	base := Defaults()
	file := Config{
		DefaultProvider: "zai",
		Providers: map[string]Provider{
			// override only the model of a built-in; kind/base must survive.
			"openai": {Model: "gpt-4o-mini"},
			// brand-new vendor profile.
			"zai": {Kind: KindAnthropic, BaseURL: "https://api.z.ai/api/anthropic", Model: "glm-4.6", AuthEnv: "ZAI_API_KEY"},
		},
	}
	got := Merge(base, file)

	if got.DefaultProvider != "zai" {
		t.Fatalf("default_provider override: got %q", got.DefaultProvider)
	}
	o := got.Providers["openai"]
	if o.Kind != KindOpenAI {
		t.Fatalf("merge must preserve built-in kind: %+v", o)
	}
	if o.Model != "gpt-4o-mini" {
		t.Fatalf("merge must apply file model: %+v", o)
	}
	if o.BaseURL != DefaultOpenAIBaseURL {
		t.Fatalf("merge must preserve built-in base_url when file omits it: %+v", o)
	}
	z := got.Providers["zai"]
	if z.Kind != KindAnthropic || z.AuthEnv != "ZAI_API_KEY" || z.BaseURL != "https://api.z.ai/api/anthropic" {
		t.Fatalf("new profile not merged: %+v", z)
	}
	// Merge must not mutate base.
	if _, leaked := base.Providers["zai"]; leaked {
		t.Fatal("Merge mutated the base config")
	}
}

func TestLoadNoFileReturnsDefaults(t *testing.T) {
	dir := t.TempDir()
	userHomeDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { userHomeDir = os.UserHomeDir })

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultProvider != "anthropic" || len(cfg.Providers) != 2 {
		t.Fatalf("absent file must yield defaults, got %+v", cfg)
	}
}

func TestLoadFromFileLayersOverDefaults(t *testing.T) {
	dir := t.TempDir()
	userHomeDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { userHomeDir = os.UserHomeDir })

	body := `default_provider = "zai"

[providers.zai]
kind = "anthropic"
base_url = "https://api.z.ai/api/anthropic"
model = "glm-4.6"
auth_env = "ZAI_API_KEY"

[providers.openai]
model = "gpt-4o-mini"
`
	cfgDir := filepath.Join(dir, ".config", "miu", "cr")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultProvider != "zai" {
		t.Fatalf("file default_provider not applied: %q", cfg.DefaultProvider)
	}
	z, ok := cfg.Providers["zai"]
	if !ok || z.Kind != KindAnthropic || z.BaseURL != "https://api.z.ai/api/anthropic" || z.AuthEnv != "ZAI_API_KEY" {
		t.Fatalf("zai profile not loaded: %+v ok=%v", z, ok)
	}
	// built-in anthropic survives, openai model overridden but kind preserved.
	if cfg.Providers["anthropic"].Kind != KindAnthropic {
		t.Fatal("built-in anthropic dropped after load")
	}
	if cfg.Providers["openai"].Model != "gpt-4o-mini" || cfg.Providers["openai"].Kind != KindOpenAI {
		t.Fatalf("openai overlay wrong: %+v", cfg.Providers["openai"])
	}
}

func TestLoadMalformedReturnsDefaultsAndError(t *testing.T) {
	dir := t.TempDir()
	userHomeDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { userHomeDir = os.UserHomeDir })

	cfgDir := filepath.Join(dir, ".config", "miu", "cr")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("this is = not [valid toml"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err == nil {
		t.Fatal("expected parse error for malformed config")
	}
	if cfg.DefaultProvider != "anthropic" {
		t.Fatalf("malformed file must still yield a usable default baseline, got %+v", cfg)
	}
}

// A config.toml without a [store] section must resolve Backend to the sqlite
// default after Merge (the overlay inherits base, never clears it to empty).
func TestMergeStoreDefaultsToSqlite(t *testing.T) {
	base := Defaults()
	if base.Store.Backend != "sqlite" {
		t.Fatalf("default store backend: want sqlite, got %q", base.Store.Backend)
	}
	out := Merge(base, Config{}) // file with no [store]
	if out.Store.Backend != "sqlite" {
		t.Fatalf("merge without [store] must keep sqlite, got %q", out.Store.Backend)
	}
	if out.Store.DSN != "" {
		t.Fatalf("default DSN must be empty, got %q", out.Store.DSN)
	}
}

// A [store] section overlays backend + DSN onto the base.
func TestMergeStoreOverlay(t *testing.T) {
	out := Merge(Defaults(), Config{Store: Store{Backend: "postgres", DSN: "postgres://h/db"}})
	if out.Store.Backend != "postgres" {
		t.Fatalf("backend overlay: want postgres, got %q", out.Store.Backend)
	}
	if out.Store.DSN != "postgres://h/db" {
		t.Fatalf("dsn overlay: got %q", out.Store.DSN)
	}
}
