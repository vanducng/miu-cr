package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCodeSummaryToml(t *testing.T) {
	dir := t.TempDir()
	userHomeDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { userHomeDir = os.UserHomeDir })

	body := "[review.code_summary]\nwalkthrough = false\nfile_change_summary = true\n"
	cfgDir := filepath.Join(dir, ".config", "miu", "cr")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Review.CodeSummary.WantWalkthrough() {
		t.Fatal("walkthrough=false in config must resolve OFF")
	}
	if !cfg.Review.CodeSummary.WantFileChangeSummary() {
		t.Fatal("file_change_summary=true in config must resolve ON")
	}
}

func TestCodeSummaryResolveDefaults(t *testing.T) {
	var cs CodeSummary // both unset
	if !cs.WantWalkthrough() {
		t.Fatal("walkthrough must default ON when unset")
	}
	if cs.WantFileChangeSummary() {
		t.Fatal("file_change_summary must default OFF when unset")
	}

	off, on := false, true
	cs = CodeSummary{Walkthrough: &off, FileChangeSummary: &on}
	if cs.WantWalkthrough() {
		t.Fatal("explicit walkthrough=false must win")
	}
	if !cs.WantFileChangeSummary() {
		t.Fatal("explicit file_change_summary=true must win")
	}
}
