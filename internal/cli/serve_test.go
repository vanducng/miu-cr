package cli

import (
	"bytes"
	stdctx "context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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

func TestServeHostReloaderReloadsPromptAndRules(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "prompts"), 0o700); err != nil {
		t.Fatal(err)
	}
	ruleDir := filepath.Join(base, "rules")
	if err := os.Mkdir(ruleDir, 0o700); err != nil {
		t.Fatal(err)
	}
	promptPath := filepath.Join(base, "prompts", "default.md")
	rulePath := filepath.Join(ruleDir, "project.md")
	if err := os.WriteFile(promptPath, []byte("prompt v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rulePath, []byte("rule v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_PROVIDER_TOKEN", "provider-secret")
	hostPath := filepath.Join(base, "host.yaml")
	hostYAML := func(mode string, agents string) string {
		return `
version: 1
default_provider: test
providers:
  test:
    kind: anthropic
    auth: api_key
    auth_env: TEST_PROVIDER_TOKEN
store:
  backend: postgres
github:
  default_account: pat
  accounts:
    pat:
      mode: pat
      auth_env: GITHUB_TOKEN
agent:
  system_prompt_file: prompts/default.md
host:
  poll_source: pulls
repos:
  - name: service-api
    slug: example-org/service-api
    git_url: https://github.com/example-org/service-api.git
    github_account: pat
    rules:
      - rules
    review:
      subagents:
        mode: ` + mode + `
        max_parallel: 3
        agents:
` + agents
	}
	agentsV1 := `          - name: airflow
            include: ["dags/**"]
          - name: dbt
            include: ["dbt/**"]
`
	if err := os.WriteFile(hostPath, []byte(hostYAML("always", agentsV1)), 0o600); err != nil {
		t.Fatal(err)
	}

	reload := buildServeHostReloader(hostPath, 0, false, "", false)
	first, err := reload(stdctx.Background())
	if err != nil {
		t.Fatalf("first reload: %v", err)
	}
	unchanged, err := reload(stdctx.Background())
	if err != nil {
		t.Fatalf("unchanged reload: %v", err)
	}
	if unchanged.Repos != nil || unchanged.TokenSources != nil {
		t.Fatalf("unchanged reload returned snapshot: %+v", unchanged)
	}
	if err := os.WriteFile(promptPath, []byte("prompt v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rulePath, []byte("rule v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	agentsV2 := `          - name: airflow
            include: ["dags/**", "plugins/**"]
          - name: dbt
            include: ["dbt/**", "models/**", "macros/**"]
          - name: tobytime
            include: ["services/sci/**"]
`
	if err := os.WriteFile(hostPath, []byte(hostYAML("auto", agentsV2)), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := reload(stdctx.Background())
	if err != nil {
		t.Fatalf("second reload: %v", err)
	}
	if !strings.Contains(first.Repos[0].Review.OperatorPrompt, "prompt v1") || !strings.Contains(first.Repos[0].Review.OperatorPrompt, "rule v1") {
		t.Fatalf("first operator prompt did not include v1 files: %q", first.Repos[0].Review.OperatorPrompt)
	}
	if !strings.Contains(second.Repos[0].Review.OperatorPrompt, "prompt v2") || !strings.Contains(second.Repos[0].Review.OperatorPrompt, "rule v2") {
		t.Fatalf("second operator prompt did not include v2 files: %q", second.Repos[0].Review.OperatorPrompt)
	}
	if first.Repos[0].PromptHash == second.Repos[0].PromptHash || first.Repos[0].RulesHash == second.Repos[0].RulesHash {
		t.Fatalf("prompt/rules hashes did not change: first=%+v second=%+v", first.Repos[0], second.Repos[0])
	}
	if first.Repos[0].Review.Subagents.Mode != "always" || len(first.Repos[0].Review.Subagents.Agents) != 2 {
		t.Fatalf("first subagents not loaded: %+v", first.Repos[0].Review.Subagents)
	}
	if second.Repos[0].Review.Subagents.Mode != "auto" || len(second.Repos[0].Review.Subagents.Agents) != 3 {
		t.Fatalf("second subagents not reloaded: %+v", second.Repos[0].Review.Subagents)
	}
	if second.Repos[0].Review.Subagents.Agents[2].Name != "tobytime" || second.Repos[0].Review.Subagents.Agents[2].Include[0] != "services/sci/**" {
		t.Fatalf("tobytime subagent not reloaded: %+v", second.Repos[0].Review.Subagents.Agents[2])
	}
}

func TestRunHostCommandOutputReportsMissingBinary(t *testing.T) {
	_, err := runHostCommandOutput(stdctx.Background(), []string{"/definitely/missing/miucr-auth-command"})
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "agent.auth_command_failed" || !strings.Contains(ce.Message, "binary not found") {
		t.Fatalf("want binary not found auth_command error, got %+v", err)
	}
}

func TestHostSecretFileReadsRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(" token-value \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := hostSecret(stdctx.Background(), "", path, nil)
	if err != nil {
		t.Fatalf("hostSecret: %v", err)
	}
	if got != "token-value" {
		t.Fatalf("secret = %q, want token-value", got)
	}
}

func TestHostSecretFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, err := hostSecret(stdctx.Background(), "", link, nil)
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "config.invalid" || !strings.Contains(ce.Message, "symlink") {
		t.Fatalf("want config.invalid symlink error, got %+v", err)
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

func TestBuildServeHostReposAllowsZeroReviewOverrides(t *testing.T) {
	one := 1
	twenty := 20
	zero := 0
	cfg := config.HostConfig{
		Review: config.HostReview{Expand: &twenty, ContextHops: &one},
		Host:   config.HostRuntime{Review: config.HostReview{Expand: &twenty, ContextHops: &one}},
		Repos: []config.HostRepo{{
			Name:          "service-api",
			Slug:          "example-org/service-api",
			Owner:         "example-org",
			Repo:          "service-api",
			GitURL:        "https://github.com/example-org/service-api.git",
			GithubAccount: "pat",
			Review:        config.HostReview{Expand: &zero, ContextHops: &zero},
		}},
	}
	repos, _, err := buildServeHostRepos(stdctx.Background(), cfg, filepath.Join(t.TempDir(), "host.yaml"))
	if err != nil {
		t.Fatalf("buildServeHostRepos: %v", err)
	}
	if repos[0].Review.ExpandWindow != 0 || repos[0].Review.ContextHops != 0 {
		t.Fatalf("zero overrides not applied: %+v", repos[0].Review)
	}
}

func TestBuildServeHostReposThreadResolutionSyncLayering(t *testing.T) {
	cfg := config.HostConfig{
		Host: config.HostRuntime{Review: config.HostReview{ThreadResolutionSync: config.ThreadResolutionSyncConfig{Mode: "off", Interval: "10m"}}},
		Repos: []config.HostRepo{{
			Name:          "service-api",
			Slug:          "example-org/service-api",
			Owner:         "example-org",
			Repo:          "service-api",
			GitURL:        "https://github.com/example-org/service-api.git",
			GithubAccount: "pat",
			Review:        config.HostReview{ThreadResolutionSync: config.ThreadResolutionSyncConfig{Mode: "poll", Interval: "2m"}},
		}},
	}
	repos, _, err := buildServeHostRepos(stdctx.Background(), cfg, filepath.Join(t.TempDir(), "host.yaml"))
	if err != nil {
		t.Fatalf("buildServeHostRepos: %v", err)
	}
	if repos[0].ThreadResolutionSync.Mode != "poll" || repos[0].ThreadResolutionSync.Interval != 2*time.Minute {
		t.Fatalf("repo override did not enable thread resolution sync: %+v", repos[0])
	}
}

func TestBuildServeHostReposRejectsInvalidThreadResolutionSync(t *testing.T) {
	cfg := config.HostConfig{
		Host: config.HostRuntime{Review: config.HostReview{ThreadResolutionSync: config.ThreadResolutionSyncConfig{Mode: "webhook"}}},
		Repos: []config.HostRepo{{
			Name:          "service-api",
			Slug:          "example-org/service-api",
			Owner:         "example-org",
			Repo:          "service-api",
			GitURL:        "https://github.com/example-org/service-api.git",
			GithubAccount: "pat",
		}},
	}
	if _, _, err := buildServeHostRepos(stdctx.Background(), cfg, filepath.Join(t.TempDir(), "host.yaml")); err == nil {
		t.Fatal("expected invalid thread resolution sync mode to fail")
	}
	cfg.Host.Review.ThreadResolutionSync = config.ThreadResolutionSyncConfig{Mode: "poll", Interval: "soon"}
	if _, _, err := buildServeHostRepos(stdctx.Background(), cfg, filepath.Join(t.TempDir(), "host.yaml")); err == nil {
		t.Fatal("expected invalid thread resolution sync interval to fail")
	}
}

func TestBuildServeHostReposMergesPRFilterRules(t *testing.T) {
	cfg := config.HostConfig{
		Review: config.HostReview{PRFilter: config.HostPRFilter{
			DefaultAction: "include",
			Rules: []config.HostPRFilterRule{{
				Action:      "exclude",
				AuthorTypes: []string{"Bot"},
			}},
		}},
		Repos: []config.HostRepo{{
			Name:          "service-api",
			Slug:          "example-org/service-api",
			Owner:         "example-org",
			Repo:          "service-api",
			GitURL:        "https://github.com/example-org/service-api.git",
			GithubAccount: "pat",
			Review: config.HostReview{PRFilter: config.HostPRFilter{
				DefaultAction: "exclude",
				Rules: []config.HostPRFilterRule{{
					Action:       "include",
					TitleRegexes: []string{`^chore\(deps\):`},
				}},
			}},
		}},
	}
	repos, _, err := buildServeHostRepos(stdctx.Background(), cfg, filepath.Join(t.TempDir(), "host.yaml"))
	if err != nil {
		t.Fatalf("buildServeHostRepos: %v", err)
	}
	rules := repos[0].PRFilter.Rules
	if len(rules) != 2 || rules[0].Action != "exclude" || rules[1].Action != "include" {
		t.Fatalf("merged filter rules = %+v", rules)
	}
	if repos[0].PRFilter.DefaultAction != "exclude" {
		t.Fatalf("merged default action = %q, want exclude", repos[0].PRFilter.DefaultAction)
	}
}

func TestBuildServeHostReposPolicyHashIgnoresPublishOnlyFields(t *testing.T) {
	baseReview := config.HostReview{
		Gate:        "high",
		FilterMode:  "diff_context",
		MinSeverity: "low",
		Format:      "full",
		Post:        boolSetting(true),
		Suggest:     boolSetting(false),
		Approval:    config.ApprovalPolicy{Mode: "off"},
		PRFilter: config.HostPRFilter{Rules: []config.HostPRFilterRule{{
			Action:       "exclude",
			TitleRegexes: []string{`^chore\(deps\):`},
		}}},
	}
	cfg := config.HostConfig{
		Review: baseReview,
		Repos: []config.HostRepo{{
			Name:          "service-api",
			Slug:          "example-org/service-api",
			Owner:         "example-org",
			Repo:          "service-api",
			GitURL:        "https://github.com/example-org/service-api.git",
			GithubAccount: "pat",
		}},
	}
	base, _, err := buildServeHostRepos(stdctx.Background(), cfg, filepath.Join(t.TempDir(), "host.yaml"))
	if err != nil {
		t.Fatalf("buildServeHostRepos base: %v", err)
	}
	cfg.Review.Format = "minimal"
	cfg.Review.Suggest = boolSetting(true)
	cfg.Review.Approval = config.ApprovalPolicy{Mode: "threshold", MaxPriority: "P3", Note: "on_findings"}
	cfg.Review.PRFilter.Rules = append(cfg.Review.PRFilter.Rules, config.HostPRFilterRule{Action: "exclude", TitleRegexes: []string{`^chore\(main\): release `}})
	got, _, err := buildServeHostRepos(stdctx.Background(), cfg, filepath.Join(t.TempDir(), "host.yaml"))
	if err != nil {
		t.Fatalf("buildServeHostRepos changed: %v", err)
	}
	if got[0].PolicyHash != base[0].PolicyHash {
		t.Fatalf("publish-only fields changed policy hash: base=%s got=%s", base[0].PolicyHash, got[0].PolicyHash)
	}
	cfg.Review.Gate = "critical"
	changed, _, err := buildServeHostRepos(stdctx.Background(), cfg, filepath.Join(t.TempDir(), "host.yaml"))
	if err != nil {
		t.Fatalf("buildServeHostRepos analysis changed: %v", err)
	}
	if changed[0].PolicyHash == base[0].PolicyHash {
		t.Fatal("analysis field change should change policy hash")
	}
}

func TestHostReviewAnalysisShapeClassifiesEveryField(t *testing.T) {
	hashed := map[string]bool{}
	shape := reflect.TypeOf(hostReviewAnalysisFields{})
	for i := 0; i < shape.NumField(); i++ {
		hashed[shape.Field(i).Name] = true
	}
	ignored := map[string]bool{
		"Format":               true,
		"CodeSummary":          true,
		"Post":                 true,
		"Suggest":              true,
		"Approval":             true,
		"PRFilter":             true,
		"ThreadResolutionSync": true,
		// Debounce only delays WHEN a review runs; it must NOT enter the policy hash,
		// or changing the window would rewrite every dedupe_key and re-review all PRs.
		"Debounce": true,
	}
	review := reflect.TypeOf(config.HostReview{})
	for i := 0; i < review.NumField(); i++ {
		name := review.Field(i).Name
		if !hashed[name] && !ignored[name] {
			t.Fatalf("HostReview.%s must be added to hostReviewAnalysisFields or explicitly ignored", name)
		}
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
