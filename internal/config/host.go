package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	yaml "go.yaml.in/yaml/v4"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
)

type HostConfig struct {
	Version         int                     `yaml:"version" json:"version,omitempty"`
	DefaultProvider string                  `yaml:"default_provider" json:"default_provider,omitempty"`
	Providers       map[string]HostProvider `yaml:"providers" json:"providers,omitempty"`
	Store           HostStore               `yaml:"store" json:"store"`
	Github          HostGithub              `yaml:"github" json:"github"`
	Review          HostReview              `yaml:"review" json:"review,omitempty"`
	Agent           HostAgent               `yaml:"agent" json:"agent,omitempty"`
	Host            HostRuntime             `yaml:"host" json:"host"`
	Repos           []HostRepo              `yaml:"repos" json:"repos"`
	X               map[string]any          `yaml:"x,omitempty" json:"-"`
}

type HostProvider struct {
	Kind        Kind     `yaml:"kind" json:"kind,omitempty"`
	BaseURL     string   `yaml:"base_url" json:"base_url,omitempty"`
	Model       string   `yaml:"model" json:"model,omitempty"`
	AuthToken   string   `yaml:"auth_token" json:"auth_token,omitempty"`
	AuthEnv     string   `yaml:"auth_env" json:"auth_env,omitempty"`
	AuthCommand []string `yaml:"auth_command" json:"auth_command,omitempty"`
	Auth        string   `yaml:"auth" json:"auth,omitempty"`
}

type HostStore struct {
	Backend string `yaml:"backend" json:"backend,omitempty"`
	DSN     string `yaml:"dsn" json:"dsn,omitempty"`
}

type HostGithub struct {
	DefaultAccount string                       `yaml:"default_account" json:"default_account,omitempty"`
	Accounts       map[string]HostGithubAccount `yaml:"accounts" json:"accounts,omitempty"`
}

type HostGithubAccount struct {
	Mode              string   `yaml:"mode" json:"mode,omitempty"`
	AuthEnv           string   `yaml:"auth_env" json:"auth_env,omitempty"`
	AuthCommand       []string `yaml:"auth_command" json:"auth_command,omitempty"`
	AuthFile          string   `yaml:"auth_file" json:"auth_file,omitempty"`
	ClientID          string   `yaml:"client_id" json:"client_id,omitempty"`
	ClientIDEnv       string   `yaml:"client_id_env" json:"client_id_env,omitempty"`
	AppID             string   `yaml:"app_id" json:"app_id,omitempty"`
	AppIDEnv          string   `yaml:"app_id_env" json:"app_id_env,omitempty"`
	InstallationID    string   `yaml:"installation_id" json:"installation_id,omitempty"`
	InstallationIDEnv string   `yaml:"installation_id_env" json:"installation_id_env,omitempty"`
	PrivateKeyPath    string   `yaml:"private_key_path" json:"private_key_path,omitempty"`
	PrivateKeyPathEnv string   `yaml:"private_key_path_env" json:"private_key_path_env,omitempty"`
	PrivateKeyCommand []string `yaml:"private_key_command" json:"private_key_command,omitempty"`
}

type HostAgent struct {
	SystemPrompt     string `yaml:"system_prompt" json:"system_prompt,omitempty"`
	SystemPromptFile string `yaml:"system_prompt_file" json:"system_prompt_file,omitempty"`
}

