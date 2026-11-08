package agent

import (
	stdctx "context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	"github.com/vanducng/miu-cr/internal/config"
)

// stall blocks until the client gives up (request ctx done) or a safety timeout.
// The body is drained first: net/http only watches for a closed connection once
// the handler has read it.
func stall(r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	select {
	case <-r.Context().Done():
	case <-time.After(5 * time.Second):
	}
}

type recordedRequests struct {
	mu       sync.Mutex
	timeouts []int // X-Stainless-Timeout seconds per request, -1 when absent
}

func (rr *recordedRequests) record(r *http.Request) {
	n := -1
	if v := r.Header.Get("X-Stainless-Timeout"); v != "" {
		n, _ = strconv.Atoi(v)
	}
	rr.mu.Lock()
	rr.timeouts = append(rr.timeouts, n)
	rr.mu.Unlock()
}

func (rr *recordedRequests) all() []int {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	return append([]int(nil), rr.timeouts...)
}

func anthropicTestServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	// Other processes on a shared host probe ephemeral localhost ports; only the
	// provider POST may reach a test handler, or stray requests skew its counters.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestAnthropic(srv *httptest.Server, callCap, job time.Duration) *anthropicAgent {
	return newAnthropicAgent(Credentials{
		Kind: config.KindAnthropic, APIKey: "k", Model: "claude-test",
		BaseURL: srv.URL, CallTimeout: callCap,
	}, job)
}

func okAnthropic(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(textMessage("fixed")))
}

func repairOnce(a *anthropicAgent, ctx stdctx.Context) error {
	_, _, err := a.RepairPatch(ctx, RepairRequest{Span: "s", Rationale: "r", Category: "bug", Severity: "high"})
	return err
}

func asCallTimeout(t *testing.T, err error) *CallTimeoutError {
	t.Helper()
	var cte *CallTimeoutError
	if !errors.As(err, &cte) {
		t.Fatalf("want *CallTimeoutError, got %T: %v", err, err)
	}
	if !errors.Is(err, stdctx.DeadlineExceeded) {
		t.Fatalf("a call timeout must still unwrap to context.DeadlineExceeded: %v", err)
	}
	return cte
}

func TestCallLimitAnthropicDelayedHeaders(t *testing.T) {
	srv := anthropicTestServer(t, func(w http.ResponseWriter, r *http.Request) { stall(r) })
	a := newTestAnthropic(srv, 200*time.Millisecond, 5*time.Second)
	start := time.Now()
	err := repairOnce(a, stdctx.Background())
	cte := asCallTimeout(t, err)
	if cte.Limit != LimitCallTimeout || cte.Budget != 200*time.Millisecond {
		t.Fatalf("want call timeout 200ms, got %+v", cte)
	}
	if !strings.Contains(cte.Phase, "headers") {
		t.Fatalf("want header-wait phase, got %q", cte.Phase)
	}
	if cte.Elapsed < 200*time.Millisecond || time.Since(start) > 3*time.Second {
		t.Fatalf("fired at the wrong time: elapsed=%s total=%s", cte.Elapsed, time.Since(start))
	}
	for _, want := range []string{"call timeout", "200ms", "headers"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q must mention %q", err, want)
		}
	}
}

func TestCallLimitAnthropicDelayedBody(t *testing.T) {
	srv := anthropicTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message",`))
		w.(http.Flusher).Flush()
		stall(r)
	})
	a := newTestAnthropic(srv, 200*time.Millisecond, 5*time.Second)
	cte := asCallTimeout(t, repairOnce(a, stdctx.Background()))
	if cte.Limit != LimitCallTimeout {
		t.Fatalf("want call timeout, got %+v", cte)
	}
	if !strings.Contains(cte.Phase, "body") {
		t.Fatalf("want body-read phase, got %q", cte.Phase)
	}
}

// The SDK stamps the per-request timeout it applies into X-Stainless-Timeout,
// so the header shows the effective limit without waiting ten minutes.
func TestCallLimitNoOverrideUsesJobRemainderNotSDKDefault(t *testing.T) {
	var rec recordedRequests
	srv := anthropicTestServer(t, func(w http.ResponseWriter, r *http.Request) { rec.record(r); okAnthropic(w) })
	a := newTestAnthropic(srv, 0, 60*time.Second)
	if err := repairOnce(a, stdctx.Background()); err != nil {
		t.Fatalf("call: %v", err)
	}
	got := rec.all()
	if len(got) != 1 || got[0] < 55 || got[0] > 60 {
		t.Fatalf("unset call_timeout must give the time left on the job (~60s), not the SDK default 600s: %v", got)
	}
}

