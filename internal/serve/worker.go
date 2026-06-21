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
	inflight map[prKey]bool
	wg       sync.WaitGroup
	reviewFn func(Job)
	log      *slog.Logger
	drops    atomic.Int64
}

// NewPool starts workers=max(2,NumCPU) goroutines draining a buffered channel of
// size 4*workers. reviewFn runs each job (the real one calls cli.ReviewPRForServe).
func NewPool(reviewFn func(Job), log *slog.Logger) *Pool {
	if log == nil {
		log = slog.Default()
	}
	workers := runtime.NumCPU()
	if workers < 2 {
		workers = 2
	}
	p := &Pool{
		jobs:     make(chan Job, 4*workers),
		inflight: make(map[prKey]bool),
		reviewFn: reviewFn,
		log:      log,
	}
	p.wg.Add(workers)
	for range workers {
		go p.worker()
	}
	return p
}

// Submit enqueues a job. It returns false (without enqueuing) when the same PR is
// already in flight (coalesced) or when the queue is full (loud-logged + counted).
func (p *Pool) Submit(j Job) bool {
	p.mu.Lock()
	if p.inflight[j.Key] {
		p.mu.Unlock()
		p.log.Info("review coalesced: PR already in flight", "repo", j.Key.Owner+"/"+j.Key.Repo, "number", j.Key.Number)
		return false
	}
	select {
	case p.jobs <- j:
		p.inflight[j.Key] = true
		p.mu.Unlock()
		return true
	default:
		p.mu.Unlock()
		p.drops.Add(1)
		p.log.Warn("review dropped: queue full", "repo", j.Key.Owner+"/"+j.Key.Repo, "number", j.Key.Number, "drops", p.drops.Load())
		return false
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
	defer func() {
		p.mu.Lock()
		delete(p.inflight, j.Key)
		p.mu.Unlock()
		if r := recover(); r != nil {
			p.log.Error("review panicked",
				"repo", j.Key.Owner+"/"+j.Key.Repo, "number", j.Key.Number,
				"error", config.RedactString(fmt.Sprintf("%v", r)),
				"stack", config.RedactString(string(debug.Stack())))
		}
	}()
	p.reviewFn(j)
}

// Drain stops accepting work and blocks until in-flight jobs finish.
func (p *Pool) Drain() {
	close(p.jobs)
	p.wg.Wait()
}