type HostRuntime struct {
	Enabled           *bool         `yaml:"enabled" json:"enabled,omitempty"`
	Addr              string        `yaml:"addr" json:"addr,omitempty"`
	Workers           int           `yaml:"workers" json:"workers,omitempty"`
	Webhook           *bool         `yaml:"webhook" json:"webhook,omitempty"`
	WebhookSecretEnv  string        `yaml:"webhook_secret_env" json:"webhook_secret_env,omitempty"`
	APITokenEnv       string        `yaml:"api_token_env" json:"api_token_env,omitempty"`
	Poll              *bool         `yaml:"poll" json:"poll,omitempty"`
	PollSource        string        `yaml:"poll_source" json:"poll_source,omitempty"`
	PollInterval      string        `yaml:"poll_interval" json:"poll_interval,omitempty"`
	TriggerCommand    string        `yaml:"trigger_command" json:"trigger_command,omitempty"`
	WorkspaceDir      string        `yaml:"workspace_dir" json:"workspace_dir,omitempty"`
	WorkspaceStrategy string        `yaml:"workspace_strategy" json:"workspace_strategy,omitempty"`
	Retention         HostRetention `yaml:"retention" json:"retention,omitempty"`
	Review            HostReview    `yaml:"review" json:"review,omitempty"`
}

type HostRetention struct {
	JanitorInterval    string `yaml:"janitor_interval" json:"janitor_interval,omitempty"`
	WorkspaceTTL       string `yaml:"workspace_ttl" json:"workspace_ttl,omitempty"`
	ClosedWorkspaceTTL string `yaml:"closed_workspace_ttl" json:"closed_workspace_ttl,omitempty"`
	MaxWorkspaceBytes  string `yaml:"max_workspace_bytes" json:"max_workspace_bytes,omitempty"`
	MaxWorkspaces      int    `yaml:"max_workspaces" json:"max_workspaces,omitempty"`
	MinFreeSpace       string `yaml:"min_free_space" json:"min_free_space,omitempty"`
	DBTTL              string `yaml:"db_ttl" json:"db_ttl,omitempty"`
}

type HostReview struct {
	Gate         string          `yaml:"gate" json:"gate,omitempty"`
	FilterMode   string          `yaml:"filter_mode" json:"filter_mode,omitempty"`
	MinSeverity  string          `yaml:"min_severity" json:"min_severity,omitempty"`
	Format       string          `yaml:"format" json:"format,omitempty"`
	Timeout      string          `yaml:"timeout" json:"timeout,omitempty"`
	Expand       *int            `yaml:"expand" json:"expand,omitempty"`
	TokenBudget  *int            `yaml:"token_budget" json:"token_budget,omitempty"`
	ContextHops  *int            `yaml:"context_hops" json:"context_hops,omitempty"`
	Mode         string          `yaml:"mode" json:"mode,omitempty"`
	DeepContext  *bool           `yaml:"deep_context" json:"deep_context,omitempty"`
	Conversation *bool           `yaml:"conversation" json:"conversation,omitempty"`
	Post         *bool           `yaml:"post" json:"post,omitempty"`
	Force        *bool           `yaml:"force" json:"force,omitempty"`
	Suggest      *bool           `yaml:"suggest" json:"suggest,omitempty"`
	PatchRepair  *bool           `yaml:"patch_repair" json:"patch_repair,omitempty"`
	Approval     ApprovalPolicy  `yaml:"approval" json:"approval"`
	Subagents    ReviewSubagents `yaml:"subagents" json:"subagents"`
	PRFilter     HostPRFilter    `yaml:"pr_filter" json:"pr_filter,omitempty"`
}

type HostPRFilter struct {
	DefaultAction         string             `toml:"default_action,omitempty" yaml:"default_action" json:"default_action,omitempty"`
	IncludeDrafts         *bool              `toml:"include_drafts,omitempty" yaml:"include_drafts" json:"include_drafts,omitempty"`
	CommentTriggerRegexes []string           `toml:"comment_trigger_regexes,omitempty" yaml:"comment_trigger_regexes" json:"comment_trigger_regexes,omitempty"`
	Rules                 []HostPRFilterRule `toml:"rules,omitempty" yaml:"rules,omitempty" json:"rules,omitempty"`
}