func TestCallLimitConfiguredCapIsMinOfCapAndRemaining(t *testing.T) {
	var rec recordedRequests
	srv := anthropicTestServer(t, func(w http.ResponseWriter, r *http.Request) { rec.record(r); okAnthropic(w) })
	if err := repairOnce(newTestAnthropic(srv, 20*time.Second, 60*time.Second), stdctx.Background()); err != nil {
		t.Fatalf("call: %v", err)
	}
	if err := repairOnce(newTestAnthropic(srv, 90*time.Second, 60*time.Second), stdctx.Background()); err != nil {
		t.Fatalf("call: %v", err)
	}
	got := rec.all()
	if len(got) != 2 || got[0] < 19 || got[0] > 20 || got[1] < 55 || got[1] > 60 {
		t.Fatalf("want cap 20s then remaining ~60s, got %v", got)
	}
}

func TestCallLimitNoJobDeadlineUsesCapOrStatedDefault(t *testing.T) {
	var rec recordedRequests
	srv := anthropicTestServer(t, func(w http.ResponseWriter, r *http.Request) { rec.record(r); okAnthropic(w) })
	// --timeout=0: no job deadline anywhere.
	if err := repairOnce(newTestAnthropic(srv, 0, 0), stdctx.Background()); err != nil {
		t.Fatalf("call: %v", err)
	}
	if err := repairOnce(newTestAnthropic(srv, 45*time.Second, 0), stdctx.Background()); err != nil {
		t.Fatalf("call: %v", err)
	}
	got := rec.all()
	wantDefault := int(defaultCallTimeout.Seconds())
	if len(got) != 2 || got[0] != wantDefault || got[1] < 44 || got[1] > 45 {
		t.Fatalf("want stated default %ds then cap 45s, got %v", wantDefault, got)
	}
}

func TestCallLimitJobDeadlineNamedNotCallTimeout(t *testing.T) {
	srv := anthropicTestServer(t, func(w http.ResponseWriter, r *http.Request) { stall(r) })
	// cap far above the job budget: the job deadline fires first.
	a := newTestAnthropic(srv, 10*time.Second, 300*time.Millisecond)
	err := repairOnce(a, stdctx.Background())
	cte := asCallTimeout(t, err)
	if cte.Limit != LimitJobDeadline || cte.Budget != 300*time.Millisecond {
		t.Fatalf("want job deadline 300ms, got %+v", cte)
	}
	if !strings.Contains(err.Error(), "job deadline") || strings.Contains(err.Error(), "call timeout") {
		t.Fatalf("error must name the job deadline only: %v", err)
	}
}

func TestCallLimitLaterTurnGetsOnlyRemainingBudget(t *testing.T) {
	var rec recordedRequests
	var n int
	var mu sync.Mutex
	srv := anthropicTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		mu.Lock()
		n++
		turn := n
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if turn == 1 {
			time.Sleep(1100 * time.Millisecond)
			_, _ = w.Write([]byte(toolUseMessage("tu_1", "grep", map[string]any{"pattern": "needle"})))
			return
		}
		_, _ = w.Write([]byte(textMessage(`{"findings":[]}`)))
	})
	a := newTestAnthropic(srv, 0, 60*time.Second)
	if _, err := a.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir()}); err != nil {
		t.Fatalf("Review: %v", err)
	}
	got := rec.all()
	if len(got) != 2 || got[0] > 60 || got[1] > got[0]-1 {
		t.Fatalf("turn 2 must get only what is left after turn 1 (>=1.1s less): %v", got)
	}
}

