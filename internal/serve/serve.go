package serve

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/vanducng/miu-cr/internal/config"
)

// Run starts the HTTP server and blocks until ctx is cancelled or the server
// errors. On cancel it gracefully shuts down (30s budget) then drains the pool so
// in-flight reviews finish. pool may be nil when the Server was built with an
// injected dispatcher (tests). Short HTTP timeouts protect the listener; the
// per-review timeout (Job.Timeout) is independent and generous.
func (s *Server) Run(ctx context.Context, pool *Pool) error {
	srv := &http.Server{
		Addr:         s.addr,
		Handler:      s.handler(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		s.log.Info("serve: listening", "addr", s.addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		s.log.Info("serve: shutdown signal received, draining")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			s.log.Error("serve: graceful shutdown failed", "error", config.RedactString(err.Error()))
		}
		if pool != nil {
			pool.Drain()
		}
		s.log.Info("serve: drained, exiting")
		return nil
	case err := <-errCh:
		// Server failed to start (e.g. address in use). Drain the pool so its
		// worker goroutines exit instead of leaking, then surface the error.
		if pool != nil {
			pool.Drain()
		}
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