type HostPRFilterRule struct {
	Action             string   `toml:"action,omitempty" yaml:"action" json:"action,omitempty"`
	Authors            []string `toml:"authors,omitempty" yaml:"authors" json:"authors,omitempty"`
	AuthorTypes        []string `toml:"author_types,omitempty" yaml:"author_types" json:"author_types,omitempty"`
	AuthorAssociations []string `toml:"author_associations,omitempty" yaml:"author_associations" json:"author_associations,omitempty"`
	TitleRegexes       []string `toml:"title_regexes,omitempty" yaml:"title_regexes" json:"title_regexes,omitempty"`
	Labels             []string `toml:"labels,omitempty" yaml:"labels" json:"labels,omitempty"`
	RequestedReviewers []string `toml:"requested_reviewers,omitempty" yaml:"requested_reviewers" json:"requested_reviewers,omitempty"`
	BaseBranches       []string `toml:"base_branches,omitempty" yaml:"base_branches" json:"base_branches,omitempty"`
	HeadBranches       []string `toml:"head_branches,omitempty" yaml:"head_branches" json:"head_branches,omitempty"`
}

type HostRepo struct {
	Name          string     `yaml:"name" json:"name"`
	Slug          string     `yaml:"slug" json:"slug"`
	Owner         string     `yaml:"-" json:"owner,omitempty"` // derived from slug; not user-settable
	Repo          string     `yaml:"-" json:"repo,omitempty"`  // derived from slug; not user-settable
	GitURL        string     `yaml:"git_url" json:"git_url"`
	DefaultBranch string     `yaml:"default_branch" json:"default_branch,omitempty"`
	GithubAccount string     `yaml:"github_account" json:"github_account,omitempty"`
	Enabled       *bool      `yaml:"enabled" json:"enabled,omitempty"`
	Poll          *bool      `yaml:"poll" json:"poll,omitempty"`
	Agent         HostAgent  `yaml:"agent" json:"agent,omitempty"`
	Rules         []string   `yaml:"rules" json:"rules,omitempty"`
	Review        HostReview `yaml:"review" json:"review,omitempty"`
}

func HostFilePath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "host.yaml"), nil
}

func LoadHost(path string) (HostConfig, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = strings.TrimSpace(os.Getenv("MIUCR_CONFIG"))
	}
	if path == "" {
		var err error
		path, err = HostFilePath()
		if err != nil {
			return HostConfig{}, err
		}
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return HostConfig{}, invalidConfig(path, err)
	}
	if err != nil {
		return HostConfig{}, invalidConfig(path, err)
	}
	var cfg HostConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return HostConfig{}, invalidConfig(path, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			err = errors.New("multiple YAML documents are not supported")
		}
		return HostConfig{}, invalidConfig(path, err)
	}
	if err := ValidateHost(cfg, path); err != nil {
		return HostConfig{}, err
	}
	normalizeHost(&cfg)
	return cfg, nil
}

func ValidateHost(cfg HostConfig, path string) error {
	if cfg.Store.Backend != "postgres" {
		return invalidHost(path, "store.backend", cfg.Store.Backend, "postgres")
	}
	if err := validateHostReview(path, "review", cfg.Review); err != nil {
		return err
	}
	if err := validateHostReview(path, "host.review", cfg.Host.Review); err != nil {
		return err
	}
	if err := validateHostAgent(path, "agent", cfg.Agent); err != nil {
		return err
	}
	if err := validateRetention(path, cfg.Host.Retention); err != nil {
		return err
	}
	if err := validatePoll(path, cfg.Host); err != nil {
		return err
	}
	if err := validateHostProviders(path, cfg.Providers); err != nil {
		return err
	}
	if err := validateGithubAccounts(path, cfg.Github); err != nil {
		return err
	}
	if err := validateHostRepos(path, cfg); err != nil {
		return err
	}
	if err := validateHostHasPollingRepo(path, cfg); err != nil {
		return err
	}
	return validateHostPatchRepair(path, cfg)
}

