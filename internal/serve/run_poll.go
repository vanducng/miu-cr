package serve

import (
	stdctx "context"
)

// RunPoll drives a poll-only daemon: it runs poller.Run(ctx) and, on ctx.Done,
// Drains the pool exactly once. Poll-only mode has no http.Server (so no
// secret/--addr is required), so RunPoll is the sole Drain owner. In webhook+poll
// mode Server.Run is the sole Drainer and poller.Run must NOT Drain — so this
// path is never used there and there is no double-drain.
func RunPoll(ctx stdctx.Context, pool *Pool, poller *Poller) error {
	done := make(chan struct{})
	go func() {
		poller.Run(ctx)
		close(done)
	}()
	<-ctx.Done()
	<-done
	if pool != nil {
		pool.Drain()
	}
	return nil
}
