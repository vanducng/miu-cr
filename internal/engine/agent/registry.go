package agent

import (
	"fmt"
	"time"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	"github.com/vanducng/miu-cr/internal/config"
)

// Constructor builds an Agent for one provider Kind from resolved Credentials.
// Adding a new first-class provider kind = implement an Agent + register one
// Constructor here; adding a new vendor of an existing kind = config only.
type Constructor func(creds Credentials, timeout time.Duration) Agent

var registry = map[config.Kind]Constructor{}

func register(kind config.Kind, c Constructor) { registry[kind] = c }

func init() {
	register(config.KindAnthropic, func(creds Credentials, timeout time.Duration) Agent {
		return newAnthropicAgent(creds, timeout)
	})
	register(config.KindOpenAI, func(creds Credentials, timeout time.Duration) Agent {
		return newOpenAIAgent(creds, timeout)
	})
}

// New constructs the Agent for the resolved credentials' Kind via the registry.
// An unregistered Kind yields a typed *clierr.CLIError. timeout (the global
// --timeout) bounds both the request context deadline and the tool loop;
// <=0 disables the agent-imposed cap (the caller's ctx still applies).
func New(creds Credentials, timeout time.Duration) (Agent, error) {
	c, ok := registry[creds.Kind]
	if !ok {
		return nil, &clierr.CLIError{
			Code:    "agent.unknown_kind",
			Message: fmt.Sprintf("no agent registered for provider kind %q", creds.Kind),
			Hint:    "use a built-in kind (anthropic, openai) or register a constructor",
			Exit:    1,
		}
	}
	return c(creds, timeout), nil
}
