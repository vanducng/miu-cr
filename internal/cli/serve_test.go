package cli

import (
	"bytes"
	stdctx "context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vanducng/miu-cr/internal/config"
)

func runServe(t *testing.T, args ...string) error {
	t.Helper()
	_, err := runServeOut(t, args...)
	return err
}

func runServeOut(t *testing.T, args ...string) (string, error) {
	t.Helper()
	opts := &options{output: "json"}
	cmd := serveCommand(opts)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	return buf.String(), err
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

func TestServeHostDryRunConfig(t *testing.T) {
	path := writeServeHostConfig(t, `version: 1
store:
  backend: postgres
  dsn: postgres://user:secret@localhost:5432/db
github:
  default_account: pat
  accounts:
    pat:
      mode: pat
      auth_env: GITHUB_TOKEN
host:
  poll_source: pulls
repos:
  - name: service-api
    slug: example-org/service-api
    git_url: https://github.com/example-org/service-api.git
`)
	t.Setenv("MIUCR_CONFIG", path)
	t.Setenv("WEBHOOK_SECRET", "")
	t.Setenv("GITHUB_TOKEN", "")

	out, err := runServeOut(t, "--host", "--dry-run-config")
	if err != nil {
		t.Fatalf("dry-run config: %v\n%s", err, out)
	}
	if strings.Contains(out, "secret@") {
		t.Fatalf("dsn secret leaked: %s", out)
	}
	var env Envelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, out)
	}
	if !env.OK || env.Kind != "serve.host_config" {
		t.Fatalf("envelope = %+v", env)
	}
	if env.Summary["repos"].(float64) != 1 || env.Summary["accounts"].(float64) != 1 {
		t.Fatalf("summary = %+v", env.Summary)
	}
}

func TestServeHostDryRunRejectsUnsupportedPollSourceOverride(t *testing.T) {
	path := writeServeHostConfig(t, `version: 1
store:
  backend: postgres
github:
  default_account: pat
  accounts:
    pat:
      mode: pat
      auth_env: GITHUB_TOKEN
host:
  poll_source: pulls
repos:
  - name: service-api
    slug: example-org/service-api
    git_url: https://github.com/example-org/service-api.git
`)
	t.Setenv("MIUCR_CONFIG", path)
	err := runServe(t, "--host", "--dry-run-config", "--poll-source", "notifications")
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "config.invalid" || ce.Exit != 2 {
		t.Fatalf("want config.invalid exit 2, got %+v", err)
	}
}

func TestServeDryRunConfigRequiresHost(t *testing.T) {
	err := runServe(t, "--dry-run-config")
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "serve.host_required" || ce.Exit != 2 {
		t.Fatalf("want serve.host_required exit 2, got %+v", err)
	}
}

func TestServeHostStoreUnwired(t *testing.T) {
	path := writeServeHostConfig(t, `version: 1
store:
  backend: postgres
github:
  default_account: pat
  accounts:
    pat:
      mode: pat
      auth_env: GITHUB_TOKEN
host:
  poll_source: pulls
repos:
  - name: service-api
    slug: example-org/service-api
    git_url: https://github.com/example-org/service-api.git
`)
	t.Setenv("MIUCR_CONFIG", path)
	err := runServe(t, "--host")
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "serve.host_store_unwired" || ce.Exit != 1 {
		t.Fatalf("want serve.host_store_unwired exit 1, got %+v", err)
	}
}

func TestServeHostRuleDirectory(t *testing.T) {
	base := t.TempDir()
	ruleDir := filepath.Join(base, "rules")
	if err := os.Mkdir(ruleDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ruleDir, "b.md"), []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ruleDir, "a.md"), []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ruleDir, "skip.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}

	prompt, promptHash, rulesHash, err := hostOperatorPrompt(base, config.HostAgent{SystemPrompt: "Base prompt."}, config.HostAgent{}, []string{"rules"})
	if err != nil {
		t.Fatalf("hostOperatorPrompt: %v", err)
	}
	if !strings.Contains(prompt, "Base prompt.") || !strings.Contains(prompt, "Trusted host rules:") {
		t.Fatalf("operator prompt missing sections: %q", prompt)
	}
	if strings.Index(prompt, "first") > strings.Index(prompt, "second") || strings.Contains(prompt, "ignored") {
		t.Fatalf("rules not sorted/filtered: %q", prompt)
	}
	if promptHash == "" || rulesHash == "" {
		t.Fatalf("hashes should be populated: prompt=%q rules=%q", promptHash, rulesHash)
	}
}

func TestRunHostCommandOutputReportsMissingBinary(t *testing.T) {
	_, err := runHostCommandOutput(stdctx.Background(), []string{"/definitely/missing/miucr-auth-command"})
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "agent.auth_command_failed" || !strings.Contains(ce.Message, "binary not found") {
		t.Fatalf("want binary not found auth_command error, got %+v", err)
	}
}

func TestSecretBufferRejectsOverflow(t *testing.T) {
	var b secretBuffer
	b.limit = 3
	n, err := b.Write([]byte("abcd"))
	if err == nil || n != 0 {
		t.Fatalf("want overflow error with n=0, got n=%d err=%v", n, err)
	}
	if b.String() != "" {
		t.Fatalf("overflow should not retain partial secret output, got %q", b.String())
	}
}

func TestBuildServeHostReposAppliesHostPollDefault(t *testing.T) {
	hostPoll := false
	cfg := config.HostConfig{
		Host: config.HostRuntime{Poll: &hostPoll},
		Repos: []config.HostRepo{{
			Name:          "service-api",
			Slug:          "example-org/service-api",
			Owner:         "example-org",
			Repo:          "service-api",
			GitURL:        "https://github.com/example-org/service-api.git",
			GithubAccount: "pat",
		}},
	}
	repos, _, err := buildServeHostRepos(stdctx.Background(), cfg, filepath.Join(t.TempDir(), "host.yaml"))
	if err != nil {
		t.Fatalf("buildServeHostRepos: %v", err)
	}
	if repos[0].Poll {
		t.Fatalf("repo poll should inherit host.poll=false: %+v", repos[0])
	}
}

func TestHostProviderDefaultFallbackDoesNotCopyAuthToken(t *testing.T) {
	provider, err := hostProvider(config.HostConfig{DefaultProvider: string(config.KindAnthropic)})
	if err != nil {
		t.Fatalf("hostProvider: %v", err)
	}
	if provider.AuthToken != "" {
		t.Fatalf("default fallback copied literal auth token")
	}
}

func writeServeHostConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "host.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
