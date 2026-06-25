package cli

import (
	"bytes"
	"strings"
	"testing"
)

// newProgress: -q always silences; -v always emits "miu-cr: <msg>" to the given
// writer; with neither flag the default is driven by the TTY check. Under `go
// test` stderr is not a char device, so the no-flag default is silent — exactly
// the piped/CI behavior that keeps the stdout envelope and its parsers untouched.
func TestNewProgress(t *testing.T) {
	t.Run("quiet wins", func(t *testing.T) {
		if newProgress(&bytes.Buffer{}, true, true) != nil {
			t.Error("quiet must return a nil (silent) sink even with verbose")
		}
		if newProgress(&bytes.Buffer{}, false, true) != nil {
			t.Error("quiet must return a nil (silent) sink")
		}
	})

	t.Run("verbose emits", func(t *testing.T) {
		var buf bytes.Buffer
		p := newProgress(&buf, true, false)
		if p == nil {
			t.Fatal("verbose must return a non-nil sink")
		}
		p("reviewing 2 files (3 changed)…")
		if got := buf.String(); !strings.HasPrefix(got, "miu-cr: ") || !strings.Contains(got, "reviewing 2 files (3 changed)…\n") {
			t.Errorf("unexpected progress line: %q", got)
		}
	})

	t.Run("non-tty default is silent", func(t *testing.T) {
		if newProgress(&bytes.Buffer{}, false, false) != nil {
			t.Error("no flags + non-tty stderr must be silent (piped/CI safe)")
		}
	})
}

// With --verbose, progress (the final "done" milestone) must land on STDERR while
// stdout carries only the clean miucr.cli/v1 envelope — never a "miu-cr:" line.
func TestReviewVerboseProgressToStderrOnly(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "synthetic-test-key")
	r := &fakeReviewer{outcome: ReviewOutcome{Findings: []ReviewFinding{{File: "a.go", Line: 1, Severity: "low"}}}}
	prev := reviewer
	SetReviewer(r)
	t.Cleanup(func() { SetReviewer(prev) })
	prettyOutput = false

	opts := &options{output: "json"}
	cmd := reviewCommand(opts)
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetArgs([]string{"--staged", "--gate", "high", "--verbose"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	if err := cmd.Execute(); err != nil {
		t.Fatalf("review: %v", err)
	}

	if strings.Contains(out.String(), "miu-cr:") {
		t.Errorf("stdout must stay envelope-only, got progress leak: %s", out.String())
	}
	if !strings.Contains(out.String(), `"api_version"`) {
		t.Errorf("stdout must still carry the envelope: %s", out.String())
	}
	if !strings.Contains(errBuf.String(), "done: 1 findings") {
		t.Errorf("stderr must carry the done milestone, got: %q", errBuf.String())
	}
}