func validateHostProviders(path string, providers map[string]HostProvider) error {
	for name, provider := range providers {
		if strings.TrimSpace(name) == "" {
			return invalidHost(path, "providers", "", "non-empty provider names")
		}
		if strings.TrimSpace(provider.AuthToken) != "" {
			return invalidHost(path, "providers."+name+".auth_token", "<set>", "auth_env or auth_command")
		}
	}
	return nil
}

func normalizeHost(cfg *HostConfig) {
	for i := range cfg.Repos {
		owner, repo, ok := strings.Cut(cfg.Repos[i].Slug, "/")
		if ok {
			cfg.Repos[i].Owner, cfg.Repos[i].Repo = owner, repo
		}
		if cfg.Repos[i].DefaultBranch == "" {
			cfg.Repos[i].DefaultBranch = "main"
		}
		if cfg.Repos[i].GithubAccount == "" {
			cfg.Repos[i].GithubAccount = cfg.Github.DefaultAccount
		}
	}
}

func validateGithubAccounts(path string, gh HostGithub) error {
	if len(gh.Accounts) == 0 {
		return invalidHost(path, "github.accounts", "", "at least one account")
	}
	if gh.DefaultAccount != "" {
		if _, ok := gh.Accounts[gh.DefaultAccount]; !ok {
			return invalidHost(path, "github.default_account", gh.DefaultAccount, "a configured account")
		}
	}
	for name, acct := range gh.Accounts {
		if strings.TrimSpace(name) == "" {
			return invalidHost(path, "github.accounts", "", "non-empty account names")
		}
		mode := strings.TrimSpace(acct.Mode)
		switch mode {
		case "pat":
			if countSet(acct.ClientID, acct.ClientIDEnv, acct.AppID, acct.AppIDEnv, acct.InstallationID, acct.InstallationIDEnv, acct.PrivateKeyPath, acct.PrivateKeyPathEnv)+countSlice(acct.PrivateKeyCommand) != 0 {
				return invalidHost(path, "github.accounts."+name, mode, "PAT accounts must not set app/client/private_key fields")
			}
			if countSet(acct.AuthEnv, acct.AuthFile)+countSlice(acct.AuthCommand) != 1 {
				return invalidHost(path, "github.accounts."+name, mode, "exactly one PAT source: auth_env, auth_file, or auth_command")
			}
		case "app":
			if countSet(acct.AuthEnv, acct.AuthFile)+countSlice(acct.AuthCommand) != 0 {
				return invalidHost(path, "github.accounts."+name, mode, "app accounts must not set PAT auth fields")
			}
			if countSet(acct.AppID, acct.AppIDEnv) != 1 {
				return invalidHost(path, "github.accounts."+name+".app_id", "", "exactly one of app_id or app_id_env")
			}
			if countSet(acct.InstallationID, acct.InstallationIDEnv) != 1 {
				return invalidHost(path, "github.accounts."+name+".installation_id", "", "exactly one of installation_id or installation_id_env")
			}
			if countSet(acct.PrivateKeyPath, acct.PrivateKeyPathEnv)+countSlice(acct.PrivateKeyCommand) != 1 {
				return invalidHost(path, "github.accounts."+name+".private_key", "", "exactly one of private_key_path, private_key_path_env, or private_key_command")
			}
		default:
			return invalidHost(path, "github.accounts."+name+".mode", mode, "pat|app")
		}
	}
	return nil
}

