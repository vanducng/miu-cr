package agent

import (
	stdctx "context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

func trackedAgent(baseURL string, base http.RoundTripper, maxRetries int, callCap, job time.Duration, extra ...option.RequestOption) *anthropicAgent {
	if base == nil {
		base = http.DefaultTransport
	}
	opts := append([]option.RequestOption{
		option.WithBaseURL(baseURL), option.WithAPIKey("k"), option.WithMaxRetries(maxRetries),
		option.WithHTTPClient(&http.Client{Transport: withPhaseTracking(base)}),
	}, extra...)
	return &anthropicAgent{
		client: sdkAnthropicClient{sdk: anthropic.NewClient(opts...), limits: callLimits{cap: callCap, job: job}},
		model:  "claude-test", timeout: job,
	}
}

func busy503(w http.ResponseWriter, delay time.Duration) {
	time.Sleep(delay)
	w.Header().Set("Retry-After-Ms", "1")
	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"busy"}}`))
}

// S07-R1-01: expiry is decided by the attempt's own context, never by aggregate
// elapsed time. Attempt 2 fails with an injected DeadlineExceeded while its own
// context is live, at ~1.15s of total time against a 1s per-attempt cap (each
// attempt alone stays well under the cap so a loaded machine cannot expire it).
func TestCallLimitNestedDeadlineOnLaterAttemptIsNotACallTimeout(t *testing.T) {
	var attempts atomic.Int32
	srv := anthropicTestServer(t, func(w http.ResponseWriter, r *http.Request) { busy503(w, 650*time.Millisecond) })
	inject := option.WithMiddleware(func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		if attempts.Add(1) == 1 {
			return next(req)
		}
		time.Sleep(500 * time.Millisecond)
		return nil, stdctx.DeadlineExceeded
	})
	a := trackedAgent(srv.URL, nil, 1, time.Second, 10*time.Second, inject)
	err := repairOnce(a, stdctx.Background())
	cte := asCallTimeout(t, err)
	if cte.Limit != LimitNested {
		t.Fatalf("a deadline from outside the attempt's own context must be unknown origin, got %+v", cte)
	}
	if attempts.Load() != 2 {
		t.Fatalf("want 2 attempts, got %d", attempts.Load())
	}
}

// S07-R1-02: a response that has only started its status line has not delivered
// headers, so a stall there is a header wait.
func TestCallLimitPartialHeadersAreStillAHeaderWait(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buf := make([]byte, 8192)
				n, _ := conn.Read(buf)
				if !strings.HasPrefix(string(buf[:n]), "POST ") { // ignore stray probes of this port
					return
				}
				_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: appl"))
				_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
				_, _ = conn.Read(buf)
			}()
		}
	}()
	a := trackedAgent("http://"+ln.Addr().String(), nil, 0, 200*time.Millisecond, 5*time.Second)
	cte := asCallTimeout(t, repairOnce(a, stdctx.Background()))
	if cte.Limit != LimitCallTimeout || cte.Phase != phaseHeaders {
		t.Fatalf("partial headers must report the header wait, got %+v", cte)
	}
}

// S07-R1-03: every HTTP attempt starts back in the header phase, including one
// that stalls while dialing, before any request is written.
func TestCallLimitRetryStallingBeforeWriteIsNotABodyRead(t *testing.T) {
	srv := anthropicTestServer(t, func(w http.ResponseWriter, r *http.Request) { busy503(w, 0) })
	var dials atomic.Int32
	base := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{}
	base.DialContext = func(ctx stdctx.Context, network, addr string) (net.Conn, error) {
		if dials.Add(1) == 1 {
			return dialer.DialContext(ctx, network, addr)
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	a := trackedAgent(srv.URL, base, 1, 300*time.Millisecond, 5*time.Second)
	err := repairOnce(a, stdctx.Background())
	cte := asCallTimeout(t, err)
	if cte.Limit != LimitCallTimeout || cte.Phase != phaseHeaders {
		t.Fatalf("attempt 2 stalled before its request was written; want call timeout in the header phase, got %+v", cte)
	}
	if !strings.Contains(err.Error(), "response headers") || dials.Load() != 2 {
		t.Fatalf("unexpected: dials=%d err=%v", dials.Load(), err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// S07-R2-01: a RoundTripper may return a nil Body for an empty response; net/http
// fills it in, so the tracking wrapper must not hide the nil behind its own body.
func TestCallLimitTrackingTransportToleratesNilResponseBody(t *testing.T) {
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, ContentLength: 0, Header: make(http.Header), Request: r}, nil
	})
	a := trackedAgent("http://provider.invalid", base, 0, time.Second, 5*time.Second)
	err := repairOnce(a, stdctx.Background())
	if err == nil {
		t.Fatal("an empty 200 is not a valid message; want the SDK decode error")
	}
	var cte *CallTimeoutError
	if errors.As(err, &cte) {
		t.Fatalf("an empty response is not a timeout: %v", err)
	}
}
