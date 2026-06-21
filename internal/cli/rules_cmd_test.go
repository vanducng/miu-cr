package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runRules(t *testing.T, args ...string) (string, error) {
	t.Helper()
	prettyOutput = false
	opts := &options{output: "json"}
	cmd := rulesCommand(opts)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	return buf.String(), err
}

// isolateRulesEnv points the cwd at a fresh temp repo and HOME at an empty dir so
// the user rule layer never bleeds in from the real machine.
func isolateRulesEnv(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Chdir(repo)
	return repo
}

func TestRulesInitWritesTemplate(t *testing.T) {
	repo := isolateRulesEnv(t)

	out, err := runRules(t, "init")
	if err != nil {
		t.Fatalf("rules init: %v\nout=%s", err, out)
	}
	env := decodeEnvelope(t, []byte(out))
	if !env.OK || env.Kind != "rules.init" {
		t.Fatalf("want ok rules.init envelope, got %+v", env)
	}

	path := filepath.Join(repo, exampleRulePath)
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("template not written: %v", readErr)
	}
	if !strings.Contains(string(data), "alwaysApply") || !strings.Contains(string(data), "context_files") {
		t.Errorf("template missing v1 keys, got:\n%s", data)
	}
}

func TestRulesInitRefusesOverwrite(t *testing.T) {
	isolateRulesEnv(t)

	if _, err := runRules(t, "init"); err != nil {
		t.Fatalf("first init: %v", err)
	}

	_, err := runRules(t, "init")
	if err == nil {
		t.Fatal("want exists error on second init, got nil")
	}
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "rules.init_exists" || ce.Exit != 2 {
		t.Fatalf("want rules.init_exists exit 2, got %+v", err)
	}

	out, ferr := runRules(t, "init", "--force")
	if ferr != nil {
		t.Fatalf("--force init: %v\nout=%s", ferr, out)
	}
	if env := decodeEnvelope(t, []byte(out)); !env.OK {
		t.Fatalf("want ok envelope on --force, got %+v", env)
	}
}

func TestRulesCheckAppliesExampleRule(t *testing.T) {
	isolateRulesEnv(t)
	if _, err := runRules(t, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}

	// example.md globs include cmd/**/*.go.
	out, err := runRules(t, "check", "cmd/miucr/main.go")
	if err != nil {
		t.Fatalf("check: %v\nout=%s", err, out)
	}
	env := decodeEnvelope(t, []byte(out))
	if !env.OK || env.Kind != "rules.check" {
		t.Fatalf("want ok rules.check envelope, got %+v", env)
	}
	stems := applicableStems(t, env)
	if !contains(stems, "example") {
		t.Errorf("want example rule applicable for cmd path, got stems=%v", stems)
	}
	// Built-in alwaysApply defaults must always be applicable too.
	if !contains(stems, "correctness") {
		t.Errorf("want builtin default applicable, got stems=%v", stems)
	}
}

func TestRulesCheckNonMatchingPath(t *testing.T) {
	isolateRulesEnv(t)
	if _, err := runRules(t, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}

	// README.md matches neither the example globs; only alwaysApply defaults apply.
	out, err := runRules(t, "check", "README.md")
	if err != nil {
		t.Fatalf("check: %v\nout=%s", err, out)
	}
	stems := applicableStems(t, decodeEnvelope(t, []byte(out)))
	if contains(stems, "example") {
		t.Errorf("example rule must NOT apply to README.md, got stems=%v", stems)
	}
}

func TestRulesCheckReportsBodyOnlyFile(t *testing.T) {
	repo := isolateRulesEnv(t)
	rulesDir := filepath.Join(repo, ".miu", "cr", "rules")
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rulesDir, "notes.md"), []byte("# Just prose, no fence\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runRules(t, "check", "any/path.go")
	if err != nil {
		t.Fatalf("check: %v\nout=%s", err, out)
	}
	env := decodeEnvelope(t, []byte(out))
	data, ok := env.Data.(map[string]any)
	if !ok {
		t.Fatalf("data not an object: %T", env.Data)
	}
	bodyOnly, ok := data["body_only"].([]any)
	if !ok || len(bodyOnly) == 0 {
		t.Fatalf("want body_only file reported, got %v", data["body_only"])
	}
	if !strings.Contains(toString(bodyOnly[0]), "notes.md") {
		t.Errorf("body_only should name notes.md, got %v", bodyOnly[0])
	}
}

func TestRulesCheckSurfacesAllWarnings(t *testing.T) {
	repo := isolateRulesEnv(t)
	rulesDir := filepath.Join(repo, ".miu", "cr", "rules")
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Oversized rule: loader skips it with a "too large" warning that must reach
	// the user via the envelope, not just the live reviewer's stderr log.
	big := "---\ndescription: huge\n---\n" + strings.Repeat("A", 64*1024+1)
	if err := os.WriteFile(filepath.Join(rulesDir, "huge.md"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runRules(t, "check", "any/path.go")
	if err != nil {
		t.Fatalf("check: %v\nout=%s", err, out)
	}
	env := decodeEnvelope(t, []byte(out))
	data, ok := env.Data.(map[string]any)
	if !ok {
		t.Fatalf("data not an object: %T", env.Data)
	}
	warnings, ok := data["warnings"].([]any)
	if !ok || len(warnings) == 0 {
		t.Fatalf("want warnings surfaced, got %v", data["warnings"])
	}
	var found bool
	for _, w := range warnings {
		if strings.Contains(toString(w), "huge.md") && strings.Contains(toString(w), "too large") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected oversized-file warning surfaced, got %v", warnings)
	}
}

func applicableStems(t *testing.T, env Envelope) []string {
	t.Helper()
	data, ok := env.Data.(map[string]any)
	if !ok {
		t.Fatalf("data not an object: %T", env.Data)
	}
	rawList, ok := data["applicable"].([]any)
	if !ok {
		t.Fatalf("applicable not a list: %T", data["applicable"])
	}
	var stems []string
	for _, item := range rawList {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		stems = append(stems, toString(m["stem"]))
	}
	return stems
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func toString(v any) string {
	s, _ := v.(string)
	return s
}
