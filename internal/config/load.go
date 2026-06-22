package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"
)

// userHomeDir is a seam so tests can point config/state resolution at a temp dir.
var userHomeDir = os.UserHomeDir

// Dir is the single miu-cr config/state directory, ~/.config/miu/cr — matching
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
// unreadable or malformed file returns the defaults plus an error so callers
// can surface it without losing a working baseline.
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
		return cfg, fmt.Errorf("read config %s: %w", path, err)
	}
	var fileCfg Config
	if err := toml.Unmarshal(data, &fileCfg); err != nil {
		return cfg, fmt.Errorf("parse config %s: %w", path, err)
	}
	return Merge(cfg, fileCfg), nil
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
// file means "inherit base" — you cannot clear a built-in field to empty via
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
	return out
}
