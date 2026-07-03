package agent

import (
	stdctx "context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	openai "github.com/openai/openai-go/v3"

	"github.com/vanducng/miu-cr/internal/config"
)

type providerRetryPolicy struct {
	maxRetries     int
	initialBackoff time.Duration
	maxBackoff     time.Duration
	maxElapsed     time.Duration
}

type providerRetryReason struct {
	code   string
	status int
}

func (r providerRetryReason) String() string {
	if r.status > 0 {
		return fmt.Sprintf("%s status=%d", r.code, r.status)
	}
	return r.code
}

// providerWaitProgressInterval is how often an in-flight provider call reports
// waiting progress. A single call on a huge diff can legitimately run for many
// minutes (observed: 4-8+ min per turn on a 90-file PR); without these ticks the
// stalled-review watchdog (which cancels after N minutes of progress silence)
// kills a review the provider is still working on. Var so tests can shorten it.
var providerWaitProgressInterval = time.Minute

// callWithWaitProgress runs call, ticking progress with "waiting on <label>
// (elapsed)" every providerWaitProgressInterval until it returns, so a long
// provider call keeps resetting the stalled-review watchdog. A genuinely dead
// call is still bounded by the overall review timeout.
func callWithWaitProgress[T any](ctx stdctx.Context, progress func(string), label string, call func() (T, error)) (T, error) {
	if progress == nil {
		return call()
	}
	done := make(chan struct{})
	defer close(done)
	start := time.Now()
	go func() {
		ticker := time.NewTicker(providerWaitProgressInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				progress(fmt.Sprintf("waiting on %s (%s)", label, time.Since(start).Round(time.Second)))
			}
		}
	}()
	return call()
}

func retryProviderCall[T any](ctx stdctx.Context, retry config.ProviderRetry, progress func(string), label string, call func() (T, error), classify func(error) error, retryable func(error) (providerRetryReason, bool)) (T, error) {
	policy := resolveProviderRetryPolicy(retry, config.DefaultProviderRetryMaxRetries)
	var zero T
	start := time.Now()
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		out, err := callWithWaitProgress(ctx, progress, label, call)
		if err == nil {
			return out, nil
		}
		if isCtxErr(err) {
			return zero, err
		}
		reason, ok := retryable(err)
		if !ok || attempt >= policy.maxRetries {
			return zero, classify(err)
		}
		wait := providerBackoff(policy, attempt+1, 0)
		if policy.maxElapsed > 0 {
			remaining := policy.maxElapsed - time.Since(start)
			if remaining <= 0 {
				return zero, classify(err)
			}
			if wait > remaining {
				wait = remaining
			}
		}
		if progress != nil {
			progress(fmt.Sprintf("provider retry %d/%d for %s after %s; waiting %s", attempt+1, policy.maxRetries, label, reason, wait))
		}
		if err := sleepCtx(ctx, wait); err != nil {
			return zero, err
		}
	}
}

func resolveProviderRetryPolicy(retry config.ProviderRetry, fallbackMaxRetries int) providerRetryPolicy {
	maxRetries := fallbackMaxRetries
	if maxRetries < 0 {
		maxRetries = config.DefaultProviderRetryMaxRetries
	}
	if retry.MaxRetries != nil {
		maxRetries = *retry.MaxRetries
	}
	if maxRetries < 0 {
		maxRetries = 0
	}
	return providerRetryPolicy{
		maxRetries:     maxRetries,
		initialBackoff: providerRetryDuration(retry.InitialBackoff, config.DefaultProviderRetryInitialBackoff),
		maxBackoff:     providerRetryDuration(retry.MaxBackoff, config.DefaultProviderRetryMaxBackoff),
		maxElapsed:     providerRetryDuration(retry.MaxElapsed, config.DefaultProviderRetryMaxElapsed),
	}
}

func providerRetryDuration(raw, fallback string) time.Duration {
	if strings.TrimSpace(raw) == "" {
		raw = fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		d, _ = time.ParseDuration(fallback)
	}
	return d
}

func providerBackoff(policy providerRetryPolicy, retryNumber int, suggested time.Duration) time.Duration {
	if suggested > 0 {
		if policy.maxBackoff > 0 && suggested > policy.maxBackoff {
			return policy.maxBackoff
		}
		return suggested
	}
	d := policy.initialBackoff
	for i := 1; i < retryNumber; i++ {
		d *= 2
		if policy.maxBackoff > 0 && d >= policy.maxBackoff {
			d = policy.maxBackoff
			break
		}
	}
	if d <= 0 {
		return 0
	}
	half := int64(d) / 2
	if half <= 0 {
		return d
	}
	return time.Duration(half + rand.Int63n(half+1))
}

func anthropicProviderRetryable(err error) (providerRetryReason, bool) {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode == 429 || (apiErr.StatusCode >= 500 && apiErr.StatusCode <= 599) {
			return providerRetryReason{code: providerRetryCode(apiErr.StatusCode), status: apiErr.StatusCode}, true
		}
		return providerRetryReason{}, false
	}
	return transportRetryable(err)
}

func openAIProviderRetryable(err error) (providerRetryReason, bool) {
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode == 429 || (apiErr.StatusCode >= 500 && apiErr.StatusCode <= 599) {
			return providerRetryReason{code: providerRetryCode(apiErr.StatusCode), status: apiErr.StatusCode}, true
		}
		return providerRetryReason{}, false
	}
	return transportRetryable(err)
}

func providerRetryCode(status int) string {
	if status == 429 {
		return codeRateLimited
	}
	return codeUnavailable
}

func transportRetryable(err error) (providerRetryReason, bool) {
	var netErr net.Error
	if errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) {
		return providerRetryReason{code: codeUnavailable}, true
	}
	s := strings.ToLower(err.Error())
	for _, needle := range []string{"connection reset", "connection refused", "broken pipe", "temporary failure", "timeout", "eof"} {
		if strings.Contains(s, needle) {
			return providerRetryReason{code: codeUnavailable}, true
		}
	}
	return providerRetryReason{}, false
}
