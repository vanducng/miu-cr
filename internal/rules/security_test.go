package rules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDirSkipsSymlinkRule(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret.md")
	if err := os.WriteFile(secret, []byte("---\ndescription: secret\n---\nleaked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.md")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	// Remove the real target file from the rules dir so only the symlink remains
	// as a candidate, proving the symlink itself is skipped (not just dedup'd).
	subdir := t.TempDir()
	outside := filepath.Join(subdir, "passwd.md")
	if err := os.WriteFile(outside, []byte("---\ndescription: x\n---\nroot:x:0:0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(dir, "escape.md")
	if err := os.Symlink(outside, escape); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	rules, warnings := loadDir(dir, RepoUntrusted)
	for _, r := range rules {
		if r.Stem == "escape" {
			t.Errorf("symlink escape.md must not be loaded: %+v", r)
		}
		if strings.Contains(r.Body, "root:x:0:0") {
			t.Errorf("symlinked content leaked into a rule body: %q", r.Body)
		}
	}
	var warned bool
	for _, w := range warnings {
		if strings.Contains(w, "escape.md") && strings.Contains(w, "symlink") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("expected a symlink-skip warning, got %v", warnings)
	}
}

func TestLoadDirSkipsOversizedRule(t *testing.T) {
	dir := t.TempDir()
	big := "---\ndescription: huge\n---\n" + strings.Repeat("A", maxRuleFileBytes+1)
	if err := os.WriteFile(filepath.Join(dir, "huge.md"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	rules, warnings := loadDir(dir, RepoUntrusted)
	if len(rules) != 0 {
		t.Errorf("oversized rule must be skipped, got %d rules", len(rules))
	}
	var warned bool
	for _, w := range warnings {
		if strings.Contains(w, "huge.md") && strings.Contains(w, "too large") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("expected an oversized-skip warning, got %v", warnings)
	}
}

func TestInlineContextFileRejectsSymlinkEscape(t *testing.T) {
	ruleDir := t.TempDir()
	outsideDir := t.TempDir()
	secret := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(ruleDir, "ctx.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	r := Rule{
		Stem:       "withctx",
		Path:       filepath.Join(ruleDir, "withctx.md"),
		Provenance: RepoUntrusted,
		FM:         Frontmatter{AlwaysApply: true, ContextFiles: []string{"ctx.txt"}},
	}
	text, _, _ := BuildRulesSection([]Rule{r}, true, 0, false)
	if strings.Contains(text, "TOP SECRET") {
		t.Errorf("symlinked context_file escaped the rule directory: %q", text)
	}
	// O_NOFOLLOW refuses the symlinked final component atomically at open time.
	if !strings.Contains(text, "symlink not allowed") {
		t.Errorf("expected symlink-skip note for symlinked context_file: %q", text)
	}
}

func TestLoadRulesWarnsIntraRepoStemCollision(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("---\ndescription: "+body+"\n---\n"+body+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// "custom" is not a built-in default, so a collision here exercises the
	// intra-repo path, not the trusted-override path. The two names differ only
	// by extension case → same stem on a case-sensitive FS.
	write("custom.md", "first")
	write("custom.MD", "second")
	entries, _ := os.ReadDir(dir)
	if len(entries) < 2 {
		t.Skip("case-insensitive filesystem coalesced the two rule files")
	}

	_, warnings := LoadRules("", dir, true)
	var warned bool
	for _, w := range warnings {
		if strings.Contains(w, "duplicate stem") && strings.Contains(w, "custom") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("expected intra-repo duplicate-stem warning, got %v", warnings)
	}
}

func TestInlineContextFileRejectsOversized(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("Z", maxContextFileBytes+1)
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Rule{
		Stem:       "withctx",
		Path:       filepath.Join(dir, "withctx.md"),
		Provenance: UserTrusted,
		FM:         Frontmatter{AlwaysApply: true, ContextFiles: []string{"big.txt"}},
	}
	text, _, _ := BuildRulesSection([]Rule{r}, true, 0, false)
	if strings.Contains(text, "ZZZZ") {
		t.Errorf("oversized context_file must be rejected, not partially inlined: %q", text)
	}
	if !strings.Contains(text, "exceeds byte cap") {
		t.Errorf("expected oversized-skip note, got: %q", text)
	}
}

func TestInlineContextFileSkipNoteHidesAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	r := Rule{
		Stem:       "withctx",
		Path:       filepath.Join(dir, "withctx.md"),
		Provenance: UserTrusted,
		FM:         Frontmatter{AlwaysApply: true, ContextFiles: []string{"missing.txt"}},
	}
	text, _, _ := BuildRulesSection([]Rule{r}, true, 0, false)
	if strings.Contains(text, dir) {
		t.Errorf("skip note leaked the absolute path %q: %s", dir, text)
	}
	if !strings.Contains(text, "missing.txt") || !strings.Contains(text, "skipped") {
		t.Errorf("expected a skip note for the missing file: %q", text)
	}
}
