package rules

import (
	"errors"
	"strings"
	"testing"
)

const (
	userDir = "testdata/user"
	repoDir = "testdata/repo"
)

func TestParseRule(t *testing.T) {
	t.Run("decodes frontmatter and body", func(t *testing.T) {
		data := []byte("---\n" +
			"description: hello\n" +
			"globs:\n  - \"**/*.go\"\n" +
			"alwaysApply: true\n" +
			"context_files:\n  - a.txt\n" +
			"---\nbody line one\nbody line two\n")
		r, err := ParseRule("rules/x.md", data)
		if err != nil {
			t.Fatalf("ParseRule: %v", err)
		}
		if r.Stem != "x" {
			t.Errorf("stem = %q, want x", r.Stem)
		}
		if r.FM.Description != "hello" {
			t.Errorf("description = %q", r.FM.Description)
		}
		if len(r.FM.Globs) != 1 || r.FM.Globs[0] != "**/*.go" {
			t.Errorf("globs = %v", r.FM.Globs)
		}
		if !r.FM.AlwaysApply {
			t.Errorf("alwaysApply = false, want true")
		}
		if len(r.FM.ContextFiles) != 1 || r.FM.ContextFiles[0] != "a.txt" {
			t.Errorf("context_files = %v", r.FM.ContextFiles)
		}
		if r.Body != "body line one\nbody line two" {
			t.Errorf("body = %q", r.Body)
		}
	})

	t.Run("missing keys default to zero values", func(t *testing.T) {
		r, err := ParseRule("rules/min.md", []byte("---\ndescription: only desc\n---\nbody\n"))
		if err != nil {
			t.Fatalf("ParseRule: %v", err)
		}
		if r.FM.AlwaysApply || len(r.FM.Globs) != 0 || len(r.FM.ContextFiles) != 0 {
			t.Errorf("expected zero defaults, got %+v", r.FM)
		}
	})

	t.Run("no fence is not a rule", func(t *testing.T) {
		_, err := ParseRule("rules/readme.md", []byte("# Heading\n\nNo frontmatter here.\n"))
		if !errors.Is(err, ErrNotARule) {
			t.Errorf("err = %v, want ErrNotARule", err)
		}
	})

	t.Run("unterminated fence errors", func(t *testing.T) {
		_, err := ParseRule("rules/bad.md", []byte("---\ndescription: x\nbody without close\n"))
		if err == nil || errors.Is(err, ErrNotARule) {
			t.Errorf("err = %v, want non-nil non-sentinel", err)
		}
	})

	t.Run("malformed yaml errors", func(t *testing.T) {
		_, err := ParseRule("rules/m.md", []byte("---\ndescription: \"oops\nglobs: [\n---\nbody\n"))
		if err == nil || errors.Is(err, ErrNotARule) {
			t.Errorf("err = %v, want yaml error", err)
		}
	})
}

func TestLoadRulesDefaults(t *testing.T) {
	rules, warnings := LoadRules("", "", false)
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings loading defaults only: %v", warnings)
	}
	want := []string{"correctness", "performance", "reliability", "security", "testing"}
	got := map[string]Rule{}
	for _, r := range rules {
		got[r.Stem] = r
	}
	for _, stem := range want {
		r, ok := got[stem]
		if !ok {
			t.Errorf("missing default rule %q", stem)
			continue
		}
		if r.Provenance != BuiltinDefault {
			t.Errorf("%s provenance = %v, want BuiltinDefault", stem, r.Provenance)
		}
		if !r.Provenance.Trusted() {
			t.Errorf("%s should be trusted", stem)
		}
		if !r.FM.AlwaysApply {
			t.Errorf("%s default should be alwaysApply", stem)
		}
	}
}

