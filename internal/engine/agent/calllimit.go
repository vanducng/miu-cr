package agent

import (
	stdctx "context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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

// callState is what the HTTP transport saw during one boundedCall. It is carried
// in the request context so the transport can update it for every SDK attempt.
type callState struct {
	headersDone    atomic.Bool // the current attempt received complete response headers
	attemptExpired atomic.Bool // the current attempt's own context hit its deadline
}

type callStateKey struct{}

func (s *callState) phase() string {
	if s.headersDone.Load() {
		return phaseBody
	}
	return phaseHeaders
}

func (s *callState) note(attemptCtx stdctx.Context) {
	if errors.Is(attemptCtx.Err(), stdctx.DeadlineExceeded) {
		s.attemptExpired.Store(true)
	}
}

// trackingTransport resets the call state at the start of every HTTP attempt,
// treats a returned response as "headers complete", and records whether the
// attempt's own context expired, so a later attempt never inherits an earlier
// attempt's phase or expiry.
type trackingTransport struct{ base http.RoundTripper }

func withPhaseTracking(base http.RoundTripper) http.RoundTripper { return trackingTransport{base} }

func (t trackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s, _ := req.Context().Value(callStateKey{}).(*callState)
	if s == nil {
		return t.base.RoundTrip(req)
	}
	s.headersDone.Store(false)
	s.attemptExpired.Store(false)
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		s.note(req.Context())
		return resp, err
	}
	s.headersDone.Store(true)
	if resp.Body != nil { // net/http fills in a nil body itself; do not hide it
		resp.Body = &trackedBody{ReadCloser: resp.Body, state: s, ctx: req.Context()}
	}
	return resp, nil
}

type trackedBody struct {
	io.ReadCloser
	state *callState
	ctx   stdctx.Context
}

func (b *trackedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && err != io.EOF {
		b.state.note(b.ctx)
	}
	return n, err
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
	var state callState
	start := time.Now()
	out, err := call(stdctx.WithValue(ctx, callStateKey{}, &state), budget)
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
	cte := &CallTimeoutError{Label: label, Elapsed: elapsed, Phase: state.phase(), cause: err}
	switch {
	case ctx.Err() != nil:
		cte.Limit, cte.Budget = LimitJobDeadline, lim.job
	case state.attemptExpired.Load():
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
		return &http.Client{Transport: withPhaseTracking(t)}
	}
	return &http.Client{Transport: withPhaseTracking(http.DefaultTransport)}
}
