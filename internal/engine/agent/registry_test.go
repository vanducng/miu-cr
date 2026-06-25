package agent

import (
	"errors"
	"testing"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	"github.com/vanducng/miu-cr/internal/config"
)

// Each built-in kind maps to its concrete Agent via the registry, no network.
func TestRegistryKindToConstructor(t *testing.T) {
	anth, err := New(Credentials{Kind: config.KindAnthropic, APIKey: secretToken, Model: "m"}, 0)
	if err != nil {
		t.Fatalf("anthropic: %v", err)
	}
	if _, ok := anth.(*anthropicAgent); !ok {
		t.Fatalf("anthropic kind built %T, want *anthropicAgent", anth)
	}

	oai, err := New(Credentials{Kind: config.KindOpenAI, APIKey: secretToken, Model: "m"}, 0)
	if err != nil {
		t.Fatalf("openai: %v", err)
	}
	if _, ok := oai.(*openaiAgent); !ok {
		t.Fatalf("openai kind built %T, want *openaiAgent", oai)
	}
}

// An unregistered kind is a typed error, not a panic or a silent default.
func TestRegistryUnknownKind(t *testing.T) {
	_, err := New(Credentials{Kind: config.Kind("ollama"), APIKey: "x", Model: "m"}, 0)
	var cerr *clierr.CLIError
	if !errors.As(err, &cerr) || cerr.Code != "agent.unknown_kind" {
		t.Fatalf("expected unknown_kind, got %v", err)
	}
}