func TestLoadRulesLayering(t *testing.T) {
	t.Run("malformed and no-fence files skipped with warnings", func(t *testing.T) {
		rules, warnings := LoadRules(userDir, "", false)
		stems := stemSet(rules)
		if stems["malformed"] {
			t.Errorf("malformed.md should be skipped")
		}
		if stems["README"] {
			t.Errorf("README.md (no fence) should be skipped")
		}
		if !stems["go-style"] || !stems["always"] || !stems["manual"] {
			t.Errorf("expected user rules loaded, got %v", stems)
		}
		var sawMalformed, sawNoFence bool
		for _, w := range warnings {
			if strings.Contains(w, "malformed.md") {
				sawMalformed = true
			}
			if strings.Contains(w, "README.md") && strings.Contains(w, "no frontmatter fence") {
				sawNoFence = true
			}
		}
		if !sawMalformed || !sawNoFence {
			t.Errorf("expected warnings for malformed + no-fence, got %v", warnings)
		}
	})

	t.Run("user rule is trusted, repo rule is untrusted", func(t *testing.T) {
		rules, _ := LoadRules(userDir, repoDir, true)
		byStem := map[string]Rule{}
		for _, r := range rules {
			byStem[r.Stem] = r
		}
		if r := byStem["always"]; r.Provenance != UserTrusted {
			t.Errorf("always provenance = %v, want UserTrusted", r.Provenance)
		}
		if r := byStem["ts"]; r.Provenance != RepoUntrusted {
			t.Errorf("ts provenance = %v, want RepoUntrusted", r.Provenance)
		}
		if byStem["ts"].Provenance.Trusted() {
			t.Errorf("repo rule must not be trusted")
		}
	})

	t.Run("repo must NOT override a trusted user stem", func(t *testing.T) {
		rules, warnings := LoadRules(userDir, repoDir, true)
		var goStyle Rule
		for _, r := range rules {
			if r.Stem == "go-style" {
				goStyle = r
			}
		}
		if goStyle.Provenance != UserTrusted {
			t.Errorf("go-style provenance = %v, want UserTrusted (repo must not override a trusted stem)", goStyle.Provenance)
		}
		if strings.Contains(goStyle.Body, "Repo-specific") {
			t.Errorf("trusted user go-style body must survive, got repo body: %q", goStyle.Body)
		}
		var warned bool
		for _, w := range warnings {
			if strings.Contains(w, "go-style") && strings.Contains(w, "ignore repo rule") {
				warned = true
			}
		}
		if !warned {
			t.Errorf("expected a warning that the repo go-style override was ignored, got %v", warnings)
		}
	})

	t.Run("allowRepo=false drops repo rules", func(t *testing.T) {
		rules, _ := LoadRules(userDir, repoDir, false)
		stems := stemSet(rules)
		if stems["ts"] {
			t.Errorf("ts (repo-only) should be dropped when allowRepo=false")
		}
		var goStyle Rule
		for _, r := range rules {
			if r.Stem == "go-style" {
				goStyle = r
			}
		}
		if goStyle.Provenance != UserTrusted {
			t.Errorf("go-style should fall back to user when allowRepo=false, got %v", goStyle.Provenance)
		}
	})

	t.Run("deterministic stem order", func(t *testing.T) {
		rules, _ := LoadRules(userDir, repoDir, true)
		for i := 1; i < len(rules); i++ {
			if rules[i-1].Stem > rules[i].Stem {
				t.Errorf("not sorted: %q before %q", rules[i-1].Stem, rules[i].Stem)
			}
		}
	})

	t.Run("missing dir is not an error", func(t *testing.T) {
		_, warnings := LoadRules("testdata/does-not-exist", "testdata/also-missing", true)
		if len(warnings) != 0 {
			t.Errorf("missing dirs should not warn, got %v", warnings)
		}
	})
}

func TestSelectRules(t *testing.T) {
	always := Rule{Stem: "always", Provenance: UserTrusted, FM: Frontmatter{AlwaysApply: true}}
	goGlob := Rule{Stem: "go", Provenance: UserTrusted, FM: Frontmatter{Globs: []string{"**/*.go"}}}
	manual := Rule{Stem: "manual", Provenance: UserTrusted, FM: Frontmatter{}}

	t.Run("alwaysApply always selected", func(t *testing.T) {
		got := SelectRules([]Rule{always}, nil)
		if len(got) != 1 || got[0].Stem != "always" {
			t.Errorf("got %v", stems(got))
		}
	})

	t.Run("glob match selects", func(t *testing.T) {
		got := SelectRules([]Rule{goGlob}, []string{"pkg/main.go"})
		if len(got) != 1 {
			t.Errorf("glob should match, got %v", stems(got))
		}
		got = SelectRules([]Rule{goGlob}, []string{"pkg/main.ts"})
		if len(got) != 0 {
			t.Errorf("non-matching glob should not select, got %v", stems(got))
		}
	})

	t.Run("manual-only never auto-selected", func(t *testing.T) {
		got := SelectRules([]Rule{manual}, []string{"x.go", "y.ts"})
		if len(got) != 0 {
			t.Errorf("manual rule must not auto-select, got %v", stems(got))
		}
	})

	t.Run("deterministic order: alwaysApply then trusted then stem", func(t *testing.T) {
		repoAlways := Rule{Stem: "zzz", Provenance: RepoUntrusted, FM: Frontmatter{AlwaysApply: true}}
		userGlobB := Rule{Stem: "bbb", Provenance: UserTrusted, FM: Frontmatter{Globs: []string{"**/*.go"}}}
		repoGlobA := Rule{Stem: "aaa", Provenance: RepoUntrusted, FM: Frontmatter{Globs: []string{"**/*.go"}}}
		in := []Rule{repoGlobA, userGlobB, repoAlways, always}
		got := SelectRules(in, []string{"x.go"})
		want := []string{"always", "zzz", "bbb", "aaa"}
		if g := stems(got); !equal(g, want) {
			t.Errorf("order = %v, want %v", g, want)
		}
	})
}

func stemSet(rules []Rule) map[string]bool {
	m := map[string]bool{}
	for _, r := range rules {
		m[r.Stem] = true
	}
	return m
}

func stems(rules []Rule) []string {
	out := make([]string, len(rules))
	for i, r := range rules {
		out[i] = r.Stem
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
