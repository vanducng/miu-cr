package serve

import (
	"fmt"
	"log/slog"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"

	"github.com/vanducng/miu-cr/internal/config"
)

// Pool is a bounded worker pool implementing Dispatcher. A mutex-guarded
// in-flight set keyed by {owner,repo,number} coalesces duplicate jobs for the
// same PR; a full queue is loud-logged and counted, never silently dropped; each
// job runs under recover() so one panic can't kill a worker.
type Pool struct {
	jobs     chan Job
	mu       sync.Mutex
	inflight map[prKey]string
	closed   bool // guarded by mu; once true, Submit refuses and jobs is closed
	wg       sync.WaitGroup
	reviewFn func(Job) error
	log      *slog.Logger
	drops    atomic.Int64
}

// NewPool starts workers=max(2,NumCPU) goroutines draining a buffered channel of
// size 4*workers. reviewFn runs each job (the real one calls cli.ReviewPRForServe);
// its returned error (and any recovered panic) is passed to Job.OnDone so callers
// record per-head dedup state only on a genuine success.
func NewPool(reviewFn func(Job) error, log *slog.Logger) *Pool {
	if log == nil {
		log = slog.Default()
	}
	workers := runtime.NumCPU()
	if workers < 2 {
		workers = 2
	}
	return NewPoolWithWorkers(reviewFn, log, workers)
}

// NewPoolWithWorkers lets host mode bind review concurrency from trusted config.
func NewPoolWithWorkers(reviewFn func(Job) error, log *slog.Logger, workers int) *Pool {
	if log == nil {
		log = slog.Default()
	}
	if workers <= 0 {
		workers = 1
	}
	p := &Pool{
		jobs:     make(chan Job, 4*workers),
		inflight: make(map[prKey]string),
		reviewFn: reviewFn,
		log:      log,
	}
	p.wg.Add(workers)
	for range workers {
		go p.worker()
	}
	return p
}

// Submit enqueues a job and classifies non-queued outcomes for durable callers.
func (p *Pool) Submit(j Job) SubmitResult {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return SubmitClosed
	}
	if head, ok := p.inflight[j.Key]; ok {
		p.mu.Unlock()
		p.log.Info("review coalesced: PR already in flight", "repo", j.Key.Owner+"/"+j.Key.Repo, "number", j.Key.Number)
		if head != "" && j.HeadSHA != "" && head == j.HeadSHA {
			return SubmitDuplicate
		}
		return SubmitCoalesced
	}
	select {
	case p.jobs <- j:
		p.inflight[j.Key] = j.HeadSHA
		p.mu.Unlock()
		return SubmitQueued
	default:
		p.mu.Unlock()
		p.drops.Add(1)
		p.log.Warn("review dropped: queue full", "repo", j.Key.Owner+"/"+j.Key.Repo, "number", j.Key.Number, "drops", p.drops.Load())
		return SubmitFull
	}
}

// Drops reports the cumulative count of jobs dropped on a full queue.
func (p *Pool) Drops() int64 { return p.drops.Load() }

func (p *Pool) worker() {
	defer p.wg.Done()
	for j := range p.jobs {
		p.run(j)
	}
}

func (p *Pool) run(j Job) {
	var reviewErr error
	defer func() {
		// Order is load-bearing: clear inflight BEFORE OnDone so a future OnDone
		// that re-Submits the same key isn't coalesced away. Today OnDone only
		// records cursor state, so the ordering is latent but intentional.
		p.mu.Lock()
		delete(p.inflight, j.Key)
		p.mu.Unlock()
		if r := recover(); r != nil {
			reviewErr = fmt.Errorf("review panicked: %v", r)
			p.log.Error("review panicked",
				"repo", j.Key.Owner+"/"+j.Key.Repo, "number", j.Key.Number,
				"error", config.RedactString(fmt.Sprintf("%v", r)),
				"stack", config.RedactString(string(debug.Stack())))
		}
		if j.OnDone != nil {
			j.OnDone(reviewErr)
		}
	}()
	reviewErr = p.reviewFn(j)
}

// Drain stops accepting work and blocks until in-flight jobs finish. It sets
// closed and closes the channel under p.mu so a concurrent Submit can never send
// on the closed channel; wg.Wait runs after releasing the lock because workers
// re-acquire p.mu in run's cleanup. Idempotent: a second Drain is a no-op.
func (p *Pool) Drain() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	close(p.jobs)
	p.mu.Unlock()
	p.wg.Wait()
}
