package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	userHomeDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { userHomeDir = os.UserHomeDir })
	return dir
}

func readSaved(t *testing.T) (string, os.FileInfo) {
	t.Helper()
	path, err := FilePath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data), fi
}

// Save with a chosen env-name-only provider re-Loads to the same effective
// config and writes no secret.
func TestSaveRoundTripEnvNameOnly(t *testing.T) {
	fakeHome(t)

	cfg := Defaults()
	cfg.DefaultProvider = "zai"
	cfg.Providers["zai"] = Provider{
		Kind:    KindAnthropic,
		BaseURL: "https://api.z.ai/api/anthropic",
		Model:   "glm-4.6",
		AuthEnv: "ZAI_API_KEY",
	}

	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.DefaultProvider != "zai" {
		t.Fatalf("default_provider round-trip: got %q", got.DefaultProvider)
	}
	z := got.Providers["zai"]
	if z.Kind != KindAnthropic || z.BaseURL != "https://api.z.ai/api/anthropic" || z.Model != "glm-4.6" || z.AuthEnv != "ZAI_API_KEY" {
		t.Fatalf("zai profile round-trip: %+v", z)
	}
	// Built-ins still resolve after load (layered, not from disk).
	if got.Providers["anthropic"].Kind != KindAnthropic || got.Providers["openai"].Kind != KindOpenAI {
		t.Fatalf("built-ins missing after load: %+v", got.Providers)
	}

	body, _ := readSaved(t)
	if strings.Contains(body, "auth_token =") {
		t.Fatalf("env-name-only config must not write a secret:\n%s", body)
	}
}

// The written file contains the chosen provider block but NOT the built-in
// anthropic/openai default profiles.
func TestSaveOmitsBuiltinProfiles(t *testing.T) {
	fakeHome(t)

	cfg := Defaults()
	cfg.DefaultProvider = "zai"
	cfg.Providers["zai"] = Provider{Kind: KindAnthropic, BaseURL: "https://gw/anthropic", Model: "glm-4.6", AuthEnv: "ZAI_API_KEY"}

	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	body, _ := readSaved(t)
	if !strings.Contains(body, "[providers.zai]") {
		t.Fatalf("chosen provider block missing:\n%s", body)
	}
	if strings.Contains(body, "[providers.anthropic]") || strings.Contains(body, "[providers.openai]") {
		t.Fatalf("built-in default profiles must not be persisted:\n%s", body)
	}
	for _, section := range []string{"[store]", "[embedding]", "[github]"} {
		if strings.Contains(body, section) {
			t.Fatalf("default %s must not be persisted:\n%s", section, body)
		}
	}
}

// Overriding only the model of a built-in writes just that profile's delta.
func TestSaveBuiltinDeltaOnly(t *testing.T) {
	fakeHome(t)

	cfg := Defaults()
	openai := cfg.Providers["openai"]
	openai.Model = "gpt-4o-mini"
	cfg.Providers["openai"] = openai

	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	body, _ := readSaved(t)
	if !strings.Contains(body, "[providers.openai]") || !strings.Contains(body, "gpt-4o-mini") {
		t.Fatalf("changed built-in must be written:\n%s", body)
	}
	if strings.Contains(body, "[providers.anthropic]") {
		t.Fatalf("unchanged built-in must be omitted:\n%s", body)
	}
	// DefaultProvider unchanged from base -> not written.
	if strings.Contains(body, "default_provider") {
		t.Fatalf("unchanged default_provider must be omitted:\n%s", body)
	}
}

// A literal secret is written only when explicitly set on the profile.
func TestSavePasteNowWritesSecret(t *testing.T) {
	fakeHome(t)

	cfg := Defaults()
	cfg.DefaultProvider = "zai"
	cfg.Providers["zai"] = Provider{Kind: KindAnthropic, BaseURL: "https://gw/anthropic", Model: "glm-4.6", AuthToken: "sk-synthetic-token"}

	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	body, _ := readSaved(t)
	if !strings.Contains(body, "auth_token") || !strings.Contains(body, "sk-synthetic-token") {
		t.Fatalf("explicit auth_token must be written:\n%s", body)
	}
}

// Perms: file 0600, dir 0700; header present.
func TestSavePermsAndHeader(t *testing.T) {
	fakeHome(t)

	cfg := Defaults()
	cfg.DefaultProvider = "openai"
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	body, fi := readSaved(t)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("config file perms: want 0600, got %o", fi.Mode().Perm())
	}
	dir, _ := Dir()
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("config dir perms: want 0700, got %o", di.Mode().Perm())
	}
	if !strings.HasPrefix(body, "# miu-cr config") {
		t.Fatalf("missing header:\n%s", body)
	}
}

// Atomic: no leftover temp files and the installed file is complete.
func TestSaveAtomicNoTempLeftovers(t *testing.T) {
	fakeHome(t)

	cfg := Defaults()
	cfg.DefaultProvider = "openai"
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	dir, _ := Dir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "config.toml")); err != nil {
		t.Fatalf("config.toml not installed: %v", err)
	}
}
