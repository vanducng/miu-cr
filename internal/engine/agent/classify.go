package agent

import (
	"github.com/vanducng/miu-cr/internal/cli/clierr"
	"github.com/vanducng/miu-cr/internal/config"
)

// Stable typed-error codes shared across all backends so a consuming agent
// branches on the code regardless of provider. ctx timeout/cancel are typed at
// the review layer (the timeout owner), not here.
const (
	codeAuthExpired    = "agent.auth_expired"
	codeAuthFailed     = "agent.auth_failed"
	codeRateLimited    = "provider.rate_limited"
	codeUnavailable    = "agent.unavailable"
	hintLoginAnthropic = "run `miucr login --provider anthropic` (or set a valid --api-key/ANTHROPIC_API_KEY)"
	hintLoginOpenAI    = "run `miucr login --provider openai`"
)

// classifyStatus maps a proven HTTP status from a backend into the stable typed
// taxonomy, or nil when the status is not one we classify (caller keeps its
// bare %w error so the ctx chain and unknown-error default stay intact). msg is
// redacted defensively; loginHint names the provider-specific login. authCode is
// the 401/403 code: auth_failed for a bad api key, auth_expired for an OAuth token.
func classifyStatus(status int, msg, loginHint, authCode string) *clierr.CLIError {
	switch {
	case status == 401 || status == 403:
		return &clierr.CLIError{
			Code:    authCode,
			Message: config.RedactString(msg),
			Hint:    loginHint,
			Exit:    1,
		}
	case status == 429:
		return &clierr.CLIError{
			Code:    codeRateLimited,
			Message: config.RedactString(msg),
			Hint:    "rate limited — wait for the reset window and retry",
			Exit:    1,
			Retry:   true,
		}
	case status == 529 || (status >= 500 && status <= 599):
		return &clierr.CLIError{
			Code:    codeUnavailable,
			Message: config.RedactString(msg),
			Hint:    "provider unavailable — retry shortly",
			Exit:    1,
			Retry:   true,
		}
	default:
		return nil
	}
}
