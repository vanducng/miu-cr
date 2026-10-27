package cli

import (
	stdctx "context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine/agent"
	"github.com/vanducng/miu-cr/internal/serve"
)

func mixedProviderHostConfig() config.HostConfig {
	return config.HostConfig{
		DefaultProvider: "gateway",
		Providers: map[string]config.HostProvider{
			"gateway": {
				Kind:    config.KindAnthropic,
				BaseURL: "https://gateway.example.test/anthropic",
				Model:   "claude-test-model",
				AuthEnv: "MIUCR_TEST_GATEWAY_TOKEN",
				Quota:   &config.QuotaConfig{Dimension: "tokens", Limit: 1000, Window: "24h"},
			},
			"chatgpt": {
				Kind:  config.KindOpenAI,
				Auth:  "oauth",
				Model: "gpt-5.5",
				Quota: &config.QuotaConfig{Dimension: "requests", Limit: 10, Window: "5h"},
			},
		},
		Repos: []config.HostRepo{
			{
				Name:          "service-api",
				Slug:          "example-org/service-api",
				Owner:         "example-org",
				Repo:          "service-api",
				GitURL:        "https://github.com/example-org/service-api.git",
				GithubAccount: "pat",
			},
			{
				Name:          "service-web",
				Slug:          "example-org/service-web",
				Owner:         "example-org",
				Repo:          "service-web",
				GitURL:        "https://github.com/example-org/service-web.git",
				GithubAccount: "pat",
				Provider:      "chatgpt",
			},
		},
	}
}

func clearHostProviderEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL", "OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_MODEL", "MIUCR_CODEX_MODEL"} {
		t.Setenv(k, "")
	}
}

func TestBuildServeHostReposResolvesProviderPerRepo(t *testing.T) {
	t.Setenv("MIUCR_TEST_GATEWAY_TOKEN", "gateway-token")
	cfg := mixedProviderHostConfig()
	repos, _, err := buildServeHostRepos(stdctx.Background(), cfg, filepath.Join(t.TempDir(), "host.yaml"))
	if err != nil {
		t.Fatalf("buildServeHostRepos: %v", err)
	}
	api, web := repos[0].Review, repos[1].Review
	if api.Provider != "anthropic" || api.AuthToken != "gateway-token" || api.APIKey != "" || api.BaseURL != "https://gateway.example.test/anthropic" || api.Model != "claude-test-model" {
		t.Fatalf("default repo must use default_provider gateway: %+v", api)
	}
	if api.QuotaProvider != "gateway" || !reflect.DeepEqual(api.Quota, cfg.Providers["gateway"].Quota) {
		t.Fatalf("default repo quota = %q %+v, want gateway quota", api.QuotaProvider, api.Quota)
	}
	if web.Provider != "openai" || web.Auth != "oauth" || web.APIKey != "" || web.AuthToken != "" || web.BaseURL != "" || web.Model != "gpt-5.5" {
		t.Fatalf("override repo must use chatgpt provider without a static secret: %+v", web)
	}
	if web.QuotaProvider != "chatgpt" || !reflect.DeepEqual(web.Quota, cfg.Providers["chatgpt"].Quota) {
		t.Fatalf("override repo quota = %q %+v, want chatgpt quota", web.QuotaProvider, web.Quota)
	}
}

func TestBuildServeHostReposFallsBackToBuiltInDefaultProvider(t *testing.T) {
	cfg := mixedProviderHostConfig()
	cfg.DefaultProvider = ""
	cfg.Repos[1].Provider = string(config.KindOpenAI)
	repos, _, err := buildServeHostRepos(stdctx.Background(), cfg, filepath.Join(t.TempDir(), "host.yaml"))
	if err != nil {
		t.Fatalf("buildServeHostRepos: %v", err)
	}
	if got := repos[0].Review; got.Provider != "anthropic" || got.QuotaProvider != "anthropic" || got.Quota != nil {
		t.Fatalf("unset default_provider must fall back to built-in anthropic: %+v", got)
	}
	if got := repos[1].Review; got.Provider != "openai" || got.QuotaProvider != "openai" || got.BaseURL != config.DefaultOpenAIBaseURL || got.Model != "" {
		t.Fatalf("built-in provider override must resolve: %+v", got)
	}
}

func TestBuildServeHostReposRejectsUnknownRepoProvider(t *testing.T) {
	t.Setenv("MIUCR_TEST_GATEWAY_TOKEN", "gateway-token")
	cfg := mixedProviderHostConfig()
	cfg.Repos[1].Provider = "missing"
	_, _, err := buildServeHostRepos(stdctx.Background(), cfg, filepath.Join(t.TempDir(), "host.yaml"))
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "agent.unknown_provider" || !strings.Contains(ce.Message, "missing") {
		t.Fatalf("want unknown provider error, got %+v", err)
	}
}

func writeHostSecretHelper(t *testing.T, body string) []string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell helper")
	}
	helper := filepath.Join(t.TempDir(), "token.sh")
	if err := os.WriteFile(helper, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return []string{"/bin/sh", helper}
}

