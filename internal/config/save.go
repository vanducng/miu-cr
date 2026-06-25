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

// showConfig is the display projection for `config show` (without --all): the
// Save delta fields plus the History/Review sections (which Save intentionally
// omits) so a user-set [review].gate is visible. A flat struct (NOT an embedded
// savedConfig) because go-toml/v2 does not inline an anonymous embedded struct.
// Pointer/omitempty sections keep an unchanged section out of the output. This is
// display-only — Save still writes only savedConfig, so init's write path is
// unchanged.
type showConfig struct {
	DefaultProvider string              `toml:"default_provider,omitempty"`
	Providers       map[string]Provider `toml:"providers,omitempty"`
	Store           *Store              `toml:"store,omitempty"`
	Embedding       *Embedding          `toml:"embedding,omitempty"`
	Github          *Github             `toml:"github,omitempty"`
	History         *History            `toml:"history,omitempty"`
	Review          *Review             `toml:"review,omitempty"`
}

// Delta returns the user-set deltas of cfg for `config show` (without --all):
// fields differing from built-in Defaults(), including History/Review which Save
// omits. Returns an opaque marshalable value (omitempty toml tags throughout).
func Delta(cfg Config) any {
	base := Defaults()
	d := delta(cfg)
	out := showConfig{
		DefaultProvider: d.DefaultProvider,
		Providers:       d.Providers,
		Store:           d.Store,
		Embedding:       d.Embedding,
		Github:          d.Github,
	}
	if cfg.History != base.History {
		h := cfg.History
		out.History = &h
	}
	if !reviewEqual(cfg.Review, base.Review) {
		r := cfg.Review
		out.Review = &r
	}
	return out
}

// reviewEqual compares two Review values structurally (Review holds a map +
// pointer, so == is illegal). Used only by Delta to detect a user-set [review].
func reviewEqual(a, b Review) bool {
	if a.Gate != b.Gate || a.FilterMode != b.FilterMode || a.MinSeverity != b.MinSeverity || a.Timeout != b.Timeout {
		return false
	}
	if (a.Suggest == nil) != (b.Suggest == nil) || (a.Suggest != nil && *a.Suggest != *b.Suggest) {
		return false
	}
	if len(a.CategoryURLs) != len(b.CategoryURLs) {
		return false
	}
	for k, v := range a.CategoryURLs {
		if b.CategoryURLs[k] != v {
			return false
		}
	}
	return true
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
