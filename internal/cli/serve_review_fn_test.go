package cli

import (
	stdctx "context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/vanducng/miu-cr/internal/serve"
	"github.com/vanducng/miu-cr/internal/store"
)

// ctxRecordingStore records the last persisted status and whether the ctx handed
// to UpsertReview was already canceled, the signal that the write rode the
// (timed-out) jobCtx instead of a fresh one.
type ctxRecordingStore struct {
	mu             sync.Mutex
	lastStatus     string
	upserts        int
	sawCanceledCtx bool
}

func (s *ctxRecordingStore) UpsertReview(ctx stdctx.Context, rec store.ReviewRecord) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upserts++
	s.lastStatus = rec.Status
	if ctx.Err() != nil {
		s.sawCanceledCtx = true
		return "", ctx.Err()
	}
	return rec.ID, nil
}

func (s *ctxRecordingStore) GetReview(stdctx.Context, string) (store.ReviewRecord, error) {
	return store.ReviewRecord{}, nil
}

// timeoutPRReviewer simulates the common failure: it blocks until the job ctx
// times out, then returns that ctx error (exactly how the engine surfaces a
// deadline-exceeded review).
type timeoutPRReviewer struct{}

func (timeoutPRReviewer) ReviewPR(ctx stdctx.Context, _ PRReviewRequest) (ReviewOutcome, error) {
	<-ctx.Done()
	return ReviewOutcome{}, ctx.Err()
}

func (timeoutPRReviewer) GateFailed([]ReviewFinding, string) bool { return false }

// TestBuildServeReviewFn_TimedOutStillPersistsFailed proves a review that fails by
// timeout still records status=failed, and does so on a FRESH context, not the
// already-canceled jobCtx (which would strand the record at pending forever).
func TestBuildServeReviewFn_TimedOutStillPersistsFailed(t *testing.T) {
	prev := prReviewer
	SetPRReviewer(timeoutPRReviewer{})
	t.Cleanup(func() { SetPRReviewer(prev) })

	st := &ctxRecordingStore{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fn := buildServeReviewFn(log, "high", st, nil, false)

	err := fn(serve.Job{Ref: "octocat/hello#7", ReviewID: "rev-timeout", Timeout: 20 * time.Millisecond})
	if err == nil {
		t.Fatal("expected a review error on timeout")
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	if st.upserts != 1 {
		t.Fatalf("want exactly 1 terminal upsert, got %d", st.upserts)
	}
	if st.lastStatus != "failed" {
		t.Fatalf("persisted status = %q, want failed", st.lastStatus)
	}
	if st.sawCanceledCtx {
		t.Fatal("terminal persist rode a canceled ctx — must use a fresh bounded ctx")
	}
}
