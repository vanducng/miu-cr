package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
)

// userHomeDir is a seam so tests can point config/state resolution at a temp dir.
var userHomeDir = os.UserHomeDir

// Dir is the single miu-cr config/state directory, ~/.config/miu/cr, matching
// the miu family convention (miu-db uses ~/.config/miu/db). Deliberately NOT
// os.UserConfigDir(), which on macOS resolves to ~/Library/Application Support.
func Dir() (string, error) {
	home, err := userHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "miu", "cr"), nil
}

// RulesDir returns the user rules directory (Dir()/rules), or "" when the home
// dir is unresolvable so callers can treat it as "no user rule layer". This is
// the single source of truth shared by the live reviewer (wire) and `rules
// check` (cli) so the two never diverge.
func RulesDir() string {
	dir, err := Dir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "rules")
}

// FilePath returns the user config file location (Dir()/config.toml). The file
// is optional; its absence is not an error.
func FilePath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

// FilePathOrEmpty is FilePath without the error, for hints/messages.
func FilePathOrEmpty() string {
	p, err := FilePath()
	if err != nil {
		return ""
	}
	return p
}

// Load returns the layered configuration: built-in Defaults overlaid by the
// user config file when present. A missing file yields the defaults; an
// unreadable or malformed file returns the defaults plus a typed config.invalid
// CLIError (Exit 2) so review/history/serve surface it identically. Importing
// the leaf internal/cli/clierr only (not internal/cli) keeps this cycle-free.
func Load() (Config, error) {
	cfg := Defaults()
	path, err := FilePath()
	if err != nil {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, invalidConfig(path, err)
	}
	var fileCfg Config
	if err := toml.Unmarshal(data, &fileCfg); err != nil {
		return cfg, invalidConfig(path, err)
	}
	return Merge(cfg, fileCfg), nil
}

// invalidConfig wraps a read/parse failure as a typed config.invalid CLIError.
// The go-toml/v2 error carries a row/col, kept in Message; the message is
// redacted in case a path or value fragment is sensitive.
func invalidConfig(path string, err error) error {
	return &clierr.CLIError{
		Code:    "config.invalid",
		Message: RedactString(fmt.Sprintf("config %s: %v", path, err)),
		Hint:    "fix or remove " + path,
		Exit:    2,
		Cause:   err,
	}
}

// Merge overlays file config onto base: a non-empty DefaultProvider wins, and
// each file profile overlays its base namesake field-by-field (non-empty file
// fields win), so a user can override just a model without restating the kind.
func Merge(base, file Config) Config {
	out := Config{
		DefaultProvider: base.DefaultProvider,
		Providers:       make(map[string]Provider, len(base.Providers)+len(file.Providers)),
	}
	if file.DefaultProvider != "" {
		out.DefaultProvider = file.DefaultProvider
	}
	for name, p := range base.Providers {
		out.Providers[name] = p
	}
	for name, fp := range file.Providers {
		out.Providers[name] = mergeProvider(out.Providers[name], fp)
	}
	out.Store = mergeStore(base.Store, file.Store)
	out.Embedding = mergeEmbedding(base.Embedding, file.Embedding)
	out.Github = mergeGithub(base.Github, file.Github)
	out.History = mergeHistory(base.History, file.History)
	out.Review = mergeReview(base.Review, file.Review)
	return out
}

// mergeReview overlays the file [review] table onto base. Non-empty scalar
// fields win (so a file-supplied [review].gate reaches cfg.Review); a non-nil
// Suggest wins (explicit true/false beats base's nil, mirroring mergeHistory); a
// non-empty CategoryURLs replaces base wholesale (a user opts into their own link
// map). An empty/nil field inherits base.
func mergeReview(base, file Review) Review {
	out := base
	if file.Gate != "" {
		out.Gate = file.Gate
	}
	if file.FilterMode != "" {
		out.FilterMode = file.FilterMode
	}
	if file.MinSeverity != "" {
		out.MinSeverity = file.MinSeverity
	}
	if file.Timeout != "" {
		out.Timeout = file.Timeout
	}
	if file.Temperature != nil {
		out.Temperature = file.Temperature
	}
	if file.Thinking != "" {
		out.Thinking = file.Thinking
	}
	if file.Expand != nil {
		out.Expand = file.Expand
	}
	if file.TokenBudget != nil {
		out.TokenBudget = file.TokenBudget
	}
	if file.DeepContext != nil {
		out.DeepContext = file.DeepContext
	}
	if file.ContextHops != nil {
		out.ContextHops = file.ContextHops
	}
	if file.Conversation != nil {
		out.Conversation = file.Conversation
	}
	if file.Suggest != nil {
		out.Suggest = file.Suggest
	}
	if file.PatchRepair != nil {
		out.PatchRepair = file.PatchRepair
	}
	out.Approval = MergeApprovalPolicy(base.Approval, file.Approval)
	out.Subagents = MergeReviewSubagents(base.Subagents, file.Subagents)
	out.PRFilter = MergePRFilter(base.PRFilter, file.PRFilter)
	if len(file.CategoryURLs) > 0 {
		out.CategoryURLs = file.CategoryURLs
	}
	return out
}