func validateHostRepos(path string, cfg HostConfig) error {
	if len(cfg.Repos) == 0 {
		return invalidHost(path, "repos", "", "at least one repo")
	}
	names := map[string]struct{}{}
	slugs := map[string]struct{}{}
	pairs := map[string]struct{}{}
	for i, repo := range cfg.Repos {
		prefix := fmt.Sprintf("repos[%d]", i)
		if strings.TrimSpace(repo.Name) == "" {
			return invalidHost(path, prefix+".name", "", "non-empty")
		}
		if _, dup := names[repo.Name]; dup {
			return invalidHost(path, prefix+".name", repo.Name, "unique")
		}
		names[repo.Name] = struct{}{}
		owner, name, ok := strings.Cut(repo.Slug, "/")
		if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
			return invalidHost(path, prefix+".slug", repo.Slug, "owner/repo")
		}
		if _, dup := slugs[repo.Slug]; dup {
			return invalidHost(path, prefix+".slug", repo.Slug, "unique")
		}
		slugs[repo.Slug] = struct{}{}
		pair := owner + "/" + name
		if _, dup := pairs[pair]; dup {
			return invalidHost(path, prefix+".slug", repo.Slug, "unique owner/repo")
		}
		pairs[pair] = struct{}{}
		account := repo.GithubAccount
		if account == "" {
			account = cfg.Github.DefaultAccount
		}
		if account == "" {
			return invalidHost(path, prefix+".github_account", "", "a configured account or github.default_account")
		}
		if _, ok := cfg.Github.Accounts[account]; !ok {
			return invalidHost(path, prefix+".github_account", account, "a configured account")
		}
		if err := validateGitURL(path, prefix+".git_url", repo.GitURL, owner, name); err != nil {
			return err
		}
		if err := validateHostAgent(path, prefix+".agent", repo.Agent); err != nil {
			return err
		}
		if err := validateHostReview(path, prefix+".review", repo.Review); err != nil {
			return err
		}
	}
	return nil
}

func validateHostHasPollingRepo(path string, cfg HostConfig) error {
	hostEnabled := cfg.Host.Enabled == nil || *cfg.Host.Enabled
	hostPoll := cfg.Host.Poll == nil || *cfg.Host.Poll
	for _, repo := range cfg.Repos {
		enabled := hostEnabled && (repo.Enabled == nil || *repo.Enabled)
		poll := hostPoll && (repo.Poll == nil || *repo.Poll)
		if enabled && poll {
			return nil
		}
	}
	return invalidHost(path, "repos", "", "at least one repo enabled for host polling")
}

func validateGitURL(path, field, raw, owner, repo string) error {
	if strings.TrimSpace(raw) == "" {
		return invalidHost(path, field, raw, "non-empty")
	}
	if strings.HasPrefix(raw, "git@github.com:") {
		want := owner + "/" + repo + ".git"
		if strings.TrimPrefix(raw, "git@github.com:") != want {
			return invalidHost(path, field, raw, "git@github.com:"+want)
		}
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return invalidHost(path, field, raw, "https://github.com/"+owner+"/"+repo+".git or git@github.com:"+owner+"/"+repo+".git")
	}
	if u.Scheme != "https" && u.Scheme != "ssh" {
		return invalidHost(path, field, raw, "https or ssh GitHub URL")
	}
	if !strings.EqualFold(u.Host, "github.com") {
		return invalidHost(path, field, raw, "github.com")
	}
	got := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
	if got != owner+"/"+repo {
		return invalidHost(path, field, raw, "a GitHub URL matching "+owner+"/"+repo)
	}
	return nil
}

func validateHostAgent(path, field string, a HostAgent) error {
	if strings.TrimSpace(a.SystemPrompt) != "" && strings.TrimSpace(a.SystemPromptFile) != "" {
		return invalidHost(path, field, "system_prompt + system_prompt_file", "only one prompt source")
	}
	return nil
}

