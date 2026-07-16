package cli

import (
	stdctx "context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/serve"
)

// anchor_recovery must survive the host review chain: a per-repo override wins,
// an omitted override inherits the outer layer, and the resolved tri-state lands
// on JobReviewOptions (which feeds the PR review request → engine).
func TestHostReviewAnchorRecoveryFlows(t *testing.T) {
	on, off := true, false
	base := config.HostReview{AnchorRecovery: &off}

	if got := mergeHostReview(base, config.HostReview{AnchorRecovery: &on}).AnchorRecovery; got == nil || !*got {
		t.Fatalf("per-repo anchor_recovery override must win, got %v", got)
	}
	if got := mergeHostReview(base, config.HostReview{}).AnchorRecovery; got == nil || *got {
		t.Fatalf("anchor_recovery must inherit base when the override omits it, got %v", got)
	}
	if got := mergeHostReview(config.HostReview{}, config.HostReview{}).AnchorRecovery; got != nil {
		t.Fatalf("anchor_recovery must stay nil (inherit machine config) when unset, got %v", got)
	}

	opts, err := hostReviewOptions("zai", config.HostProvider{Kind: config.KindAnthropic}, "sk-test", config.HostReview{AnchorRecovery: &off})
	if err != nil {
		t.Fatalf("hostReviewOptions: %v", err)
	}
	if opts.AnchorRecovery == nil || *opts.AnchorRecovery {
		t.Fatalf("JobReviewOptions.AnchorRecovery = %v, want explicit false", opts.AnchorRecovery)
	}
}

type capturePRReviewer struct {
	mu  sync.Mutex
	req PRReviewRequest
}

func (c *capturePRReviewer) ReviewPR(_ stdctx.Context, req PRReviewRequest) (ReviewOutcome, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.req = req
	return ReviewOutcome{}, nil
}

func (c *capturePRReviewer) GateFailed([]ReviewFinding, string) bool { return false }

func TestBuildServeReviewFnForwardsAnchorRecovery(t *testing.T) {
	prev := prReviewer
	rec := &capturePRReviewer{}
	SetPRReviewer(rec)
	t.Cleanup(func() { SetPRReviewer(prev) })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fn := buildServeReviewFn(log, "high", nil, nil, false)

	off := false
	job := serve.Job{Ref: "octocat/hello#7", Timeout: time.Second, Review: &serve.JobReviewOptions{Gate: "high", AnchorRecovery: &off}}
	if err := fn(job); err != nil {
		t.Fatalf("reviewFn: %v", err)
	}
	rec.mu.Lock()
	if rec.req.AnchorRecovery == nil || *rec.req.AnchorRecovery {
		t.Fatalf("PRReviewRequest.AnchorRecovery = %v, want explicit false", rec.req.AnchorRecovery)
	}
	rec.mu.Unlock()

	job.Review = &serve.JobReviewOptions{Gate: "high"}
	if err := fn(job); err != nil {
		t.Fatalf("reviewFn (nil override): %v", err)
	}
	rec.mu.Lock()
	if rec.req.AnchorRecovery != nil {
		t.Fatalf("PRReviewRequest.AnchorRecovery = %v, want nil (inherit)", rec.req.AnchorRecovery)
	}
	rec.mu.Unlock()
}
