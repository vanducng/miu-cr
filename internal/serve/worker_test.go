package serve

import (
	"io"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func key(owner, repo string, n int) prKey { return prKey{Owner: owner, Repo: repo, Number: n} }

func TestPool_CoalescesSameKey(t *testing.T) {
	var calls atomic.Int64
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	reviewFn := func(Job) error {
		calls.Add(1)
		started <- struct{}{}
		<-release // hold the job in flight so the second Submit coalesces
		return nil
	}
	p := NewPool(reviewFn, discardLog())

	k := key("octocat", "hello", 1)
	if p.Submit(Job{Key: k, Ref: "octocat/hello#1"}) != SubmitQueued {
		t.Fatal("first Submit should enqueue")
	}
	<-started // ensure the job is in flight (in the inflight set)
	if got := p.Submit(Job{Key: k, Ref: "octocat/hello#1"}); got != SubmitCoalesced {
		t.Fatalf("second Submit for same key = %v, want coalesced", got)
	}
	close(release)
	p.Drain()

	if got := calls.Load(); got != 1 {
		t.Fatalf("reviewFn called %d times, want 1 (coalesced)", got)
	}
}

func TestPool_CoalesceDistinguishesSameHead(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	reviewFn := func(Job) error {
		started <- struct{}{}
		<-release
		return nil
	}
	p := NewPool(reviewFn, discardLog())
	k := key("octocat", "hello", 1)
	if got := p.Submit(Job{Key: k, HeadSHA: "same"}); got != SubmitQueued {
		t.Fatalf("first submit = %v, want queued", got)
	}
	<-started
	if got := p.Submit(Job{Key: k, HeadSHA: "same"}); got != SubmitDuplicate {
		t.Fatalf("same-head submit = %v, want duplicate", got)
	}
	if got := p.Submit(Job{Key: k, HeadSHA: "next"}); got != SubmitCoalesced {
		t.Fatalf("different-head submit = %v, want coalesced", got)
	}
	close(release)
	p.Drain()
}

func TestPool_DistinctKeysBothRun(t *testing.T) {
	var calls atomic.Int64
	reviewFn := func(Job) error { calls.Add(1); return nil }
	p := NewPool(reviewFn, discardLog())

	if p.Submit(Job{Key: key("o", "r", 1)}) != SubmitQueued {
		t.Fatal("Submit 1 failed")
	}
	if p.Submit(Job{Key: key("o", "r", 2)}) != SubmitQueued {
		t.Fatal("Submit 2 failed")
	}
	p.Drain()

	if got := calls.Load(); got != 2 {
		t.Fatalf("reviewFn called %d times, want 2", got)
	}
}

func TestPoolWithWorkersOneRunsSerially(t *testing.T) {
	started := make(chan int, 2)
	release := make(chan struct{})
	reviewFn := func(j Job) error {
		started <- j.Key.Number
		<-release
		return nil
	}
	p := NewPoolWithWorkers(reviewFn, discardLog(), 1)
	if p.Submit(Job{Key: key("o", "r", 1)}) != SubmitQueued || p.Submit(Job{Key: key("o", "r", 2)}) != SubmitQueued {
		t.Fatal("submits failed")
	}
	if got := <-started; got != 1 {
		t.Fatalf("first started = %d, want 1", got)
	}
	select {
	case got := <-started:
		t.Fatalf("second job started before first released: %d", got)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	p.Drain()
}

func TestPool_FullQueueLoudDrop(t *testing.T) {
	// Block every worker so the buffered channel fills and the next Submit drops.
	block := make(chan struct{})
	var ran sync.WaitGroup
	reviewFn := func(Job) error {
		ran.Done()
		<-block
		return nil
	}
	p := NewPool(reviewFn, discardLog())

	// Fill: occupy all workers + the whole buffer with distinct keys.
	occupy := cap(p.jobs) + numWorkers(p)
	ran.Add(numWorkers(p))
	n := 0
	for ; n < occupy; n++ {
		if p.Submit(Job{Key: key("o", "r", n)}) != SubmitQueued {
			break // queue filled early
		}
	}

	// One more distinct-key Submit must be dropped (loud + counted).
	dropped := false
	for try := 0; try < 1000; try++ {
		if p.Submit(Job{Key: key("o", "r", 100000+try)}) != SubmitQueued {
			dropped = true
			break
		}
	}
	if !dropped {
		t.Fatal("expected a full-queue drop, got none")
	}
	if p.Drops() < 1 {
		t.Fatalf("drops counter = %d, want >=1", p.Drops())
	}

	close(block)
	p.Drain()
}

func TestPool_PanicSurvival(t *testing.T) {
	var ok atomic.Bool
	done := make(chan struct{})
	first := make(chan struct{})
	reviewFn := func(j Job) error {
		if j.Key.Number == 1 {
			close(first)
			panic("boom")
		}
		ok.Store(true)
		close(done)
		return nil
	}
	p := NewPool(reviewFn, discardLog())

	if p.Submit(Job{Key: key("o", "r", 1)}) != SubmitQueued {
		t.Fatal("panic-job Submit failed")
	}
	<-first
	if p.Submit(Job{Key: key("o", "r", 2)}) != SubmitQueued {
		t.Fatal("post-panic Submit failed")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not survive panic; subsequent job never ran")
	}
	p.Drain()
	if !ok.Load() {
		t.Fatal("subsequent job did not complete")
	}
}

func TestPool_DrainWaitsInFlight(t *testing.T) {
	var completed atomic.Bool
	reviewFn := func(Job) error {
		time.Sleep(50 * time.Millisecond)
		completed.Store(true)
		return nil
	}
	p := NewPool(reviewFn, discardLog())
	if p.Submit(Job{Key: key("o", "r", 1)}) != SubmitQueued {
		t.Fatal("Submit failed")
	}
	p.Drain()
	if !completed.Load() {
		t.Fatal("Drain returned before in-flight job finished")
	}
}

// TestPool_SubmitRacesDrainNoPanic hammers Submit concurrently with Drain under
// -race. The send on p.jobs must never hit a closed channel (the historic "send
// on closed channel" panic), and every Submit after Drain must return false.
func TestPool_SubmitRacesDrainNoPanic(t *testing.T) {
	p := NewPool(func(Job) error { return nil }, discardLog())

	var wg sync.WaitGroup
	for i := range 64 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Submit panicked: %v", r)
				}
			}()
			p.Submit(Job{Key: key("o", "r", n)})
		}(i)
	}
	p.Drain() // races the Submit goroutines above
	wg.Wait()

	if p.Submit(Job{Key: key("o", "r", 999)}) == SubmitQueued {
		t.Fatal("Submit after Drain must return false")
	}
}

func numWorkers(p *Pool) int {
	w := runtime.NumCPU()
	if w < 2 {
		w = 2
	}
	return w
}
