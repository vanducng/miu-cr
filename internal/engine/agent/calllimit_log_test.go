package agent

import (
	"bytes"
	stdctx "context"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"
)

func jobLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})).With("ref", "o/r#7", "job_id", 42, "head_sha", "abc1234")
}

// B1: the timeout warning and the slow-call info line carry the job identifiers
// the serve layer put on the context logger, so they can be tied to a PR.
func TestCallLimitLogLinesCarryJobIdentifiers(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithLogger(stdctx.Background(), jobLogger(&buf))

	srv := anthropicTestServer(t, func(w http.ResponseWriter, r *http.Request) { stall(r) })
	_ = repairOnce(newTestAnthropic(srv, 100*time.Millisecond, 5*time.Second), ctx)

	old := slowCallLogAfter
	slowCallLogAfter = 20 * time.Millisecond
	t.Cleanup(func() { slowCallLogAfter = old })
	slow := anthropicTestServer(t, func(w http.ResponseWriter, r *http.Request) { time.Sleep(60 * time.Millisecond); okAnthropic(w) })
	if err := repairOnce(newTestAnthropic(slow, 0, 5*time.Second), ctx); err != nil {
		t.Fatalf("slow call: %v", err)
	}

	out := buf.String()
	for _, want := range []string{
		"provider call timed out", "reason_code=provider_call_timeout", `limit="call timeout"`, "job_id=42", "head_sha=abc1234",
		"slow provider call completed",
	} {
		if !bytes.Contains([]byte(out), []byte(want)) {
			t.Fatalf("log output must contain %q:\n%s", want, out)
		}
	}
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if !bytes.Contains(line, []byte("job_id=42")) {
			t.Fatalf("every provider-call log line needs the job identifiers: %s", line)
		}
	}
}

// B2: agents share one HTTP transport, so a long-running host keeps connection
// pooling instead of building a transport (and idle connections) per review.
func TestProviderAgentsShareOneHTTPTransport(t *testing.T) {
	var mu sync.Mutex
	var conns []string
	srv := anthropicTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		conns = append(conns, r.RemoteAddr)
		mu.Unlock()
		okAnthropic(w)
	})
	for i := 0; i < 2; i++ {
		if err := repairOnce(newTestAnthropic(srv, 0, 5*time.Second), stdctx.Background()); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(conns) != 2 || conns[0] != conns[1] {
		t.Fatalf("two agents must reuse one pooled connection, got %v", conns)
	}
	if providerHTTPClient() != providerHTTPClient() {
		t.Fatal("providerHTTPClient must return the one shared client")
	}
}
