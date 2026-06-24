package agent

import (
	stdctx "context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	"github.com/vanducng/miu-cr/internal/config"
)

// Backoff knobs are vars (not consts) only so a test can shrink the sleeps;
// production uses these defaults.
var (
	codexMaxAttempts   = 3 // initial try + 2 retries, matching the SDK backends
	codexBaseBackoff   = 500 * time.Millisecond
	codexMaxBackoff    = 8 * time.Second
	codexMaxRetryAfter = 30 * time.Second // cap an honored Retry-After/resets_in so we never block on a long usage-cap window
)

// codexRetryable wraps a transient codex failure (429/502/503/504 or a
// response.failed SSE event) the post() loop may retry. resetsIn/retryAfter
// carry an upstream-suggested wait when known (0 ⇒ use computed backoff). It is
// never the value returned to the caller — on give-up the loop converts it to a
// typed CLIError; a non-retryable status returns its CLIError directly.
type codexRetryable struct {
	status     int
	resetsIn   time.Duration // usage_limit_reached resets_in_seconds (429 body)
	retryAfter time.Duration // Retry-After header
	failed     bool          // response.failed SSE event (no HTTP status)
	msg        string        // redacted, for the final classified message
}

func (e *codexRetryable) Error() string {
	if e.failed {
		return "codex response failed"
	}
	return fmt.Sprintf("codex backend status %d", e.status)
}

// rateLimitError builds the terminal provider.rate_limited CLIError from a 429,
// surfacing the usage-cap reset window in Details + an actionable Hint. Mirrors
// publish.go's mapWriteError shape.
func (e *codexRetryable) rateLimitError() *clierr.CLIError {
	ce := &clierr.CLIError{
		Code:    codeRateLimited,
		Message: config.RedactString(e.msg),
		Hint:    "rate limited — wait for the reset window and retry, or try another provider",
		Exit:    1,
		Retry:   true,
	}
	if d := e.resetsIn; d > 0 {
		ce.Details = map[string]any{"resets_in_seconds": int(d.Seconds())}
		ce.Hint = fmt.Sprintf("usage cap reached — resets in ~%s; wait for the reset or try another provider", humanizeDuration(d))
	} else if d := e.retryAfter; d > 0 {
		ce.Details = map[string]any{"retry_after_seconds": int(d.Seconds())}
	}
	return ce
}

func humanizeDuration(d time.Duration) string {
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%.0fh", d.Hours())
	case d >= time.Minute:
		return fmt.Sprintf("%.0fm", d.Minutes())
	default:
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
}

// codexRetryStatus reports whether an HTTP status is a transient codex failure
// the loop retries. 401 is handled by the single-refresh path; other 4xx are
// terminal (typed by classifyStatus).
func codexRetryStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, // 429
		http.StatusBadGateway,         // 502
		http.StatusServiceUnavailable, // 503
		http.StatusGatewayTimeout:     // 504
		return true
	}
	return false
}

// parseResetsIn pulls resets_in_seconds from a 429 usage_limit_reached body.
// Tolerant of the field nested under error{} or top-level. Returns 0 if absent.
func parseResetsIn(body string) time.Duration {
	var env struct {
		ResetsInSeconds *float64 `json:"resets_in_seconds"`
		Error           struct {
			ResetsInSeconds *float64 `json:"resets_in_seconds"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &env) != nil {
		return 0
	}
	v := env.ResetsInSeconds
	if v == nil {
		v = env.Error.ResetsInSeconds
	}
	if v == nil || *v <= 0 {
		return 0
	}
	return time.Duration(*v * float64(time.Second))
}

// parseRetryAfter reads a Retry-After header (delta-seconds form only). 0 if absent/unparseable.
func parseRetryAfter(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return 0
}

// codexBackoff returns the wait before attempt n (1-based): exponential base*2^(n-1),
// capped, with full jitter. An upstream-suggested wait (resets_in/Retry-After),
// when set and within cap, wins so we honor the server. ctx-gating is the caller's.
func codexBackoff(attempt int, suggested time.Duration) time.Duration {
	if suggested > 0 {
		if suggested > codexMaxRetryAfter {
			suggested = codexMaxRetryAfter
		}
		return suggested
	}
	d := codexBaseBackoff << (attempt - 1)
	if d > codexMaxBackoff {
		d = codexMaxBackoff
	}
	half := int64(d) / 2
	return time.Duration(half + rand.Int63n(half+1)) // equal jitter [d/2, d] — never 0, so a 429 burst actually backs off
}

// sleepCtx waits d, aborting promptly on ctx cancel/deadline (returns the ctx error).
func sleepCtx(ctx stdctx.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// isCtxErr reports a context cancel/deadline (so the loop never retries a ctx abort).
func isCtxErr(err error) bool {
	return errors.Is(err, stdctx.Canceled) || errors.Is(err, stdctx.DeadlineExceeded)
}
