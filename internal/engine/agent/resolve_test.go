package agent

import (
	"errors"
	"testing"

	"github.com/vanducng/miu-cr/internal/cli"
)

func TestResolveFlagWinsOverEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	t.Setenv("ANTHROPIC_MODEL", "")
	creds, err := Resolve("flag-key")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if creds.APIKey != "flag-key" {
		t.Fatalf("flag must win: got %q", creds.APIKey)
	}
	if creds.Model != defaultModel {
		t.Fatalf("default model expected, got %q", creds.Model)
	}
}

func TestResolveEnvKeyAndModel(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	t.Setenv("ANTHROPIC_MODEL", "custom-model")
	creds, err := Resolve("")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if creds.APIKey != "env-key" {
		t.Fatalf("env key expected, got %q", creds.APIKey)
	}
	if creds.Model != "custom-model" {
		t.Fatalf("env model expected, got %q", creds.Model)
	}
}

func TestResolveMissingKeyTypedError(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	_, err := Resolve("   ")
	if err == nil {
		t.Fatal("expected error for missing credentials")
	}
	var cerr *cli.CLIError
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *cli.CLIError, got %T", err)
	}
	if cerr.Code != "agent.no_credentials" {
		t.Fatalf("unexpected code %q", cerr.Code)
	}
}
