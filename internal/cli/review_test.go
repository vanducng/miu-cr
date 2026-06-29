package cli

import (
	"bytes"
	stdctx "context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

type fakeReviewer struct {
	outcome ReviewOutcome
	err     error
	gotCtx  stdctx.Context
	gotReq  ReviewRequest
}

func (f *fakeReviewer) Review(ctx stdctx.Context, req ReviewRequest) (ReviewOutcome, error) {
	f.gotCtx = ctx
	f.gotReq = req
	if f.err != nil {
		return ReviewOutcome{}, f.err
	}
	return f.outcome, nil
}

func (f *fakeReviewer) GateFailed(findings []ReviewFinding, gate string) bool {
	if gate == "" || gate == "none" {
		return false
	}
	rank := map[string]int{"info": 1, "low": 2, "medium": 3, "high": 4, "critical": 5}
	max := 0
	for _, fn := range findings {
		if r := rank[fn.Severity]; r > max {
			max = r
		}
	}
	return max >= rank[gate]
}

type blockingReviewer struct{}

func (blockingReviewer) Review(ctx stdctx.Context, req ReviewRequest) (ReviewOutcome, error) {
	<-ctx.Done()
	return ReviewOutcome{}, ctx.Err()
}

func (blockingReviewer) GateFailed([]ReviewFinding, string) bool { return false }

func runReview(t *testing.T, r Reviewer, args ...string) (string, error) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())                       // hermetic config dir: no real ~/.config/miu/cr leaks in
	t.Setenv("ANTHROPIC_API_KEY", "synthetic-test-key") // satisfy the soft first-run gate
	prev := reviewer
	SetReviewer(r)
	t.Cleanup(func() { SetReviewer(prev) })
	prevFmt := outputFormat
	t.Cleanup(func() { outputFormat = prevFmt })
	prettyOutput = false
	outputFormat = "json"

	opts := &options{output: "json"}
	cmd := reviewCommand(opts)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	return buf.String(), err
}

func TestReviewGateExitCode(t *testing.T) {
	r := &fakeReviewer{outcome: ReviewOutcome{Findings: []ReviewFinding{{File: "a.go", Line: 1, Severity: "high"}}}}
	out, err := runReview(t, r, "--staged", "--gate", "high")
	if err == nil {
		t.Fatal("want gated error, got nil")
	}
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "review.gate_failed" || ce.Exit != 2 {
		t.Fatalf("want review.gate_failed exit 2, got %+v", err)
	}
	if !ce.AlreadyWritten {
		t.Error("gate error must set AlreadyWritten")
	}
	if !strings.Contains(out, `"ok":true`) {
		t.Errorf("envelope still emitted before gate error, out=%s", out)
	}

	// Below gate → exit 0.
	out2, err2 := runReview(t, r, "--staged", "--gate", "critical")
	if err2 != nil {
		t.Fatalf("below-gate must not error, got %v", err2)
	}
	if !strings.Contains(out2, `"ok":true`) {
		t.Errorf("want success envelope, got %s", out2)
	}
}

func TestReviewClassifiesTimeout(t *testing.T) {
	// Mimic the engine pass-through: a DeadlineExceeded wrapped with %w must still
	// be reachable via errors.Is at the RunE boundary (the %w chain survives).
	r := &fakeReviewer{err: fmt.Errorf("agent: messages.new: %w", stdctx.DeadlineExceeded)}
	_, err := runReview(t, r, "--staged", "--gate", "high")
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "review.timeout" {
		t.Fatalf("want review.timeout, got %+v", err)
	}
	if !ce.Retry || ce.Hint == "" {
		t.Fatalf("review.timeout must be retryable with a hint, got %+v", ce)
	}
	// Envelope honesty: retryable surfaces as true.
	var buf bytes.Buffer
	_ = writeError(&buf, "review", ce)
	var env Envelope
	if e := json.Unmarshal(buf.Bytes(), &env); e != nil {
		t.Fatalf("invalid envelope: %v\n%s", e, buf.String())
	}
	if env.Error == nil || !env.Error.Retryable || env.Error.Code != "review.timeout" {
		t.Fatalf("envelope did not reflect a retryable review.timeout: %+v", env.Error)
	}
}

