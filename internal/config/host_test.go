package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
)

func TestLoadHostYAMLAnchors(t *testing.T) {
	path := writeHostConfig(t, `version: 1
store:
  backend: postgres
github:
  default_account: app
  accounts:
    app:
      mode: app
      app_id_env: APP_ID
      installation_id_env: INSTALLATION_ID
      private_key_path_env: APP_KEY_PATH
x:
  repo_defaults: &repo_defaults
    default_branch: main
    enabled: true
    poll: true
  host_review: &host_review
    suggest: true
    patch_repair: true
  service_rules: &service_rules
    - rules/service.md
review:
  gate: high
  filter_mode: diff_context
  min_severity: low
  timeout: 900s
host:
  addr: ":8080"
  poll: true
  poll_source: pulls
  poll_interval: 60s
  workspace_dir: /tmp/miucr
  retention:
    janitor_interval: 15m
    workspace_ttl: 168h
    closed_workspace_ttl: 24h
    max_workspace_bytes: 50GiB
    max_workspaces: 200
    min_free_space: 10GiB
    db_ttl: 2160h
  review: *host_review
agent:
  system_prompt_file: prompts/default.md
repos:
  - <<: *repo_defaults
    name: service-api
    slug: example-org/service-api
    git_url: https://github.com/example-org/service-api.git
    github_account: app
    agent:
      system_prompt: |
        Review this service carefully.
    rules: *service_rules
`)

	cfg, err := LoadHost(path)
	if err != nil {
		t.Fatalf("LoadHost: %v", err)
	}
	if len(cfg.Repos) != 1 {
		t.Fatalf("repos: want 1, got %d", len(cfg.Repos))
	}
	repo := cfg.Repos[0]
	if repo.Owner != "example-org" || repo.Repo != "service-api" || repo.DefaultBranch != "main" {
		t.Fatalf("repo normalized wrong: %+v", repo)
	}
	if cfg.Host.Retention.MaxWorkspaceBytes != "50GiB" {
		t.Fatalf("retention not loaded: %+v", cfg.Host.Retention)
	}
	if cfg.Host.Review.PatchRepair == nil || !*cfg.Host.Review.PatchRepair {
		t.Fatalf("merged host review not loaded: %+v", cfg.Host.Review)
	}
	if len(repo.Rules) != 1 || repo.Rules[0] != "rules/service.md" {
		t.Fatalf("sequence anchor not loaded: %+v", repo.Rules)
	}
}

func TestLoadHostDuplicateRepoFails(t *testing.T) {
	path := writeHostConfig(t, minimalHostYAML()+`
repos:
  - name: one
    slug: example-org/service-api
    git_url: https://github.com/example-org/service-api.git
  - name: two
    slug: example-org/service-api
    git_url: https://github.com/example-org/service-api.git
`)
	err := loadHostErr(path)
	if !isConfigInvalid(err) || !strings.Contains(err.Error(), "unique") {
		t.Fatalf("want duplicate config.invalid, got %v", err)
	}
}

func TestLoadHostDuplicateAccountFails(t *testing.T) {
	path := writeHostConfig(t, `version: 1
store:
  backend: postgres
github:
  default_account: pat
  accounts:
    pat:
      mode: pat
      auth_env: GITHUB_TOKEN
    pat:
      mode: pat
      auth_env: OTHER_GITHUB_TOKEN
host:
  poll_source: pulls
repos:
  - name: service-api
    slug: example-org/service-api
    git_url: https://github.com/example-org/service-api.git
`)
	err := loadHostErr(path)
	if !isConfigInvalid(err) || !strings.Contains(err.Error(), "already defined") {
		t.Fatalf("want duplicate account config.invalid, got %v", err)
	}
}

func TestLoadHostPromptSourcesMutuallyExclusive(t *testing.T) {
	path := writeHostConfig(t, minimalHostYAML()+`
agent:
  system_prompt: inline
  system_prompt_file: prompts/default.md
repos:
  - name: service-api
    slug: example-org/service-api
    git_url: https://github.com/example-org/service-api.git
`)
	err := loadHostErr(path)
	if !isConfigInvalid(err) || !strings.Contains(err.Error(), "only one prompt source") {
		t.Fatalf("want prompt config.invalid, got %v", err)
	}
}

func TestLoadHostUnknownAccountFails(t *testing.T) {
	path := writeHostConfig(t, minimalHostYAML()+`
repos:
  - name: service-api
    slug: example-org/service-api
    git_url: https://github.com/example-org/service-api.git
    github_account: missing
`)
	err := loadHostErr(path)
	if !isConfigInvalid(err) || !strings.Contains(err.Error(), "github_account") {
		t.Fatalf("want account config.invalid, got %v", err)
	}
}

