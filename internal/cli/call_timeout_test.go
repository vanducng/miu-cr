package cli

import (
	stdctx "context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine/agent"
)

func timeoutErr(limit agent.TimeoutLimit, budget time.Duration) error {
	cte := &agent.CallTimeoutError{Limit: limit, Budget: budget, Elapsed: 601 * time.Second, Phase: "waiting for response headers", Label: "anthropic.messages"}
	// The agent always keeps the SDK's deadline error as the cause.
	return fmt.Errorf("agent: %w", fmt.Errorf("%w: %w", cte, stdctx.DeadlineExceeded))
}

func TestClassifyReviewErrNamesTheLimitThatFired(t *testing.T) {
	// A bare deadline can only be the job deadline: keep the CLI-owned message.
	bare := classifyReviewErr(fmt.Errorf("agent: %w", stdctx.DeadlineExceeded), 30*time.Minute)
	assertTimeout(t, bare, "review timed out after 30m0s", "--timeout")

	job := classifyReviewErr(timeoutErr(agent.LimitJobDeadline, 30*time.Minute), 30*time.Minute)
	assertTimeout(t, job, "cut by the job deadline", "--timeout")
	if !strings.Contains(job.Error(), "review timed out after 30m0s") {
		t.Fatalf("job deadline must keep the CLI-owned budget: %v", job)
	}

	call := classifyReviewErr(timeoutErr(agent.LimitCallTimeout, 10*time.Minute), 30*time.Minute)
	assertTimeout(t, call, "call timeout (10m0s)", "call_timeout")
	if !strings.Contains(call.Error(), "waiting for response headers") {
		t.Fatalf("call timeout must say which phase it was in: %v", call)
	}

	nested := classifyReviewErr(timeoutErr(agent.LimitNested, 10*time.Minute), 30*time.Minute)
	assertTimeout(t, nested, "nested deadline of unknown origin", "provider_call_timeout")
	if strings.Contains(nested.Error(), "timed out after 10m") {
		t.Fatalf("an unidentified deadline must not be reported as a 10m timeout: %v", nested)
	}
}

func assertTimeout(t *testing.T, err error, wantMsg, wantHint string) {
	t.Helper()
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "review.timeout" || !ce.Retry {
		t.Fatalf("want retryable review.timeout, got %+v", err)
	}
	if !strings.Contains(ce.Message, wantMsg) {
		t.Fatalf("message %q must contain %q", ce.Message, wantMsg)
	}
	if !strings.Contains(ce.Hint, wantHint) {
		t.Fatalf("hint %q must contain %q", ce.Hint, wantHint)
	}
	if !strings.Contains(ce.Hint, "requeues an open PR head while it is still current") || strings.Contains(ce.Hint, "guaranteed") {
		t.Fatalf("hint must state the host requeue condition, not promise a retry: %q", ce.Hint)
	}
}

func TestHostCallTimeoutReachesJobOptionsAndPolicyHash(t *testing.T) {
	base := config.HostReview{CallTimeout: "12m"}
	merged := mergeHostReview(base, config.HostReview{CallTimeout: "25m"})
	if merged.CallTimeout != "25m" {
		t.Fatalf("per-repo call_timeout must win, got %q", merged.CallTimeout)
	}
	if got := mergeHostReview(base, config.HostReview{}).CallTimeout; got != "12m" {
		t.Fatalf("an unset per-repo call_timeout must inherit the host value, got %q", got)
	}
	opts, err := hostReviewOptions("p", config.HostProvider{}, "", merged)
	if err != nil {
		t.Fatalf("hostReviewOptions: %v", err)
	}
	if opts.CallTimeout != "25m" {
		t.Fatalf("job options must carry call_timeout, got %q", opts.CallTimeout)
	}
	if fmt.Sprint(hostReviewAnalysisShape(base)) == fmt.Sprint(hostReviewAnalysisShape(merged)) {
		t.Fatal("changing call_timeout must change the policy shape so failed heads are retried under the new limit")
	}
}
