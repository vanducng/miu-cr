package agent

import (
	stdctx "context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vanducng/miu-cr/internal/config"
)

// progressRecorder collects progress messages; safe for the ticker goroutine.
type progressRecorder struct {
	mu   sync.Mutex
	msgs []string
}

func (r *progressRecorder) record(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, s)
}

func (r *progressRecorder) waiting() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, m := range r.msgs {
		if strings.Contains(m, "waiting on") {
			out = append(out, m)
		}
	}
	return out
}

func shortWaitInterval(t *testing.T, d time.Duration) {
	t.Helper()
	old := providerWaitProgressInterval
	providerWaitProgressInterval = d
	t.Cleanup(func() { providerWaitProgressInterval = old })
}

// A provider call that outlives the tick interval must emit waiting progress —
// without it, the stalled-review watchdog (progress-silence based) cancels a
// review the provider is still working on (observed on a 90-file PR whose
// single turn ran past the 8m stall timeout).
func TestCallWithWaitProgressTicksDuringLongCall(t *testing.T) {
	shortWaitInterval(t, 10*time.Millisecond)
	rec := &progressRecorder{}

	out, err := callWithWaitProgress(stdctx.Background(), rec.record, "anthropic.messages", func() (string, error) {
		time.Sleep(80 * time.Millisecond)
		return "ok", nil
	})
	if err != nil || out != "ok" {
		t.Fatalf("call result = %q, %v", out, err)
	}
	ticks := rec.waiting()
	if len(ticks) == 0 {
		t.Fatal("expected waiting-progress ticks during a long provider call (stall watchdog would fire without them)")
	}
	if !strings.Contains(ticks[0], "waiting on anthropic.messages") {
		t.Fatalf("tick must name the call: %q", ticks[0])
	}
}

func TestCallWithWaitProgressNilProgress(t *testing.T) {
	shortWaitInterval(t, time.Millisecond)
	out, err := callWithWaitProgress(stdctx.Background(), nil, "x", func() (int, error) {
		time.Sleep(10 * time.Millisecond)
		return 7, nil
	})
	if err != nil || out != 7 {
		t.Fatalf("call result = %d, %v", out, err)
	}
}

// The wiring: retryProviderCall must tick waiting progress while its call is in
// flight (this is the path the anthropic/openai review loops use).
func TestRetryProviderCallTicksWaitingProgress(t *testing.T) {
	shortWaitInterval(t, 10*time.Millisecond)
	rec := &progressRecorder{}

	out, err := retryProviderCall(stdctx.Background(), config.ProviderRetry{}, rec.record, "openai.chat",
		func() (string, error) {
			time.Sleep(60 * time.Millisecond)
			return "done", nil
		},
		func(err error) error { return err },
		func(err error) (providerRetryReason, bool) { return providerRetryReason{}, false },
	)
	if err != nil || out != "done" {
		t.Fatalf("call result = %q, %v", out, err)
	}
	if len(rec.waiting()) == 0 {
		t.Fatal("retryProviderCall must emit waiting-progress ticks during a long call")
	}
}
