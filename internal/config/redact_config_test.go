package config

import (
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
)

// TestRedactConfigMasksEverySecretField injects a unique token into every
// secret-bearing field and asserts none survives in the redacted copy (struct
// fields AND the marshaled text), while non-secret fields are preserved.
func TestRedactConfigMasksEverySecretField(t *testing.T) {
	const (
		tokA = "sk-ant-PROVIDER-A-SECRET"
		tokB = "sk-PROVIDER-B-SECRET"
		dsn  = "postgres://u:STORESECRETPW@host:5432/db"
	)
	cfg := Config{
		DefaultProvider: "a",
		Providers: map[string]Provider{
			"a": {Kind: KindAnthropic, AuthToken: tokA, AuthEnv: "KEEP_ENV_NAME", AuthCommand: []string{"gopass", "show", "sk-command-secret"}, Model: "keep-model"},
			"b": {Kind: KindOpenAI, AuthToken: tokB},
		},
		Store: Store{Backend: "postgres", DSN: dsn},
	}

	safe := RedactConfig(cfg)

	if safe.Providers["a"].AuthToken == tokA || safe.Providers["b"].AuthToken == tokB {
		t.Fatal("provider auth_token not masked structurally")
	}
	if safe.Store.DSN == dsn {
		t.Fatal("store DSN not masked structurally")
	}
	// Non-secret fields preserved.
	if safe.Providers["a"].AuthEnv != "KEEP_ENV_NAME" || safe.Providers["a"].Model != "keep-model" {
		t.Fatal("non-secret fields must be preserved")
	}
	if got := safe.Providers["a"].AuthCommand[2]; strings.Contains(got, "sk-command-secret") {
		t.Fatalf("auth_command token-shaped arg not redacted: %q", got)
	}
	if safe.Store.Backend != "postgres" {
		t.Fatal("non-secret backend must be preserved")
	}

	raw, err := toml.Marshal(safe)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, secret := range []string{tokA, tokB, "STORESECRETPW", dsn, "sk-command-secret"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("secret %q leaked into redacted config text: %s", secret, raw)
		}
	}

	// Original is unmutated.
	if cfg.Providers["a"].AuthToken != tokA || cfg.Store.DSN != dsn {
		t.Fatal("RedactConfig mutated the input")
	}
}

// TestRedactConfigEmptySecretStaysEmpty: an unset secret stays empty so a viewer
// can tell "unset" from "set".
func TestRedactConfigEmptySecretStaysEmpty(t *testing.T) {
	safe := RedactConfig(Config{Providers: map[string]Provider{"a": {Kind: KindAnthropic}}})
	if safe.Providers["a"].AuthToken != "" {
		t.Fatalf("empty auth_token should stay empty, got %q", safe.Providers["a"].AuthToken)
	}
}