func TestReviewClassifiesStalledProgress(t *testing.T) {
	writeUserConfig(t, `[review]
stalled_timeout = "20ms"
`)
	_, err := runReviewKeepHome(t, blockingReviewer{}, "--staged", "--gate", "high")
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "review.stalled" {
		t.Fatalf("want review.stalled, got %+v", err)
	}
	if !ce.Retry || !strings.Contains(ce.Hint, "stalled_timeout") {
		t.Fatalf("review.stalled must be retryable with stalled_timeout hint, got %+v", ce)
	}
}

func TestReviewClassifiesCanceled(t *testing.T) {
	r := &fakeReviewer{err: fmt.Errorf("agent: chat.completions: %w", stdctx.Canceled)}
	_, err := runReview(t, r, "--staged", "--gate", "high")
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "review.canceled" {
		t.Fatalf("want review.canceled, got %+v", err)
	}
	if ce.Exit != 130 {
		t.Fatalf("review.canceled exit = %d, want 130", ce.Exit)
	}
}

// An unrecognized error stays internal.error (the bare-%w default), proving we
// don't over-classify.
func TestReviewUnknownErrorStaysInternal(t *testing.T) {
	r := &fakeReviewer{err: fmt.Errorf("agent: model produced no parseable findings")}
	_, err := runReview(t, r, "--staged", "--gate", "high")
	var ce *CLIError
	if asCLIError(err, &ce) {
		t.Fatalf("unknown error wrongly typed: %+v", ce)
	}
	var buf bytes.Buffer
	_ = writeError(&buf, "review", err)
	if !strings.Contains(buf.String(), `"internal.error"`) {
		t.Fatalf("want internal.error envelope, got %s", buf.String())
	}
}

func TestReviewSurfacesReviewID(t *testing.T) {
	r := &fakeReviewer{outcome: ReviewOutcome{Findings: []ReviewFinding{}, ReviewID: "rec-123"}}
	out, err := runReview(t, r, "--staged", "--gate", "high")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	var env Envelope
	if e := json.Unmarshal([]byte(out), &env); e != nil {
		t.Fatalf("invalid envelope: %v\n%s", e, out)
	}
	data, _ := env.Data.(map[string]any)
	if data["review_id"] != "rec-123" {
		t.Errorf("want review_id rec-123, got %v", data["review_id"])
	}
	if r.gotReq.NoSave {
		t.Error("default run must not set NoSave")
	}
}

func TestReviewNoSaveFlag(t *testing.T) {
	r := &fakeReviewer{outcome: ReviewOutcome{Findings: []ReviewFinding{}}}
	out, err := runReview(t, r, "--staged", "--gate", "high", "--no-save")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if !r.gotReq.NoSave {
		t.Error("--no-save must set NoSave on the request")
	}
	var env Envelope
	if e := json.Unmarshal([]byte(out), &env); e != nil {
		t.Fatalf("invalid envelope: %v\n%s", e, out)
	}
	data, _ := env.Data.(map[string]any)
	if data["review_id"] != "" {
		t.Errorf("--no-save: review_id must be empty, got %v", data["review_id"])
	}
}

func TestReviewSingleEmit(t *testing.T) {
	r := &fakeReviewer{outcome: ReviewOutcome{Findings: []ReviewFinding{{File: "a.go", Line: 2, Severity: "critical"}}}}
	out, _ := runReview(t, r, "--staged", "--gate", "high")
	if n := strings.Count(out, `"api_version"`); n != 1 {
		t.Fatalf("envelope must be emitted exactly once, got %d", n)
	}
}

func TestReviewFlagValidation(t *testing.T) {
	r := &fakeReviewer{}
	cases := [][]string{
		{"--from", "main"},                 // half-range
		{"--staged", "--commit", "abc123"}, // two modes
		{"--commit", "abc", "--from", "a", "--to", "b"},
		{},                             // no mode
		{"--staged", "--gate", "hgih"}, // typo gate
		{"--staged", "--gate", "High"}, // wrong case
		{"--staged", "--gate", "sev"},  // not a severity
	}
	for _, args := range cases {
		if _, err := runReview(t, r, args...); err == nil {
			t.Errorf("args %v: want validation error, got nil", args)
		}
	}
}

func TestReviewBadGateCode(t *testing.T) {
	r := &fakeReviewer{}
	_, err := runReview(t, r, "--staged", "--gate", "hgih")
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "review.bad_gate" || ce.Exit != 2 {
		t.Fatalf("want review.bad_gate exit 2, got %+v", err)
	}
}

