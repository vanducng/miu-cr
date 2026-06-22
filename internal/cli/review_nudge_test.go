package cli

import (
	"bytes"
	"strings"
	"testing"
)

// runReviewBare drives review from a true unconfigured baseline (temp HOME, all
// LLM-credential env vars cleared) so the soft first-run gate can be exercised.
// envKey, when non-empty, is set as ANTHROPIC_API_KEY after the clear to model
// "a key IS present" without runReview's blanket injection.
func runReviewBare(t *testing.T, r Reviewer, envKey string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", envKey)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("OPENAI_API_KEY", "")
	prev := reviewer
	SetReviewer(r)
	t.Cleanup(func() { SetReviewer(prev) })
	prettyOutput = false

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

func TestReviewNudgeWhenUnconfigured(t *testing.T) {
	r := &fakeReviewer{outcome: ReviewOutcome{Findings: []ReviewFinding{}}}
	_, err := runReviewBare(t, r, "", "--staged")
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "provider.unconfigured" {
		t.Fatalf("want provider.unconfigured nudge, got %+v", err)
	}
	if !strings.Contains(ce.Hint, "miucr init") {
		t.Errorf("nudge hint must mention `miucr init`, got %q", ce.Hint)
	}
}

func TestReviewNoNudgeWithEnvKey(t *testing.T) {
	r := &fakeReviewer{outcome: ReviewOutcome{Findings: []ReviewFinding{}}}
	out, err := runReviewBare(t, r, "synthetic-test-key", "--staged")
	if err != nil {
		t.Fatalf("key present must proceed (no nudge), got %v", err)
	}
	if !strings.Contains(out, `"ok":true`) {
		t.Errorf("want success envelope, got %s", out)
	}
}

func TestReviewNoNudgeWithFlag(t *testing.T) {
	r := &fakeReviewer{outcome: ReviewOutcome{Findings: []ReviewFinding{}}}
	out, err := runReviewBare(t, r, "", "--staged", "--api-key", "synthetic-test-key")
	if err != nil {
		t.Fatalf("--api-key present must proceed (no nudge), got %v", err)
	}
	if !strings.Contains(out, `"ok":true`) {
		t.Errorf("want success envelope, got %s", out)
	}
}