func TestCallLimitSDKRetryAttemptGetsItsOwnBoundedTimeout(t *testing.T) {
	var rec recordedRequests
	var n int
	var mu sync.Mutex
	srv := anthropicTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		mu.Lock()
		n++
		attempt := n
		mu.Unlock()
		if attempt == 1 {
			w.Header().Set("Retry-After-Ms", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"busy"}}`))
			return
		}
		stall(r)
	})
	a := newTestAnthropic(srv, 300*time.Millisecond, 5*time.Second)
	start := time.Now()
	cte := asCallTimeout(t, repairOnce(a, stdctx.Background()))
	if cte.Limit != LimitCallTimeout {
		t.Fatalf("the hung retry attempt must hit the call timeout, got %+v", cte)
	}
	if !strings.Contains(cte.Phase, "headers") {
		t.Fatalf("a 503 on attempt 1 must not make attempt 2 look like a body read: %q", cte.Phase)
	}
	if got := rec.all(); len(got) != 2 {
		t.Fatalf("want 2 attempts, got %v", got)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("retry attempt was not bounded by the call timeout: %s", time.Since(start))
	}
}

func TestCallLimitUnidentifiedNestedDeadlineIsNotNamedAsAnyLimit(t *testing.T) {
	lim := callLimits{cap: time.Hour, job: time.Hour}
	_, err := boundedCall(stdctx.Background(), lim, "test.call", func(stdctx.Context, time.Duration) (int, error) {
		return 0, stdctx.DeadlineExceeded
	})
	cte := asCallTimeout(t, err)
	if cte.Limit != LimitNested {
		t.Fatalf("want nested deadline, got %+v", cte)
	}
	if strings.Contains(err.Error(), "10m") {
		t.Fatalf("an unidentified deadline must never be reported as 10m: %v", err)
	}
}

func TestCallLimitOpenAISlowHeaders(t *testing.T) {
	srv := anthropicTestServer(t, func(w http.ResponseWriter, r *http.Request) { stall(r) })
	a := newOpenAIAgent(Credentials{
		Kind: config.KindOpenAI, APIKey: "k", Model: "gpt-test",
		BaseURL: srv.URL, CallTimeout: 200 * time.Millisecond,
	}, 5*time.Second)
	_, _, err := a.RepairPatch(stdctx.Background(), RepairRequest{Span: "s", Rationale: "r", Category: "bug", Severity: "high"})
	cte := asCallTimeout(t, err)
	if cte.Limit != LimitCallTimeout || !strings.Contains(cte.Phase, "headers") {
		t.Fatalf("want call timeout while waiting for headers, got %+v", cte)
	}
}

func TestCallLimitOpenAIGetsJobRemainder(t *testing.T) {
	var rec recordedRequests
	srv := anthropicTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(textCompletion("fixed")))
	})
	a := newOpenAIAgent(Credentials{Kind: config.KindOpenAI, APIKey: "k", Model: "gpt-test", BaseURL: srv.URL}, 60*time.Second)
	if _, _, err := a.RepairPatch(stdctx.Background(), RepairRequest{Span: "s", Rationale: "r", Category: "bug", Severity: "high"}); err != nil {
		t.Fatalf("call: %v", err)
	}
	if got := rec.all(); len(got) != 1 || got[0] < 55 || got[0] > 60 {
		t.Fatalf("openai call must carry the job remainder as its request timeout: %v", got)
	}
}

// The SDK default transport caps response headers at a hidden 10 minutes; the
// per-call budget replaces it, so the transport itself must not add a cap.
func TestProviderHTTPClientHasNoHiddenHeaderTimeout(t *testing.T) {
	tr, ok := providerHTTPClient().Transport.(trackingTransport).base.(*http.Transport)
	if !ok {
		t.Skip("default transport is wrapped")
	}
	if tr.ResponseHeaderTimeout != 0 {
		t.Fatalf("ResponseHeaderTimeout = %s, want 0 (bounded by the per-call budget)", tr.ResponseHeaderTimeout)
	}
}

func TestResolveCallTimeout(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-x")
	cfg := config.Merge(config.Defaults(), config.Config{Review: config.Review{CallTimeout: "12m"}})
	creds, err := resolveWith(cfg, ResolveInput{})
	if err != nil || creds.CallTimeout != 12*time.Minute {
		t.Fatalf("config call_timeout must reach credentials: %v err=%v", creds.CallTimeout, err)
	}
	creds, err = resolveWith(cfg, ResolveInput{CallTimeout: "25m"})
	if err != nil || creds.CallTimeout != 25*time.Minute {
		t.Fatalf("a per-repo override must win: %v err=%v", creds.CallTimeout, err)
	}
	creds, err = resolveWith(config.Defaults(), ResolveInput{})
	if err != nil || creds.CallTimeout != 0 {
		t.Fatalf("unset call_timeout must stay 0 (job remainder): %v err=%v", creds.CallTimeout, err)
	}
	_, err = resolveWith(cfg, ResolveInput{CallTimeout: "soon"})
	var ce *clierr.CLIError
	if !errors.As(err, &ce) || ce.Code != "config.invalid" || !strings.Contains(ce.Message, "call_timeout") {
		t.Fatalf("a bad call_timeout must fail loud as config.invalid, got %v", err)
	}
}