func TestLoadHostPatchRepairRequiresSuggest(t *testing.T) {
	path := writeHostConfig(t, minimalHostYAML()+`
repos:
  - name: service-api
    slug: example-org/service-api
    git_url: https://github.com/example-org/service-api.git
    review:
      patch_repair: true
`)
	err := loadHostErr(path)
	if !isConfigInvalid(err) || !strings.Contains(err.Error(), "suggest=true") {
		t.Fatalf("want patch repair config.invalid, got %v", err)
	}
}

func TestLoadHostPatchRepairUsesEffectiveSuggest(t *testing.T) {
	path := writeHostConfig(t, minimalHostYAML()+`
  review:
    suggest: true
repos:
  - name: service-api
    slug: example-org/service-api
    git_url: https://github.com/example-org/service-api.git
    review:
      patch_repair: true
`)
	if _, err := LoadHost(path); err != nil {
		t.Fatalf("LoadHost: %v", err)
	}
}

func TestLoadHostRequiresEffectivePollingRepo(t *testing.T) {
	path := writeHostConfig(t, minimalHostYAML()+`
  poll: false
repos:
  - name: service-api
    slug: example-org/service-api
    git_url: https://github.com/example-org/service-api.git
`)
	err := loadHostErr(path)
	if !isConfigInvalid(err) || !strings.Contains(err.Error(), "enabled for host polling") {
		t.Fatalf("want no polling repo config.invalid, got %v", err)
	}
}

func TestLoadHostUnknownFieldFails(t *testing.T) {
	path := writeHostConfig(t, minimalHostYAML()+`
repos:
  - name: service-api
    slug: example-org/service-api
    git_url: https://github.com/example-org/service-api.git
    poll_intervl: 60s
`)
	err := loadHostErr(path)
	if !isConfigInvalid(err) || !strings.Contains(err.Error(), "poll_intervl") {
		t.Fatalf("want unknown field config.invalid, got %v", err)
	}
}

func TestLoadHostMultipleDocumentsFails(t *testing.T) {
	path := writeHostConfig(t, minimalHostYAML()+`
repos:
  - name: service-api
    slug: example-org/service-api
    git_url: https://github.com/example-org/service-api.git
---
repos: []
`)
	err := loadHostErr(path)
	if !isConfigInvalid(err) || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("want multiple document config.invalid, got %v", err)
	}
}

func TestLoadHostReviewErrorUsesHostPath(t *testing.T) {
	path := writeHostConfig(t, minimalHostYAML()+`
  review:
    timeout: soon
repos:
  - name: service-api
    slug: example-org/service-api
    git_url: https://github.com/example-org/service-api.git
`)
	err := loadHostErr(path)
	if !isConfigInvalid(err) || !strings.Contains(err.Error(), "host.review.timeout") || !strings.Contains(err.Error(), path) {
		t.Fatalf("want host review path config.invalid, got %v", err)
	}
}

func TestLoadHostFromEnv(t *testing.T) {
	path := writeHostConfig(t, minimalHostYAML()+`
repos:
  - name: service-api
    slug: example-org/service-api
    git_url: https://github.com/example-org/service-api.git
`)
	t.Setenv("MIUCR_CONFIG", path)
	cfg, err := LoadHost("")
	if err != nil {
		t.Fatalf("LoadHost env: %v", err)
	}
	if cfg.Repos[0].Slug != "example-org/service-api" {
		t.Fatalf("wrong env config: %+v", cfg.Repos)
	}
}

func TestRedactHostConfigMasksSecrets(t *testing.T) {
	secret := "postgres://user:password@localhost:5432/db"
	cfg := HostConfig{
		Store: HostStore{Backend: "postgres", DSN: secret},
		Providers: map[string]HostProvider{
			"anthropic": {Kind: KindAnthropic, AuthToken: "synthetic-provider-secret"},
		},
	}
	safe := RedactHostConfig(cfg)
	if safe.Store.DSN == secret || safe.Store.DSN == "" {
		t.Fatalf("dsn not masked: %+v", safe.Store)
	}
	if safe.Providers["anthropic"].AuthToken == "synthetic-provider-secret" || safe.Providers["anthropic"].AuthToken == "" {
		t.Fatalf("auth token not masked: %+v", safe.Providers["anthropic"])
	}
	if cfg.Store.DSN != secret {
		t.Fatal("RedactHostConfig mutated input")
	}
}

func minimalHostYAML() string {
	return `version: 1
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
`
}

func writeHostConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "host.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func loadHostErr(path string) error {
	_, err := LoadHost(path)
	return err
}

func isConfigInvalid(err error) bool {
	var ce *clierr.CLIError
	return errors.As(err, &ce) && ce.Code == "config.invalid" && ce.Exit == 2
}