func validateHostReview(path, field string, r HostReview) error {
	if r.Gate != "" && gateValidator != nil && !gateValidator(r.Gate) {
		return invalidHost(path, field+".gate", r.Gate, "none|info|low|medium|high|critical")
	}
	if r.FilterMode != "" && filterModeValidator != nil && !filterModeValidator(r.FilterMode) {
		return invalidHost(path, field+".filter_mode", r.FilterMode, "added|diff_context|file|nofilter")
	}
	if r.MinSeverity != "" && minSeverityValidator != nil && !minSeverityValidator(r.MinSeverity) {
		return invalidHost(path, field+".min_severity", r.MinSeverity, "none|info|low|medium|high|critical")
	}
	if r.Format != "" && formatValidator != nil && !formatValidator(r.Format) {
		return invalidHost(path, field+".format", r.Format, "full|minimal")
	}
	if r.Timeout != "" {
		if _, err := time.ParseDuration(r.Timeout); err != nil {
			return invalidHost(path, field+".timeout", r.Timeout, "a Go duration like 300s or 5m")
		}
	}
	if r.Mode != "" && r.Mode != "review" && r.Mode != "checks" {
		return invalidHost(path, field+".mode", r.Mode, "review|checks")
	}
	switch r.Approval.Mode {
	case "", "off", "clean", "threshold":
	default:
		return invalidHost(path, field+".approval.mode", r.Approval.Mode, "off|clean|threshold")
	}
	if r.Approval.MaxPriority != "" {
		if r.Approval.Mode != "threshold" {
			return invalidHost(path, field+".approval.max_priority", r.Approval.MaxPriority, "only used when approval.mode is \"threshold\"")
		}
		if !validApprovalPriority(r.Approval.MaxPriority) {
			return invalidHost(path, field+".approval.max_priority", r.Approval.MaxPriority, "P0|P1|P2|P3|P4")
		}
	}
	switch r.Approval.Note {
	case "", "none", "on_findings", "always":
	default:
		return invalidHost(path, field+".approval.note", r.Approval.Note, "none|on_findings|always")
	}
	if r.Expand != nil && *r.Expand < 0 {
		return invalidHost(path, field+".expand", strconv.Itoa(*r.Expand), ">= 0")
	}
	if r.ContextHops != nil && *r.ContextHops < 0 {
		return invalidHost(path, field+".context_hops", strconv.Itoa(*r.ContextHops), ">= 0")
	}
	if r.TokenBudget != nil && *r.TokenBudget < 0 {
		return invalidHost(path, field+".token_budget", strconv.Itoa(*r.TokenBudget), ">= 0")
	}
	if err := validateHostSubagents(path, field+".subagents", r.Subagents); err != nil {
		return err
	}
	if err := validateHostPRFilter(path, field+".pr_filter", r.PRFilter); err != nil {
		return err
	}
	return nil
}

