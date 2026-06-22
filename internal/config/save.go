package config

import (
	"fmt"
	"os"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"
)

const saveHeader = `# miu-cr config — written by ` + "`miucr init`" + `.
# Only user-set values live here; built-in defaults are layered at load time.
# Prefer auth_env (an env-var NAME) over auth_token (a literal secret on disk).
`

// savedConfig is the on-disk projection of a Config: every field is omitempty
// and sections are pointers so unchanged/default sections produce no output at
// all. This keeps the written file to the user-set deltas only.
type savedConfig struct {
	DefaultProvider string              `toml:"default_provider,omitempty"`
	Providers       map[string]Provider `toml:"providers,omitempty"`
	Store           *Store              `toml:"store,omitempty"`
	Embedding       *Embedding          `toml:"embedding,omitempty"`
	Github          *Github             `toml:"github,omitempty"`
}

// Save writes the user-set deltas of cfg to FilePath() atomically with safe
// perms (dir 0700, file 0600). It marshals ONLY fields that differ from the
// built-in Defaults() — never the merged defaults themselves — so built-in
// provider profiles are never baked onto disk and the file stays minimal.
func Save(cfg Config) error {
	path, err := FilePath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir %s: %w", dir, err)
	}

	body, err := toml.Marshal(delta(cfg))
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	out := append([]byte(saveHeader), body...)

	tmp, err := os.CreateTemp(dir, "config-*.toml.tmp")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("install config %s: %w", path, err)
	}
	return nil
}

// delta returns the on-disk projection holding only fields that differ from
// Defaults(), so the encoded file omits built-in profiles and default sections.
func delta(cfg Config) savedConfig {
	base := Defaults()
	out := savedConfig{Providers: map[string]Provider{}}

	if cfg.DefaultProvider != "" && cfg.DefaultProvider != base.DefaultProvider {
		out.DefaultProvider = cfg.DefaultProvider
	}
	for name, p := range cfg.Providers {
		if bp, ok := base.Providers[name]; ok && p == bp {
			continue
		}
		out.Providers[name] = p
	}
	if len(out.Providers) == 0 {
		out.Providers = nil
	}
	if cfg.Store != base.Store {
		s := cfg.Store
		out.Store = &s
	}
	if cfg.Embedding != base.Embedding {
		e := cfg.Embedding
		out.Embedding = &e
	}
	if cfg.Github != base.Github {
		g := cfg.Github
		out.Github = &g
	}
	return out
}