func MergeApprovalPolicy(base, over ApprovalPolicy) ApprovalPolicy {
	out := base
	if over.Mode != "" {
		out.Mode = over.Mode
		if over.Mode != "threshold" {
			out.MaxSeverity = ""
			out.Note = ""
		}
	}
	if over.MaxSeverity != "" {
		out.MaxSeverity = over.MaxSeverity
	}
	if over.Note != "" {
		out.Note = over.Note
	}
	if out.Mode != "threshold" {
		out.MaxSeverity = ""
	}
	if out.Mode == "" || out.Mode == "off" {
		out.Note = ""
	}
	return out
}

func MergePRFilter(base, over HostPRFilter) HostPRFilter {
	out := base
	if over.DefaultAction != "" {
		out.DefaultAction = over.DefaultAction
	}
	if over.IncludeDrafts != nil {
		out.IncludeDrafts = over.IncludeDrafts
	}
	out.CommentTriggerRegexes = append(append([]string(nil), base.CommentTriggerRegexes...), over.CommentTriggerRegexes...)
	out.Rules = append(append([]HostPRFilterRule(nil), base.Rules...), over.Rules...)
	return out
}

// MergeReviewSubagents keeps host and user config layering in lockstep.
func MergeReviewSubagents(base, file ReviewSubagents) ReviewSubagents {
	out := base
	if file.Mode != "" {
		out.Mode = file.Mode
	}
	if file.MaxParallel != 0 {
		out.MaxParallel = file.MaxParallel
	}
	if file.MinFiles != 0 {
		out.MinFiles = file.MinFiles
	}
	if file.MinContextBytes != 0 {
		out.MinContextBytes = file.MinContextBytes
	}
	if file.RequireAll != nil {
		out.RequireAll = file.RequireAll
	}
	if len(file.Agents) > 0 {
		out.Agents = append([]ReviewSubagent(nil), file.Agents...)
	}
	return out
}

// mergeHistory overlays non-zero file [history] fields onto base. A nil file
// Enabled inherits base (default-on); an explicit true/false wins. MaxRecords 0
// inherits base so a user can flip enabled without restating the cap.
func mergeHistory(base, file History) History {
	out := base
	if file.Enabled != nil {
		out.Enabled = file.Enabled
	}
	if file.MaxRecords != 0 {
		out.MaxRecords = file.MaxRecords
	}
	return out
}

// mergeGithub overlays non-empty file [github] fields onto base. An empty file
// Mode inherits base (default "pat"), so a config.toml without [github] resolves
// to PAT mode. PrivateKeyPath is a path, never inline PEM.
func mergeGithub(base, file Github) Github {
	out := base
	if file.Mode != "" {
		out.Mode = file.Mode
	}
	if file.AppID != "" {
		out.AppID = file.AppID
	}
	if file.InstallationID != "" {
		out.InstallationID = file.InstallationID
	}
	if file.PrivateKeyPath != "" {
		out.PrivateKeyPath = file.PrivateKeyPath
	}
	return out
}

// mergeEmbedding overlays non-empty file [embedding] fields onto base. Enabled is
// a plain bool: a file with [embedding].enabled = true flips it on; an absent or
// false value leaves base (default disabled). Empty string/zero fields inherit
// base so a user can flip enabled without restating model/dim.
func mergeEmbedding(base, file Embedding) Embedding {
	out := base
	if file.Enabled {
		out.Enabled = true
	}
	if file.Provider != "" {
		out.Provider = file.Provider
	}
	if file.Model != "" {
		out.Model = file.Model
	}
	if file.BaseURL != "" {
		out.BaseURL = file.BaseURL
	}
	if file.Dim != 0 {
		out.Dim = file.Dim
	}
	return out
}

// mergeStore overlays non-empty file [store] fields onto base. An empty file
// Backend inherits base (default "sqlite"), so a config.toml without [store]
// resolves to the SQLite default.
func mergeStore(base, file Store) Store {
	out := base
	if file.Backend != "" {
		out.Backend = file.Backend
	}
	if file.DSN != "" {
		out.DSN = file.DSN
	}
	return out
}

// mergeProvider overlays non-empty file fields onto base. An empty string in the
// file means "inherit base", you cannot clear a built-in field to empty via
// config (and resolve falls back to defaults for empty fields anyway).
func mergeProvider(base, file Provider) Provider {
	out := base
	if file.Kind != "" {
		out.Kind = file.Kind
	}
	if file.BaseURL != "" {
		out.BaseURL = file.BaseURL
	}
	if file.Model != "" {
		out.Model = file.Model
	}
	if file.AuthToken != "" {
		out.AuthToken = file.AuthToken
	}
	if file.AuthEnv != "" {
		out.AuthEnv = file.AuthEnv
	}
	if len(file.AuthCommand) > 0 {
		out.AuthCommand = append([]string(nil), file.AuthCommand...)
	}
	if file.Auth != "" {
		out.Auth = file.Auth
	}
	return out
}