func validateHostPRFilter(path, field string, f HostPRFilter) error {
	switch f.DefaultAction {
	case "", "include", "exclude":
	default:
		return invalidHost(path, field+".default_action", f.DefaultAction, "include|exclude")
	}
	for i, v := range f.CommentTriggerRegexes {
		if strings.TrimSpace(v) == "" {
			return invalidHost(path, fmt.Sprintf("%s.comment_trigger_regexes[%d]", field, i), "", "non-empty")
		}
		if _, err := regexp.Compile(v); err != nil {
			return invalidHost(path, fmt.Sprintf("%s.comment_trigger_regexes[%d]", field, i), v, "valid regexp")
		}
	}
	for i, r := range f.Rules {
		prefix := fmt.Sprintf("%s.rules[%d]", field, i)
		switch r.Action {
		case "include", "exclude":
		default:
			return invalidHost(path, prefix+".action", r.Action, "include|exclude")
		}
		if countSlice(r.Authors)+countSlice(r.AuthorTypes)+countSlice(r.AuthorAssociations)+countSlice(r.TitleRegexes)+countSlice(r.Labels)+countSlice(r.RequestedReviewers)+countSlice(r.BaseBranches)+countSlice(r.HeadBranches) == 0 {
			return invalidHost(path, prefix, "", "at least one matcher")
		}
		for j, v := range r.Authors {
			if strings.TrimSpace(v) == "" {
				return invalidHost(path, fmt.Sprintf("%s.authors[%d]", prefix, j), "", "non-empty")
			}
		}
		for j, v := range r.AuthorTypes {
			if strings.TrimSpace(v) == "" {
				return invalidHost(path, fmt.Sprintf("%s.author_types[%d]", prefix, j), "", "non-empty")
			}
		}
		for j, v := range r.AuthorAssociations {
			if strings.TrimSpace(v) == "" {
				return invalidHost(path, fmt.Sprintf("%s.author_associations[%d]", prefix, j), "", "non-empty")
			}
		}
		for j, v := range r.TitleRegexes {
			if _, err := regexp.Compile(v); err != nil {
				return invalidHost(path, fmt.Sprintf("%s.title_regexes[%d]", prefix, j), v, "valid regexp")
			}
		}
		for j, v := range r.Labels {
			if strings.TrimSpace(v) == "" {
				return invalidHost(path, fmt.Sprintf("%s.labels[%d]", prefix, j), "", "non-empty")
			}
		}
		for j, v := range r.RequestedReviewers {
			if strings.TrimSpace(v) == "" {
				return invalidHost(path, fmt.Sprintf("%s.requested_reviewers[%d]", prefix, j), "", "non-empty")
			}
		}
		for j, v := range r.BaseBranches {
			if strings.TrimSpace(v) == "" {
				return invalidHost(path, fmt.Sprintf("%s.base_branches[%d]", prefix, j), "", "non-empty")
			}
		}
		for j, v := range r.HeadBranches {
			if strings.TrimSpace(v) == "" {
				return invalidHost(path, fmt.Sprintf("%s.head_branches[%d]", prefix, j), "", "non-empty")
			}
		}
	}
	return nil
}

func validateHostSubagents(path, field string, s ReviewSubagents) error {
	switch s.Mode {
	case "", "off", "auto", "always":
	default:
		return invalidHost(path, field+".mode", s.Mode, "off|auto|always")
	}
	if s.MaxParallel < 0 {
		return invalidHost(path, field+".max_parallel", strconv.Itoa(s.MaxParallel), ">= 0")
	}
	if s.MinFiles < 0 {
		return invalidHost(path, field+".min_files", strconv.Itoa(s.MinFiles), ">= 0")
	}
	if s.MinContextBytes < 0 {
		return invalidHost(path, field+".min_context_bytes", strconv.Itoa(s.MinContextBytes), ">= 0")
	}
	if len(s.Agents) > 8 {
		return invalidHost(path, field+".agents", strconv.Itoa(len(s.Agents)), "at most 8 agents")
	}
	seen := make(map[string]bool, len(s.Agents))
	for i, a := range s.Agents {
		prefix := fmt.Sprintf("%s.agents[%d]", field, i)
		if a.Name == "" {
			return invalidHost(path, prefix+".name", "", "non-empty")
		}
		if seen[a.Name] {
			return invalidHost(path, prefix+".name", a.Name, "unique")
		}
		seen[a.Name] = true
		if len(a.Include) == 0 {
			return invalidHost(path, prefix+".include", "", "at least one glob")
		}
		for j, g := range a.Include {
			if g == "" {
				return invalidHost(path, fmt.Sprintf("%s.include[%d]", prefix, j), "", "non-empty glob")
			}
		}
		for j, g := range a.Exclude {
			if g == "" {
				return invalidHost(path, fmt.Sprintf("%s.exclude[%d]", prefix, j), "", "non-empty glob")
			}
		}
	}
	return nil
}

func validateHostPatchRepair(path string, cfg HostConfig) error {
	hostReview := inheritHostPatchPolicy(cfg.Review, cfg.Host.Review)
	if boolValue(hostReview.PatchRepair) && !boolValue(hostReview.Suggest) {
		field := "host.review"
		if cfg.Host.Review.PatchRepair == nil {
			field = "review"
		}
		return invalidHost(path, field+".patch_repair", "true", "effective suggest=true")
	}
	for i, repo := range cfg.Repos {
		repoReview := inheritHostPatchPolicy(hostReview, repo.Review)
		if boolValue(repoReview.PatchRepair) && !boolValue(repoReview.Suggest) {
			return invalidHost(path, fmt.Sprintf("repos[%d].review.patch_repair", i), "true", "effective suggest=true")
		}
	}
	return nil
}

