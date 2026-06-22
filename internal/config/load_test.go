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

// No [embedding] section -> disabled by default with the built-in model/dim.
func TestEmbeddingDefaultsDisabled(t *testing.T) {
	d := Defaults()
	if d.Embedding.Enabled {
		t.Fatal("embedding must be disabled by default")
	}
	if d.Embedding.Model != DefaultEmbeddingModel || d.Embedding.Dim != DefaultEmbeddingDim {
		t.Fatalf("embedding defaults wrong: %+v", d.Embedding)
	}
	out := Merge(d, Config{}) // file with no [embedding]
	if out.Embedding.Enabled {
		t.Fatal("merge without [embedding] must keep disabled")
	}
	if out.Embedding.Model != DefaultEmbeddingModel || out.Embedding.Dim != DefaultEmbeddingDim {
		t.Fatalf("merge without [embedding] dropped defaults: %+v", out.Embedding)
	}
}

// An [embedding] section flips enabled and overlays fields; absent fields inherit
// the base defaults.
func TestMergeEmbeddingOverlay(t *testing.T) {
	out := Merge(Defaults(), Config{Embedding: Embedding{Enabled: true, BaseURL: "https://gw/v1"}})
	if !out.Embedding.Enabled {
		t.Fatal("enabled overlay not applied")
	}
	if out.Embedding.BaseURL != "https://gw/v1" {
		t.Fatalf("base_url overlay: got %q", out.Embedding.BaseURL)
	}
	if out.Embedding.Model != DefaultEmbeddingModel || out.Embedding.Dim != DefaultEmbeddingDim {
		t.Fatalf("absent model/dim must inherit defaults: %+v", out.Embedding)
	}
}

func TestLoadEmbeddingFromFile(t *testing.T) {
	dir := t.TempDir()
	userHomeDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { userHomeDir = os.UserHomeDir })

	body := `[embedding]
enabled = true
model = "text-embedding-3-large"
dim = 256
base_url = "https://gw.example/v1"
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
	if !cfg.Embedding.Enabled || cfg.Embedding.Model != "text-embedding-3-large" || cfg.Embedding.Dim != 256 || cfg.Embedding.BaseURL != "https://gw.example/v1" {
		t.Fatalf("embedding not loaded: %+v", cfg.Embedding)
	}
}

// No [github] section -> mode defaults to pat with empty App fields.
func TestGithubDefaultsToPat(t *testing.T) {
	d := Defaults()
	if d.Github.Mode != "pat" {
		t.Fatalf("default github mode: want pat, got %q", d.Github.Mode)
	}
	out := Merge(d, Config{}) // file with no [github]
	if out.Github.Mode != "pat" {
		t.Fatalf("merge without [github] must keep pat, got %q", out.Github.Mode)
	}
	if out.Github.AppID != "" || out.Github.InstallationID != "" || out.Github.PrivateKeyPath != "" {
		t.Fatalf("default github App fields must be empty: %+v", out.Github)
	}
}

// A [github] section overlays mode + App fields; absent mode inherits the default.
func TestMergeGithubOverlay(t *testing.T) {
	out := Merge(Defaults(), Config{Github: Github{
		Mode:           "app",
		AppID:          "12345",
		InstallationID: "99",
		PrivateKeyPath: "/etc/miucr/app.pem",
	}})
	if out.Github.Mode != "app" {
		t.Fatalf("mode overlay: want app, got %q", out.Github.Mode)
	}
	if out.Github.AppID != "12345" || out.Github.InstallationID != "99" || out.Github.PrivateKeyPath != "/etc/miucr/app.pem" {
		t.Fatalf("app fields overlay: %+v", out.Github)
	}

	// Overlaying only App fields (no mode) inherits the default pat mode.
	partial := Merge(Defaults(), Config{Github: Github{AppID: "678"}})
	if partial.Github.Mode != "pat" {
		t.Fatalf("absent mode must inherit pat, got %q", partial.Github.Mode)
	}
}
