package engine_test

import (
	stdctx "context"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
	"github.com/vanducng/miu-cr/internal/rules"
)

func stagedGoChange(t *testing.T) string {
	t.Helper()
	dir := initRepo(t)
	writeFile(t, dir, "app.go", "package app\n\nfunc Existing() int { return 1 }\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")
	writeFile(t, dir, "app.go", "package app\n\nfunc Existing() int { return 1 }\n\nfunc Added() {}\n")
	git(t, dir, "add", "app.go")
	return dir
}

func reviewWith(t *testing.T, dir string, fa *fakeAgent, req engine.Request) (engine.ReviewResult, *fakeAgent) {
	t.Helper()
	req.Mode = 0
	req.RepoDir = dir
	req.Gate = "high"
	req.Extensions = []string{"go"}
	eng := engine.New(fa, gitcmd.New())
	res, err := eng.Review(stdctx.Background(), req)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	return res, fa
}

func TestRulesReachUserTurnBeforeDiff(t *testing.T) {
	dir := stagedGoChange(t)
	loaded := []rules.Rule{
		{Stem: "gorule", Provenance: rules.UserTrusted, FM: rules.Frontmatter{Description: "go rule", Globs: []string{"**/*.go"}}, Body: "TRUSTED_RULE_MARKER"},
	}
	fa := &fakeAgent{}
	_, fa = reviewWith(t, dir, fa, engine.Request{Rules: loaded, RulesTokenBudget: 4096})

	if !strings.Contains(fa.gotRules, "TRUSTED_RULE_MARKER") {
		t.Fatalf("selected rule did not reach AgentContext.Rules: %q", fa.gotRules)
	}
	if strings.Contains(fa.gotRules, "UNTRUSTED") {
		t.Errorf("trusted rule must not be fenced: %q", fa.gotRules)
	}
}

func TestRulesUntrustedFenced(t *testing.T) {
	dir := stagedGoChange(t)
	loaded := []rules.Rule{
		{Stem: "repo", Provenance: rules.RepoUntrusted, FM: rules.Frontmatter{AlwaysApply: true}, Body: "REPO_RULE_MARKER"},
	}
	fa := &fakeAgent{}
	_, fa = reviewWith(t, dir, fa, engine.Request{Rules: loaded, RulesFork: false, RulesTokenBudget: 4096})

	if !strings.Contains(fa.gotRules, "REPO_RULE_MARKER") {
		t.Fatalf("repo rule missing on non-fork: %q", fa.gotRules)
	}
	if !strings.Contains(fa.gotRules, "UNTRUSTED") {
		t.Errorf("repo rule must be context-only fenced: %q", fa.gotRules)
	}
}

func TestRulesForkDropsRepoRules(t *testing.T) {
	dir := stagedGoChange(t)
	loaded := []rules.Rule{
		{Stem: "userrule", Provenance: rules.UserTrusted, FM: rules.Frontmatter{AlwaysApply: true}, Body: "USER_MARKER"},
		{Stem: "reporule", Provenance: rules.RepoUntrusted, FM: rules.Frontmatter{AlwaysApply: true}, Body: "REPO_MARKER"},
	}
	fa := &fakeAgent{}
	res, fa := reviewWith(t, dir, fa, engine.Request{Rules: loaded, RulesFork: true, RulesTokenBudget: 4096})

	if strings.Contains(fa.gotRules, "REPO_MARKER") {
		t.Errorf("fork PR must drop repo rules: %q", fa.gotRules)
	}
	if !strings.Contains(fa.gotRules, "USER_MARKER") {
		t.Errorf("user rule must survive on fork: %q", fa.gotRules)
	}
	if got, _ := res.Stats["rules_applied"].(float64); got != 1 {
		t.Errorf("rules_applied = %v, want 1", res.Stats["rules_applied"])
	}
}

func TestRulesStatsAndBudgetFloor(t *testing.T) {
	dir := stagedGoChange(t)
	loaded := []rules.Rule{
		{Stem: "a", Provenance: rules.UserTrusted, FM: rules.Frontmatter{AlwaysApply: true}, Body: strings.Repeat("word ", 600)},
		{Stem: "b", Provenance: rules.UserTrusted, FM: rules.Frontmatter{AlwaysApply: true}, Body: strings.Repeat("word ", 600)},
		{Stem: "c", Provenance: rules.UserTrusted, FM: rules.Frontmatter{AlwaysApply: true}, Body: strings.Repeat("word ", 600)},
	}
	fa := &fakeAgent{}
	// Tiny total budget + a rules cap: the diff budget must not collapse to <=0.
	res, _ := reviewWith(t, dir, fa, engine.Request{Rules: loaded, RulesTokenBudget: 200, TokenBudget: 300})

	if _, ok := res.Stats["rules_applied"]; !ok {
		t.Errorf("rules_applied missing from stats")
	}
	if tr, ok := res.Stats["rules_truncated"].(bool); !ok || !tr {
		t.Errorf("rules_truncated = %v, want true under tight cap", res.Stats["rules_truncated"])
	}
}

func TestRulesNoneApplied(t *testing.T) {
	dir := stagedGoChange(t)
	loaded := []rules.Rule{
		{Stem: "tsonly", Provenance: rules.UserTrusted, FM: rules.Frontmatter{Globs: []string{"**/*.ts"}}, Body: "NOPE"},
	}
	fa := &fakeAgent{}
	_, fa = reviewWith(t, dir, fa, engine.Request{Rules: loaded, RulesTokenBudget: 4096})
	if fa.gotRules != "" {
		t.Errorf("no matching rule should yield empty rules section, got %q", fa.gotRules)
	}
}
