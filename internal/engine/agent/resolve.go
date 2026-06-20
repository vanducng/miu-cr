package agent

import (
	"os"
	"strings"

	"github.com/vanducng/miu-cr/internal/cli"
)

// defaultModel is the pinned review model. Override with ANTHROPIC_MODEL.
const defaultModel = "claude-sonnet-4-5-20250929"

// Credentials is the resolved, in-memory-only auth for an Anthropic call. The
// token is NEVER persisted to disk or the store.
type Credentials struct {
	APIKey string
	Model  string
}

// Resolve picks the API key from the --api-key flag (wins) then the
// ANTHROPIC_API_KEY env var, and the model from ANTHROPIC_MODEL (default
// pinned). Missing credentials return a typed *cli.CLIError.
func Resolve(flagKey string) (Credentials, error) {
	key := strings.TrimSpace(flagKey)
	if key == "" {
		key = strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	}
	if key == "" {
		return Credentials{}, &cli.CLIError{
			Code:    "agent.no_credentials",
			Message: "no Anthropic API key: set ANTHROPIC_API_KEY or pass --api-key",
			Hint:    "export ANTHROPIC_API_KEY=... or run with --api-key",
			Exit:    1,
		}
	}
	model := strings.TrimSpace(os.Getenv("ANTHROPIC_MODEL"))
	if model == "" {
		model = defaultModel
	}
	return Credentials{APIKey: key, Model: model}, nil
}
