package agent

import (
	stdctx "context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"
	"time"
)

// TimeoutLimit names which deadline cut a provider call.
type TimeoutLimit string

const (
	LimitJobDeadline TimeoutLimit = "job deadline"
	LimitCallTimeout TimeoutLimit = "call timeout"
	LimitNested      TimeoutLimit = "nested deadline"
)

const (
	// defaultCallTimeout bounds a call when neither a job deadline (--timeout=0)
	// nor [review].call_timeout exists. It matches the review default.
	defaultCallTimeout = 30 * time.Minute
	// slowCallLogAfter logs a completed call that outlived the SDK's former
	// hidden 10 minute limit, as evidence the budget let it finish.
	slowCallLogAfter = 10 * time.Minute

	phaseHeaders = "waiting for response headers"
	phaseBody    = "reading the response body"
)

// CallTimeoutError reports which limit cut a provider call, the budget of that
// limit and the time spent in the call. It unwraps to the underlying error, so
// errors.Is(err, context.DeadlineExceeded) still holds.
type CallTimeoutError struct {
	Limit TimeoutLimit
	// Budget is the call budget for LimitCallTimeout and LimitNested, and the job
	// timeout (0 when unknown) for LimitJobDeadline.
	Budget  time.Duration
	Elapsed time.Duration
	Phase   string
	Label   string
	cause   error
}

func (e *CallTimeoutError) Unwrap() error { return e.cause }

func (e *CallTimeoutError) Error() string {
	where := ""
	if e.Phase != "" {
		where = " while " + e.Phase
	}
	switch e.Limit {
	case LimitCallTimeout:
		return fmt.Sprintf("provider call %s hit the call timeout (%s) after %s%s", e.Label, e.Budget, roundElapsed(e.Elapsed), where)
	case LimitJobDeadline:
		job := ""
		if e.Budget > 0 {
			job = fmt.Sprintf(" (%s)", e.Budget)
		}
		return fmt.Sprintf("provider call %s was cut by the job deadline%s after %s in this call%s", e.Label, job, roundElapsed(e.Elapsed), where)
	default:
		return fmt.Sprintf("provider call %s hit a nested deadline of unknown origin after %s%s (neither its call budget %s nor the job deadline was reached)", e.Label, roundElapsed(e.Elapsed), where, e.Budget)
	}
}

func roundElapsed(d time.Duration) time.Duration {
	if d >= time.Minute {
		return d.Round(time.Second)
	}
	return d.Round(time.Millisecond)
}

// callLimits is the budget policy for one provider client: cap is the configured
// [review].call_timeout (0 = none) and job is the review's own deadline, used only
// to name it in errors (0 = unknown).
type callLimits struct{ cap, job time.Duration }

// budget is min(cap, time left on ctx). With neither, it is defaultCallTimeout:
// never 0, which would restore the SDK's 10 minute default.
func (l callLimits) budget(ctx stdctx.Context) (time.Duration, error) {
	budget := l.cap
	if dl, ok := ctx.Deadline(); ok {
		remaining := time.Until(dl)
		if remaining <= 0 {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			return 0, stdctx.DeadlineExceeded
		}
		if budget <= 0 || remaining < budget {
			budget = remaining
		}
	}
	if budget <= 0 {
		budget = defaultCallTimeout
	}
	return budget, nil
}

// phaseTracker records whether the current HTTP attempt has seen response bytes,
// so a deadline can say if the provider was silent or slow to finish the body.
type phaseTracker struct{ gotResponse atomic.Bool }

func (p *phaseTracker) attach(ctx stdctx.Context) stdctx.Context {
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteRequest:         func(httptrace.WroteRequestInfo) { p.gotResponse.Store(false) },
		GotFirstResponseByte: func() { p.gotResponse.Store(true) },
	})
}

func (p *phaseTracker) phase() string {
	if p.gotResponse.Load() {
		return phaseBody
	}
	return phaseHeaders
}

// boundedCall runs one provider call under its effective budget, which call
// applies as the SDK per-attempt request timeout. A deadline error comes back as
// *CallTimeoutError naming the limit that fired.
func boundedCall[T any](ctx stdctx.Context, lim callLimits, label string, call func(stdctx.Context, time.Duration) (T, error)) (T, error) {
	var zero T
	budget, err := lim.budget(ctx)
	if err != nil {
		return zero, err
	}
	var tracker phaseTracker
	start := time.Now()
	out, err := call(tracker.attach(ctx), budget)
	elapsed := time.Since(start)
	if err == nil {
		if elapsed >= slowCallLogAfter {
			slog.Info("slow provider call completed", "call", label, "budget", budget.String(), "elapsed", roundElapsed(elapsed).String())
		}
		return out, nil
	}
	if !errors.Is(err, stdctx.DeadlineExceeded) || errors.Is(ctx.Err(), stdctx.Canceled) {
		return zero, err
	}
	cte := &CallTimeoutError{Label: label, Elapsed: elapsed, Phase: tracker.phase(), cause: err}
	switch {
	case ctx.Err() != nil:
		cte.Limit, cte.Budget = LimitJobDeadline, lim.job
	case elapsed >= budget:
		cte.Limit, cte.Budget = LimitCallTimeout, budget
	default:
		cte.Limit, cte.Budget = LimitNested, budget
	}
	slog.Warn("provider call timed out", "reason_code", "provider_call_timeout", "limit", string(cte.Limit), "call", label,
		"budget", cte.Budget.String(), "elapsed", roundElapsed(elapsed).String(), "phase", cte.Phase)
	return zero, cte
}

// providerHTTPClient is the SDK default client minus its 10 minute
// ResponseHeaderTimeout: the per-call budget bounds the header wait instead.
func providerHTTPClient() *http.Client {
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t = t.Clone()
		t.ResponseHeaderTimeout = 0
		return &http.Client{Transport: t}
	}
	return &http.Client{Transport: http.DefaultTransport}
}
