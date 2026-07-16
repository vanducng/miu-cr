package symbolcontext

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "t@example.com")
	runGit(t, repo, "config", "user.name", "T")
	runGit(t, repo, "config", "commit.gpgsign", "false")
	return repo
}

func writeRepoFile(t *testing.T, repo, name, body string) {
	t.Helper()
	path := filepath.Join(repo, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func commitRepo(t *testing.T, repo, msg string) string {
	t.Helper()
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", msg)
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func symbolConfig() config.SymbolContext {
	return config.SymbolContext{}
}

func TestToolSpecDescribesRelationUsage(t *testing.T) {
	spec := ToolSpec()
	relation, ok := spec.Properties["relation"].(map[string]any)
	if !ok {
		t.Fatalf("relation schema missing: %+v", spec.Properties)
	}
	text := spec.Description + " " + relation["description"].(string)
	for _, want := range []string{"document_symbols maps", "definition finds", "references finds", "incoming_calls finds", "outgoing_calls lists", "implementations finds", "dependencies traces"} {
		if !strings.Contains(text, want) {
			t.Fatalf("tool spec missing %q:\n%s", want, text)
		}
	}
}

func TestRunDefinitionReadsReviewedRevision(t *testing.T) {
	repo := initRepo(t)
	writeRepoFile(t, repo, "src/user.ts", "export function loadUser() {\n  return 1\n}\n")
	sha := commitRepo(t, repo, "initial")
	writeRepoFile(t, repo, "src/user.ts", "export function loadUserLeaked() {\n  return 2\n}\n")

	out, isErr := Run(context.Background(), symbolConfig(), Context{RepoDir: repo, Rev: sha, Runner: gitcmd.New()}, 0, []byte(`{"relation":"definition","symbol":"loadUser"}`))
	if isErr {
		t.Fatalf("Run returned error output: %s", out)
	}
	if !strings.Contains(out, "src/user.ts:1") || !strings.Contains(out, "loadUser()") {
		t.Fatalf("expected committed symbol, got %q", out)
	}
	if strings.Contains(out, "loadUserLeaked") {
		t.Fatalf("worktree edit leaked into symbol context: %q", out)
	}
}

func TestRunDefinitionReadsStagedIndex(t *testing.T) {
	repo := initRepo(t)
	writeRepoFile(t, repo, "app/service.py", "def staged_service():\n    return 1\n")
	runGit(t, repo, "add", "-A")
	writeRepoFile(t, repo, "app/service.py", "def leaked_service():\n    return 2\n")

	out, isErr := Run(context.Background(), symbolConfig(), Context{RepoDir: repo, Runner: gitcmd.New()}, 0, []byte(`{"relation":"definition","symbol":"staged_service"}`))
	if isErr {
		t.Fatalf("Run returned error output: %s", out)
	}
	if !strings.Contains(out, "staged_service") {
		t.Fatalf("expected staged symbol, got %q", out)
	}
	if strings.Contains(out, "leaked_service") {
		t.Fatalf("worktree edit leaked into symbol context: %q", out)
	}
}

func TestRunDefinitionDoesNotMatchFileBasename(t *testing.T) {
	repo := initRepo(t)
	writeRepoFile(t, repo, "internal/engine/engine.go", `package engine

type Request struct {}
type Engine struct {}
func (e *Engine) Review() {}
`)
	sha := commitRepo(t, repo, "engine")

	out, isErr := Run(context.Background(), symbolConfig(), Context{RepoDir: repo, Rev: sha, Runner: gitcmd.New()}, 0, []byte(`{"relation":"definition","symbol":"Engine","limit":10}`))
	if isErr || !strings.Contains(out, "type Engine") || strings.Contains(out, "type Request") {
		t.Fatalf("definition basename match = %q isErr=%v", out, isErr)
	}
}

func TestRunDocumentSymbolsAndOutgoingCalls(t *testing.T) {
	repo := initRepo(t)
	writeRepoFile(t, repo, "main.go", `package main

func Handler() {
	loadUser()
	sendEmail()
}

func loadUser() {}
func sendEmail() {}
`)
	sha := commitRepo(t, repo, "go")

	summary, isErr := Run(context.Background(), symbolConfig(), Context{RepoDir: repo, Rev: sha, Runner: gitcmd.New()}, 0, []byte(`{"relation":"document_symbols","file":"main.go"}`))
	if isErr || !strings.Contains(summary, "Handler") || !strings.Contains(summary, "loadUser") {
		t.Fatalf("document_symbols = %q isErr=%v", summary, isErr)
	}
	outgoing, isErr := Run(context.Background(), symbolConfig(), Context{RepoDir: repo, Rev: sha, Runner: gitcmd.New()}, 0, []byte(`{"relation":"outgoing_calls","symbol":"Handler"}`))
	if isErr || !strings.Contains(outgoing, "loadUser") || !strings.Contains(outgoing, "sendEmail") {
		t.Fatalf("outgoing_calls = %q isErr=%v", outgoing, isErr)
	}
}

func TestRunDefinitionParallelRepoScan(t *testing.T) {
	repo := initRepo(t)
	for i := 0; i < 20; i++ {
		name := filepath.Join("src", "pkg", "f"+string(rune('a'+i))+".ts")
		body := "export function helper" + string(rune('A'+i)) + "() { return 1 }\n"
		if i == 19 {
			body = "export function targetSymbol() { return 42 }\n"
		}
		writeRepoFile(t, repo, name, body)
	}
	sha := commitRepo(t, repo, "many")
	cfg := symbolConfig()
	cfg.MaxParallel = 4

	out, isErr := Run(context.Background(), cfg, Context{RepoDir: repo, Rev: sha, Runner: gitcmd.New()}, 0, []byte(`{"relation":"definition","symbol":"targetSymbol","limit":1}`))
	if isErr || !strings.Contains(out, "targetSymbol") {
		t.Fatalf("parallel definition scan = %q isErr=%v", out, isErr)
	}
}

func TestRunDependencies(t *testing.T) {
	repo := initRepo(t)
	writeRepoFile(t, repo, "models/orders.sql", `select * from {{ ref("customers") }} join {{ source("stripe", "charges") }} on true`)
	sha := commitRepo(t, repo, "dbt")

	out, isErr := Run(context.Background(), symbolConfig(), Context{RepoDir: repo, Rev: sha, Runner: gitcmd.New()}, 0, []byte(`{"relation":"dependencies","file":"models/orders.sql"}`))
	if isErr || !strings.Contains(out, "dbt.ref: customers") || !strings.Contains(out, "dbt.source: stripe.charges") {
		t.Fatalf("dependencies = %q isErr=%v", out, isErr)
	}
}

func TestRunDocumentSymbolsScriptValues(t *testing.T) {
	repo := initRepo(t)
	writeRepoFile(t, repo, "workflow.js", `export const meta = {
  name: 'adversarial-review',
}

const DIMENSIONS = [
  { key: 'correctness' },
]
`)
	sha := commitRepo(t, repo, "script-values")

	out, isErr := Run(context.Background(), symbolConfig(), Context{RepoDir: repo, Rev: sha, Runner: gitcmd.New()}, 0, []byte(`{"relation":"document_symbols","file":"workflow.js"}`))
	if isErr || !strings.Contains(out, "[value] export const meta") || !strings.Contains(out, "DIMENSIONS") {
		t.Fatalf("script values summary = %q isErr=%v", out, isErr)
	}
}

func TestRunDocumentSymbolsSingleFileComponents(t *testing.T) {
	repo := initRepo(t)
	writeRepoFile(t, repo, "src/StatusBadge.astro", `---
const API_URL = "/api";
type Status = "open" | "closed";
const formatStatus = (status: Status) => status.toUpperCase();
---
<span>{formatStatus("open")}</span>
`)
	writeRepoFile(t, repo, "src/UserPanel.vue", `<script setup lang="ts">
type UserPanelProps = { name: string };
const formatName = (name: string) => name.trim();
</script>
<template><section>{{ formatName("Ada") }}</section></template>
`)
	writeRepoFile(t, repo, "src/ActionMenu.svelte", `<script lang="ts">
type ActionMenuProps = { label: string };
const labelText = (label: string) => label.trim();
</script>
<button>{labelText("Open")}</button>
`)
	sha := commitRepo(t, repo, "components")

	astro, isErr := Run(context.Background(), symbolConfig(), Context{RepoDir: repo, Rev: sha, Runner: gitcmd.New()}, 0, []byte(`{"relation":"document_symbols","file":"src/StatusBadge.astro"}`))
	if isErr || !strings.Contains(astro, "component StatusBadge") || !strings.Contains(astro, "formatStatus") {
		t.Fatalf("astro summary = %q isErr=%v", astro, isErr)
	}
	if strings.Contains(astro, "[component] const API_URL") {
		t.Fatalf("all-caps constant should not be treated as component: %q", astro)
	}
	vue, isErr := Run(context.Background(), symbolConfig(), Context{RepoDir: repo, Rev: sha, Runner: gitcmd.New()}, 0, []byte(`{"relation":"definition","symbol":"UserPanel"}`))
	if isErr || !strings.Contains(vue, "src/UserPanel.vue:1") {
		t.Fatalf("vue component definition = %q isErr=%v", vue, isErr)
	}
	svelte, isErr := Run(context.Background(), symbolConfig(), Context{RepoDir: repo, Rev: sha, Runner: gitcmd.New()}, 0, []byte(`{"relation":"document_symbols","file":"src/ActionMenu.svelte"}`))
	if isErr || !strings.Contains(svelte, "component ActionMenu") || !strings.Contains(svelte, "labelText") {
		t.Fatalf("svelte summary = %q isErr=%v", svelte, isErr)
	}
}

func TestRunCapsOutput(t *testing.T) {
	repo := initRepo(t)
	writeRepoFile(t, repo, "many.ts", "export function firstSymbol() {}\nexport function secondSymbol() {}\n")
	sha := commitRepo(t, repo, "many")
	cfg := symbolConfig()
	cfg.MaxBytes = 30

	out, isErr := Run(context.Background(), cfg, Context{RepoDir: repo, Rev: sha, Runner: gitcmd.New()}, 0, []byte(`{"relation":"document_symbols","file":"many.ts"}`))
	if isErr || !strings.HasSuffix(out, "\n...(truncated)") {
		t.Fatalf("capped output = %q isErr=%v", out, isErr)
	}
	if len(out) > cfg.MaxBytes {
		t.Fatalf("capped output length = %d, want <= %d", len(out), cfg.MaxBytes)
	}
}

// A directory passed to document_symbols must answer with a file listing the
// model can act on, not a raw git error that wastes the turn.
func TestDocumentSymbolsOnDirectoryListsFiles(t *testing.T) {
	repo := initRepo(t)
	writeRepoFile(t, repo, "pkg/a.go", "package pkg\n\nfunc Alpha() {}\n")
	writeRepoFile(t, repo, "pkg/b.go", "package pkg\n\nfunc Beta() {}\n")
	rev := commitRepo(t, repo, "init")

	out, err := scan(context.Background(), symbolConfig(), Context{RepoDir: repo, Rev: rev, Runner: gitcmd.New()},
		Args{Relation: "document_symbols", File: "pkg"})
	if err != nil {
		t.Fatalf("directory path must not error: %v", err)
	}
	if !strings.Contains(out, "directory") || !strings.Contains(out, "pkg/a.go") || !strings.Contains(out, "pkg/b.go") {
		t.Fatalf("want directory listing, got: %q", out)
	}
}

// references with file+line but no symbol must resolve the enclosing
// definition instead of erroring.
func TestReferencesResolvesSymbolFromFileLine(t *testing.T) {
	repo := initRepo(t)
	writeRepoFile(t, repo, "a.go", "package a\n\nfunc Target() {\n\thelper()\n}\n")
	writeRepoFile(t, repo, "b.go", "package a\n\nfunc caller() {\n\tTarget()\n}\n")
	rev := commitRepo(t, repo, "init")

	out, err := scan(context.Background(), symbolConfig(), Context{RepoDir: repo, Rev: rev, Runner: gitcmd.New()},
		Args{Relation: "references", File: "a.go", Line: 4})
	if err != nil {
		t.Fatalf("references with file+line must resolve a symbol: %v", err)
	}
	if !strings.Contains(out, "References for Target") {
		t.Fatalf("want references for Target, got: %q", out)
	}
}

// "path.go:42" in the file argument must fold the trailing line into Line.
func TestFileArgWithTrailingLineSuffix(t *testing.T) {
	repo := initRepo(t)
	writeRepoFile(t, repo, "a.go", "package a\n\nfunc Target() {\n\thelper()\n}\n")
	rev := commitRepo(t, repo, "init")

	out, err := scan(context.Background(), symbolConfig(), Context{RepoDir: repo, Rev: rev, Runner: gitcmd.New()},
		Args{Relation: "references", File: "a.go:3"})
	if err != nil {
		t.Fatalf("file:line suffix must parse: %v", err)
	}
	if !strings.Contains(out, "References for Target") {
		t.Fatalf("want references for Target, got: %q", out)
	}

	if got, n := splitTrailingLine("plain.go"); got != "plain.go" || n != 0 {
		t.Fatalf("plain path must pass through, got %q %d", got, n)
	}
}
