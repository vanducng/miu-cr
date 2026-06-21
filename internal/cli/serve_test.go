package cli

import (
	"bytes"
	stdctx "context"
	"testing"
	"time"
)

func runServe(t *testing.T, args ...string) error {
	t.Helper()
	opts := &options{output: "json"}
	cmd := serveCommand(opts)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	return cmd.Execute()
}

// runServeCtx runs serveCommand under ctx so a long-running mode (poll/webhook)
// can be stopped via cancellation in tests.
func runServeCtx(t *testing.T, ctx stdctx.Context, args ...string) error {
	t.Helper()
	opts := &options{output: "json"}
	cmd := serveCommand(opts)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	return cmd.ExecuteContext(ctx)
}

func TestServe_MissingSecret(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "")
	t.Setenv("GITHUB_TOKEN", "ghp_token1234567890abcdefABCDEF12")
	err := runServe(t, "--repos", "octocat/hello")
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "serve.secret_required" || ce.Exit != 2 {
		t.Fatalf("want serve.secret_required exit 2, got %+v", err)
	}
}

func TestServe_MissingToken(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "shared-hmac-secret")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	err := runServe(t, "--repos", "octocat/hello")
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "serve.token_required" || ce.Exit != 2 {
		t.Fatalf("want serve.token_required exit 2, got %+v", err)
	}
}

func TestServe_MissingRepos(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "shared-hmac-secret")
	t.Setenv("GITHUB_TOKEN", "ghp_token1234567890abcdefABCDEF12")
	err := runServe(t)
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "serve.repos_required" || ce.Exit != 2 {
		t.Fatalf("want serve.repos_required exit 2, got %+v", err)
	}
}

// --poll without a secret is allowed (poll-only mode bypasses the secret
// requirement); the secret check must not fire.
func TestServe_PollOnlyNoSecretAllowed(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "")
	t.Setenv("GITHUB_TOKEN", "ghp_token1234567890abcdefABCDEF12")

	ctx, cancel := stdctx.WithCancel(stdctx.Background())
	cancel() // cancel before run so RunPoll returns immediately after first drain

	done := make(chan error, 1)
	go func() {
		done <- runServeCtx(t, ctx, "--poll", "--repos", "octocat/hello")
	}()
	select {
	case err := <-done:
		// Poll-only returns nil on ctx cancel; it must NOT be a secret_required error.
		var ce *CLIError
		if asCLIError(err, &ce) && ce.Code == "serve.secret_required" {
			t.Fatalf("poll-only must not require a secret, got %+v", err)
		}
		if err != nil {
			t.Fatalf("poll-only run returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("poll-only run did not stop on ctx cancel")
	}
}

// An invalid --poll-source is a typed config error.
func TestServe_PollSourceInvalid(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "")
	t.Setenv("GITHUB_TOKEN", "ghp_token1234567890abcdefABCDEF12")
	err := runServe(t, "--poll", "--poll-source", "bogus", "--repos", "octocat/hello")
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "serve.poll_source_invalid" || ce.Exit != 2 {
		t.Fatalf("want serve.poll_source_invalid exit 2, got %+v", err)
	}
}