func TestBuildServeHostReposResolvesEachProviderSecretOnce(t *testing.T) {
	calls := filepath.Join(t.TempDir(), "calls")
	cfg := mixedProviderHostConfig()
	gateway := cfg.Providers["gateway"]
	gateway.AuthEnv = ""
	gateway.AuthCommand = writeHostSecretHelper(t, "echo x >> '"+calls+"'\nprintf gateway-token\n")
	cfg.Providers["gateway"] = gateway
	third := cfg.Repos[0]
	third.Name, third.Slug, third.Repo = "service-jobs", "example-org/service-jobs", "service-jobs"
	third.GitURL = "https://github.com/example-org/service-jobs.git"
	third.Provider = "gateway"
	cfg.Repos = append(cfg.Repos, third)

	repos, _, err := buildServeHostRepos(stdctx.Background(), cfg, filepath.Join(t.TempDir(), "host.yaml"))
	if err != nil {
		t.Fatalf("buildServeHostRepos: %v", err)
	}
	if repos[0].Review.AuthToken != "gateway-token" || repos[2].Review.AuthToken != "gateway-token" {
		t.Fatalf("gateway repos missing secret: %+v / %+v", repos[0].Review, repos[2].Review)
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "x"); n != 1 {
		t.Fatalf("auth_command ran %d times for one provider, want 1", n)
	}
}

func TestBuildServeHostReposSkipsSecretForDisabledRepo(t *testing.T) {
	t.Setenv("MIUCR_TEST_GATEWAY_TOKEN", "")
	disabled := false
	cfg := mixedProviderHostConfig()
	cfg.Repos[0].Enabled = &disabled
	repos, _, err := buildServeHostRepos(stdctx.Background(), cfg, filepath.Join(t.TempDir(), "host.yaml"))
	if err != nil {
		t.Fatalf("a provider used only by a disabled repo must not need its secret: %v", err)
	}
	if got := repos[0].Review; repos[0].Enabled || got.AuthToken != "" || got.QuotaProvider != "gateway" || got.Model != "claude-test-model" {
		t.Fatalf("disabled repo options = %+v", got)
	}
}

func TestBuildServeHostReposSecretErrorNamesProvider(t *testing.T) {
	cfg := mixedProviderHostConfig()
	gateway := cfg.Providers["gateway"]
	gateway.AuthEnv = ""
	gateway.AuthCommand = writeHostSecretHelper(t, "exit 3\n")
	cfg.Providers["gateway"] = gateway
	_, _, err := buildServeHostRepos(stdctx.Background(), cfg, filepath.Join(t.TempDir(), "host.yaml"))
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "agent.auth_command_failed" || !strings.Contains(ce.Message, `host provider "gateway"`) {
		t.Fatalf("want auth_command error naming the provider, got %+v", err)
	}
}

func resolveHostRepoCreds(opts serve.JobReviewOptions, loggedIn bool) (agent.Credentials, error) {
	return agent.Resolve(agent.ResolveInput{
		Ctx:       stdctx.Background(),
		Provider:  opts.Provider,
		APIKey:    opts.APIKey,
		BaseURL:   opts.BaseURL,
		AuthToken: opts.AuthToken,
		Auth:      opts.Auth,
		Model:     opts.Model,
		OAuthResolver: func(stdctx.Context) (agent.OAuthCredential, bool, error) {
			if !loggedIn {
				return agent.OAuthCredential{}, false, nil
			}
			return agent.OAuthCredential{AccessToken: "oauth-access", AccountID: "acct-test", BackendBaseURL: "https://codex.example.test/backend"}, true, nil
		},
	})
}

func TestMixedHostProvidersResolveDistinctCredentials(t *testing.T) {
	clearHostProviderEnv(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MIUCR_TEST_GATEWAY_TOKEN", "gateway-token")
	t.Setenv("OPENAI_API_KEY", "sk-ambient")
	cfg := mixedProviderHostConfig()
	third := cfg.Repos[1]
	third.Name, third.Slug, third.Repo = "service-jobs", "example-org/service-jobs", "service-jobs"
	third.GitURL = "https://github.com/example-org/service-jobs.git"
	third.Provider = string(config.KindOpenAI)
	cfg.Repos = append(cfg.Repos, third)
	repos, _, err := buildServeHostRepos(stdctx.Background(), cfg, filepath.Join(t.TempDir(), "host.yaml"))
	if err != nil {
		t.Fatalf("buildServeHostRepos: %v", err)
	}
	resolve := func(i int) agent.Credentials {
		t.Helper()
		creds, err := resolveHostRepoCreds(repos[i].Review, true)
		if err != nil {
			t.Fatalf("resolve %s: %v", repos[i].Slug, err)
		}
		return creds
	}
	api := resolve(0)
	if api.Kind != config.KindAnthropic || api.Backend != "" || api.AuthToken != "gateway-token" || api.BaseURL != "https://gateway.example.test/anthropic" || api.Model != "claude-test-model" {
		t.Fatalf("anthropic repo creds wrong: kind=%s backend=%q base=%q model=%q", api.Kind, api.Backend, api.BaseURL, api.Model)
	}
	web := resolve(1)
	if web.Kind != config.KindOpenAI || web.Backend != "codex" || web.OAuthToken != "oauth-access" || web.APIKey != "" || web.Model != "gpt-5.5" {
		t.Fatalf("openai oauth repo creds wrong: kind=%s backend=%q model=%q api_key_set=%t", web.Kind, web.Backend, web.Model, web.APIKey != "")
	}
	builtin := resolve(2)
	if builtin.Backend != "codex" || builtin.Model != config.DefaultCodexModel {
		t.Fatalf("built-in openai repo with a login session must use the codex default model: backend=%q model=%q", builtin.Backend, builtin.Model)
	}
	_, err = resolveHostRepoCreds(repos[1].Review, false)
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "agent.no_credentials" {
		t.Fatalf("auth oauth without a login session must fail, not use OPENAI_API_KEY: %v", err)
	}
}
