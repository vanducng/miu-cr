package cli

import (
	"testing"

	"github.com/vanducng/miu-cr/internal/config"
)

// thinking must survive the host review chain: a per-repo override wins,
// an omitted override inherits the global default, and the resolved value
// lands on JobReviewOptions (which feeds the PR review request → creds).
func TestHostReviewThinkingFlows(t *testing.T) {
	base := config.HostReview{Thinking: "high"}

	if got := mergeHostReview(base, config.HostReview{Thinking: "medium"}).Thinking; got != "medium" {
		t.Fatalf("per-repo thinking override must win, got %q", got)
	}
	if got := mergeHostReview(base, config.HostReview{}).Thinking; got != "high" {
		t.Fatalf("thinking must inherit base when the override omits it, got %q", got)
	}

	opts, err := hostReviewOptions("zai", config.HostProvider{Kind: config.KindAnthropic}, "sk-test", config.HostReview{Thinking: "medium"})
	if err != nil {
		t.Fatalf("hostReviewOptions: %v", err)
	}
	if opts.Thinking != "medium" {
		t.Fatalf("JobReviewOptions.Thinking = %q, want medium", opts.Thinking)
	}
}
