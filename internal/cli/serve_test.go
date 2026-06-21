package cli

import (
	"bytes"
	"testing"
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