func TestReviewEmptyStaged(t *testing.T) {
	r := &fakeReviewer{outcome: ReviewOutcome{Findings: []ReviewFinding{}, Stats: map[string]any{"findings_total": 0}}}
	out, err := runReview(t, r, "--staged", "--gate", "high")
	if err != nil {
		t.Fatalf("empty staged must not error, got %v", err)
	}
	var env Envelope
	if e := json.Unmarshal([]byte(out), &env); e != nil {
		t.Fatalf("invalid envelope: %v\n%s", e, out)
	}
	if !env.OK {
		t.Error("want ok=true")
	}
	data, _ := env.Data.(map[string]any)
	findings, _ := data["findings"].([]any)
	if len(findings) != 0 {
		t.Errorf("want findings=[], got %v", findings)
	}
}

// The --timeout flag must bound the whole operation, not just the agent pass:
// the context handed to Review carries a deadline so git subprocesses share it.
func TestReviewAppliesTimeoutToContext(t *testing.T) {
	r := &fakeReviewer{outcome: ReviewOutcome{Findings: []ReviewFinding{}}}
	t.Setenv("ANTHROPIC_API_KEY", "synthetic-test-key")
	prev := reviewer
	SetReviewer(r)
	t.Cleanup(func() { SetReviewer(prev) })
	prettyOutput = false

	opts := &options{output: "json", timeout: 30 * time.Second}
	cmd := reviewCommand(opts)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--staged", "--gate", "high"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	if err := cmd.Execute(); err != nil {
		t.Fatalf("review: %v", err)
	}
	if r.gotCtx == nil {
		t.Fatal("Review received nil context")
	}
	if _, ok := r.gotCtx.Deadline(); !ok {
		t.Error("context passed to Review must carry the --timeout deadline")
	}
}

func TestReviewDeepContextDefaults(t *testing.T) {
	r := &fakeReviewer{outcome: ReviewOutcome{Findings: []ReviewFinding{}}}
	if _, err := runReview(t, r, "--staged", "--deep-context"); err != nil {
		t.Fatalf("review: %v", err)
	}
	if r.gotReq.ExpandWindow != 20 || r.gotReq.TokenBudget != 0 || r.gotReq.Timeout != defaultReviewTimeout || !r.gotReq.DeepContext || !r.gotReq.ContextHopsAuto || r.gotReq.ContextHops != 0 {
		t.Fatalf("deep defaults = expand %d budget %d timeout %s deep %v auto %v hops %d", r.gotReq.ExpandWindow, r.gotReq.TokenBudget, r.gotReq.Timeout, r.gotReq.DeepContext, r.gotReq.ContextHopsAuto, r.gotReq.ContextHops)
	}

	r = &fakeReviewer{outcome: ReviewOutcome{Findings: []ReviewFinding{}}}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "synthetic-test-key")
	prev := reviewer
	SetReviewer(r)
	t.Cleanup(func() { SetReviewer(prev) })
	prettyOutput = false
	outputFormat = "json"

	opts := &options{output: "json", timeout: 30 * time.Second}
	cmd := rootCommand(opts)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--timeout", "42s", "review", "--staged", "--deep-context", "--expand", "7", "--token-budget", "123", "--context-hops", "4"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	if err := cmd.Execute(); err != nil {
		t.Fatalf("review: %v", err)
	}
	if r.gotReq.ExpandWindow != 7 || r.gotReq.TokenBudget != 123 || r.gotReq.Timeout != 42*time.Second || r.gotReq.ContextHopsAuto || r.gotReq.ContextHops != 4 {
		t.Fatalf("explicit flags = expand %d budget %d timeout %s auto %v hops %d", r.gotReq.ExpandWindow, r.gotReq.TokenBudget, r.gotReq.Timeout, r.gotReq.ContextHopsAuto, r.gotReq.ContextHops)
	}
}

func TestReviewCapabilityFirstDefaults(t *testing.T) {
	r := &fakeReviewer{outcome: ReviewOutcome{Findings: []ReviewFinding{}}}
	if _, err := runReview(t, r, "--staged"); err != nil {
		t.Fatalf("review: %v", err)
	}
	if r.gotReq.TokenBudget != 0 || r.gotReq.Timeout != defaultReviewTimeout {
		t.Fatalf("defaults = budget %d timeout %s, want no budget cap and %s", r.gotReq.TokenBudget, r.gotReq.Timeout, defaultReviewTimeout)
	}
}

func asCLIError(err error, target **CLIError) bool {
	for err != nil {
		if ce, ok := err.(*CLIError); ok {
			*target = ce
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
