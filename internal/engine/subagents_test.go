package engine_test

import (
	stdctx "context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

type subagentFake struct {
	mu          sync.Mutex
	calls       []engine.AgentContext
	failBackend bool
}

func (f *subagentFake) Review(_ stdctx.Context, rc engine.AgentContext) (engine.ReviewOutput, error) {
	f.mu.Lock()
	f.calls = append(f.calls, rc)
	f.mu.Unlock()

	var findings []engine.Finding
	if strings.Contains(rc.Text, "BackendRisk()") {
		if f.failBackend {
			return engine.ReviewOutput{}, errors.New("Authorization: Bearer sk-synthetic-secret-token")
		}
		findings = append(findings, engine.Finding{File: "backend.go", Severity: "high", Category: "bug", Rationale: "backend issue", QuotedCode: "func BackendRisk() {}"})
	}
	if strings.Contains(rc.Text, "frontendRisk()") {
		findings = append(findings, engine.Finding{File: "frontend.ts", Severity: "medium", Category: "bug", Rationale: "frontend issue", QuotedCode: "export function frontendRisk() {}"})
	}
	rc.Trace.SetSystemPrompt("fake system")
	rc.Trace.SetPrompt(rc.Text)
	rc.Trace.RecordTool(0, "grep", "Risk")
	rc.Trace.RecordToolResult(0, "grep", "Risk", strings.Repeat("x", 17*1024), false)
	rc.Trace.SetFinalResponse(`{"findings":[]}`)
	return engine.ReviewOutput{
		Findings:      findings,
		Walkthrough:   "- reviewed scoped files",
		FileSummaries: map[string]string{"summary.txt": "scoped"},
	}, nil
}

func (f *subagentFake) RepairPatch(stdctx.Context, engine.RepairRequest) (string, engine.Usage, error) {
	return "", engine.Usage{}, nil
}

func (f *subagentFake) RelocateQuote(stdctx.Context, engine.RelocateRequest) (string, engine.Usage, error) {
	return "", engine.Usage{}, nil
}

func TestReviewSubagentsFanOutAndMerge(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "backend.go", "package app\n\nfunc Existing() {}\n")
	writeFile(t, dir, "frontend.ts", "export function existing() {}\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")
	writeFile(t, dir, "backend.go", "package app\n\nfunc Existing() {}\n\nfunc BackendRisk() {}\n")
	writeFile(t, dir, "frontend.ts", "export function existing() {}\n\nexport function frontendRisk() {}\n")
	git(t, dir, "add", ".")

	fa := &subagentFake{}
	eng := engine.New(fa, gitcmd.New())
	res, err := eng.Review(stdctx.Background(), engine.Request{
		Mode:    0,
		RepoDir: dir,
		Gate:    "high",
		Subagents: engine.SubagentConfig{
			Mode:        "always",
			MaxParallel: 2,
			RequireAll:  true,
			Agents: []engine.SubagentSpec{
				{Name: "backend", IncludeGlobs: []string{"**/*.go"}, OperatorPrompt: "backend prompt"},
				{Name: "frontend", IncludeGlobs: []string{"**/*.ts"}, OperatorPrompt: "frontend prompt"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(res.Findings) != 2 {
		t.Fatalf("findings: got %d, want 2: %+v", len(res.Findings), res.Findings)
	}
	if got, _ := res.Stats["subagent_count"].(float64); got != 2 {
		t.Fatalf("subagent_count = %v, want 2", res.Stats["subagent_count"])
	}
	if res.Stats["subagents_degraded"] != false {
		t.Fatalf("subagents_degraded = %v, want false", res.Stats["subagents_degraded"])
	}
	if len(fa.calls) != 2 {
		t.Fatalf("agent calls = %d, want 2", len(fa.calls))
	}
	seen := map[string]bool{}
	for _, c := range fa.calls {
		seen[c.Instruction] = true
		if strings.Contains(c.Text, "BackendRisk()") && strings.Contains(c.Text, "frontendRisk()") {
			t.Fatalf("subagent received both partitions:\n%s", c.Text)
		}
	}
	if !containsInstruction(seen, `Subagent "backend"`) || !containsInstruction(seen, `Subagent "frontend"`) {
		t.Fatalf("missing scoped subagent instructions: %#v", seen)
	}
	if !strings.Contains(res.Walkthrough, "- backend: reviewed scoped files") {
		t.Fatalf("walkthrough missing bullet-prefixed subagent label:\n%s", res.Walkthrough)
	}
	if strings.Contains(res.Walkthrough, "backend: - reviewed scoped files") {
		t.Fatalf("walkthrough has awkward label-before-bullet format:\n%s", res.Walkthrough)
	}
}

func TestReviewSubagentsRedactsPartialFailureStats(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "backend.go", "package app\n\nfunc Existing() {}\n")
	writeFile(t, dir, "frontend.ts", "export function existing() {}\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")
	writeFile(t, dir, "backend.go", "package app\n\nfunc Existing() {}\n\nfunc BackendRisk() {}\n")
	writeFile(t, dir, "frontend.ts", "export function existing() {}\n\nexport function frontendRisk() {}\n")
	git(t, dir, "add", ".")

	fa := &subagentFake{failBackend: true}
	eng := engine.New(fa, gitcmd.New())
	res, err := eng.Review(stdctx.Background(), engine.Request{
		Mode:    0,
		RepoDir: dir,
		Gate:    "high",
		Subagents: engine.SubagentConfig{
			Mode:        "always",
			MaxParallel: 2,
			RequireAll:  false,
			Agents: []engine.SubagentSpec{
				{Name: "backend", IncludeGlobs: []string{"**/*.go"}},
				{Name: "frontend", IncludeGlobs: []string{"**/*.ts"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("findings: got %d, want 1: %+v", len(res.Findings), res.Findings)
	}
	got, _ := res.Stats["subagents_error"].(string)
	if strings.Contains(got, "sk-synthetic-secret-token") || !strings.Contains(got, "[redacted]") {
		t.Fatalf("subagents_error not redacted: %q", got)
	}
}

func TestReviewSubagentsMergesTrace(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "backend.go", "package app\n\nfunc Existing() {}\n")
	writeFile(t, dir, "frontend.ts", "export function existing() {}\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")
	writeFile(t, dir, "backend.go", "package app\n\nfunc Existing() {}\n\nfunc BackendRisk() {}\n")
	writeFile(t, dir, "frontend.ts", "export function existing() {}\n\nexport function frontendRisk() {}\n")
	git(t, dir, "add", ".")

	cs := &captureStore{}
	eng := engine.New(&subagentFake{}, gitcmd.New())
	eng.Store = cs
	if _, err := eng.Review(stdctx.Background(), engine.Request{
		Mode:    0,
		RepoDir: dir,
		Gate:    "high",
		Subagents: engine.SubagentConfig{
			Mode:       "always",
			RequireAll: true,
			Agents: []engine.SubagentSpec{
				{Name: "backend", IncludeGlobs: []string{"**/*.go"}},
				{Name: "frontend", IncludeGlobs: []string{"**/*.ts"}},
			},
		},
	}); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if !strings.Contains(cs.rec.RawPrompt, `Subagent "backend"`) || !strings.Contains(cs.rec.RawPrompt, `Subagent "frontend"`) {
		t.Fatalf("raw prompt missing subagent traces:\n%s", cs.rec.RawPrompt)
	}
	if len(cs.rec.Transcript) == 0 {
		t.Fatal("expected merged subagent tool transcript")
	}
	var turns []engine.TurnRecord
	if err := json.Unmarshal(cs.rec.Transcript, &turns); err != nil {
		t.Fatalf("transcript json: %v", err)
	}
	if !hasTruncatedToolResult(turns) {
		t.Fatalf("merged transcript did not preserve truncated subagent result: %+v", turns)
	}
}

func TestReviewSubagentsStreamsTraceSink(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "backend.go", "package app\n\nfunc Existing() {}\n")
	writeFile(t, dir, "frontend.ts", "export function existing() {}\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")
	writeFile(t, dir, "backend.go", "package app\n\nfunc Existing() {}\n\nfunc BackendRisk() {}\n")
	writeFile(t, dir, "frontend.ts", "export function existing() {}\n\nexport function frontendRisk() {}\n")
	git(t, dir, "add", ".")

	var mu sync.Mutex
	var steps []string
	var events []struct {
		step    string
		payload any
	}
	eng := engine.New(&subagentFake{}, gitcmd.New())
	if _, err := eng.Review(stdctx.Background(), engine.Request{
		Mode:    0,
		RepoDir: dir,
		Gate:    "high",
		TraceSink: func(step string, payload any) {
			mu.Lock()
			defer mu.Unlock()
			steps = append(steps, step)
			events = append(events, struct {
				step    string
				payload any
			}{step: step, payload: payload})
		},
		Subagents: engine.SubagentConfig{
			Mode:       "always",
			RequireAll: true,
			Agents: []engine.SubagentSpec{
				{Name: "backend", IncludeGlobs: []string{"**/*.go"}},
				{Name: "frontend", IncludeGlobs: []string{"**/*.ts"}},
			},
		},
	}); err != nil {
		t.Fatalf("Review: %v", err)
	}
	mu.Lock()
	got := countSteps(steps, "user_prompt")
	mu.Unlock()
	if got < 2 {
		t.Fatalf("user_prompt trace steps = %d, want at least 2: %#v", got, steps)
	}
	if !hasSubagentFinalResponse(events) {
		t.Fatalf("missing tagged subagent final_response event: %#v", events)
	}
	if !hasTopLevelFinalResponse(events) {
		t.Fatalf("missing terminal top-level final_response event: %#v", events)
	}
}

func TestReviewSubagentsAlwaysRunsSingleMatchingAgent(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "backend.go", "package app\n\nfunc Existing() {}\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")
	writeFile(t, dir, "backend.go", "package app\n\nfunc Existing() {}\n\nfunc BackendRisk() {}\n")
	git(t, dir, "add", ".")

	fa := &subagentFake{}
	eng := engine.New(fa, gitcmd.New())
	res, err := eng.Review(stdctx.Background(), engine.Request{
		Mode:    0,
		RepoDir: dir,
		Gate:    "high",
		Subagents: engine.SubagentConfig{
			Mode:       "always",
			RequireAll: true,
			Agents: []engine.SubagentSpec{
				{Name: "backend", IncludeGlobs: []string{"**/*.go"}, OperatorPrompt: "backend prompt"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("findings: got %d, want 1: %+v", len(res.Findings), res.Findings)
	}
	if got, _ := res.Stats["subagent_count"].(float64); got != 1 {
		t.Fatalf("subagent_count = %v, want 1", res.Stats["subagent_count"])
	}
	if len(fa.calls) != 1 {
		t.Fatalf("agent calls = %d, want 1", len(fa.calls))
	}
	if !strings.Contains(fa.calls[0].Instruction, `Subagent "backend"`) {
		t.Fatalf("instruction = %q, want backend subagent scope", fa.calls[0].Instruction)
	}
	if !strings.Contains(fa.calls[0].OperatorPrompt, "backend prompt") {
		t.Fatalf("operator prompt = %q, want backend prompt", fa.calls[0].OperatorPrompt)
	}
}

func containsInstruction(seen map[string]bool, needle string) bool {
	for s := range seen {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func countSteps(steps []string, want string) int {
	n := 0
	for _, step := range steps {
		if step == want {
			n++
		}
	}
	return n
}

func hasTruncatedToolResult(turns []engine.TurnRecord) bool {
	for _, tr := range turns {
		if tr.Tool == "grep" && tr.Args == "Risk" && tr.ResultTruncated && strings.Contains(tr.Result, "truncated tool result") {
			return true
		}
	}
	return false
}

func hasSubagentFinalResponse(events []struct {
	step    string
	payload any
}) bool {
	for _, event := range events {
		if event.step != "final_response" {
			continue
		}
		payload, ok := event.payload.(map[string]any)
		if !ok {
			continue
		}
		if payload["source"] == "subagent" && payload["subagent"] != "" && payload["review_terminal"] == false {
			return true
		}
	}
	return false
}

func hasTopLevelFinalResponse(events []struct {
	step    string
	payload any
}) bool {
	for _, event := range events {
		if event.step != "final_response" {
			continue
		}
		if _, ok := event.payload.(map[string]any); !ok {
			return true
		}
	}
	return false
}