func inheritHostPatchPolicy(base, over HostReview) HostReview {
	out := base
	if over.Suggest != nil {
		out.Suggest = over.Suggest
	}
	if over.PatchRepair != nil {
		out.PatchRepair = over.PatchRepair
	}
	return out
}

func validatePoll(path string, h HostRuntime) error {
	if h.PollInterval != "" {
		if _, err := time.ParseDuration(h.PollInterval); err != nil {
			return invalidHost(path, "host.poll_interval", h.PollInterval, "a Go duration like 60s")
		}
	}
	if h.PollSource != "" && h.PollSource != "pulls" {
		return invalidHost(path, "host.poll_source", h.PollSource, "pulls")
	}
	if h.Workers < 0 {
		return invalidHost(path, "host.workers", strconv.Itoa(h.Workers), ">= 0")
	}
	return nil
}

func validateRetention(path string, r HostRetention) error {
	for field, value := range map[string]string{
		"janitor_interval":     r.JanitorInterval,
		"workspace_ttl":        r.WorkspaceTTL,
		"closed_workspace_ttl": r.ClosedWorkspaceTTL,
		"db_ttl":               r.DBTTL,
	} {
		if value == "" {
			continue
		}
		if d, err := time.ParseDuration(value); err != nil || d <= 0 {
			return invalidHost(path, "host.retention."+field, value, "a positive Go duration")
		}
	}
	for field, value := range map[string]string{
		"max_workspace_bytes": r.MaxWorkspaceBytes,
		"min_free_space":      r.MinFreeSpace,
	} {
		if value == "" {
			continue
		}
		if _, err := ParseByteSize(value); err != nil {
			return invalidHost(path, "host.retention."+field, value, "a positive byte size like 10GiB")
		}
	}
	if r.MaxWorkspaces < 0 {
		return invalidHost(path, "host.retention.max_workspaces", strconv.Itoa(r.MaxWorkspaces), ">= 0")
	}
	return nil
}

// ParseByteSize validates host retention byte limits without accepting floats.
func ParseByteSize(value string) (int64, error) {
	s := strings.TrimSpace(value)
	if s == "" {
		return 0, errors.New("empty")
	}
	units := []struct {
		suffix string
		mul    int64
	}{
		{"tib", 1 << 40}, {"gib", 1 << 30}, {"mib", 1 << 20}, {"kib", 1 << 10},
		{"tb", 1000 * 1000 * 1000 * 1000}, {"gb", 1000 * 1000 * 1000}, {"mb", 1000 * 1000}, {"kb", 1000},
		{"b", 1},
	}
	lower := strings.ToLower(s)
	mul := int64(1)
	num := lower
	for _, u := range units {
		if strings.HasSuffix(lower, u.suffix) {
			mul = u.mul
			num = strings.TrimSpace(strings.TrimSuffix(lower, u.suffix))
			break
		}
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil || n <= 0 {
		return 0, errors.New("invalid size")
	}
	return n * mul, nil
}

func boolValue(v *bool) bool { return v != nil && *v }

func countSet(values ...string) int {
	n := 0
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			n++
		}
	}
	return n
}

func countSlice(values []string) int {
	if len(values) > 0 {
		return 1
	}
	return 0
}

func invalidHost(path, field, value, want string) error {
	return &clierr.CLIError{
		Code:    "config.invalid",
		Message: fmt.Sprintf("config %s: %s %q is invalid: want %s", path, field, RedactString(value), want),
		Hint:    "fix " + field + " in " + path,
		Exit:    2,
	}
}
