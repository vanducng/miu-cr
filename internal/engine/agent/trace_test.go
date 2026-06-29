package agent

import (
	stdctx "context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

// Every backend's Review must capture a NON-EMPTY system prompt + its
// model/provider into the threaded Trace, the headline bug fix. Without this an
// openai/codex review would persist an empty SystemPrompt.
func TestTrace_AnthropicCapturesSystemPromptAndModel(t *testing.T) {
	a := &anthropicAgent{
		client: &fakeAnthropic{responses: []string{textMessage(`{"findings":[]}`)}},
		model:  "claude-test",
	}
	tr := &engine.ReviewTrace{}
	if _, err := a.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir(), Trace: tr}); err != nil {
		t.Fatalf("Review: %v", err)
	}
	assertCapturedSystemPrompt(t, tr, "anthropic", "claude-test")
}

func TestTrace_OpenAICapturesSystemPromptAndModel(t *testing.T) {
	a := &openaiAgent{
		client: &fakeOpenAI{responses: []string{textCompletion(`{"findings":[]}`)}},
		model:  "gpt-test",
	}
	tr := &engine.ReviewTrace{}
	if _, err := a.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir(), Trace: tr}); err != nil {
		t.Fatalf("Review: %v", err)
	}
	assertCapturedSystemPrompt(t, tr, "openai", "gpt-test")
}

func TestTrace_CodexCapturesSystemPromptAndModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, codexMessageResp(`{"findings":[]}`))
	}))
	defer srv.Close()
	a := newTestCodexAgent(t, srv)
	tr := &engine.ReviewTrace{}
	if _, err := a.Review(stdctx.Background(), Context{Text: "diff", Trace: tr}); err != nil {
		t.Fatalf("Review: %v", err)
	}
	assertCapturedSystemPrompt(t, tr, "codex", "gpt-test")
}

func assertCapturedSystemPrompt(t *testing.T, tr *engine.ReviewTrace, wantProvider, wantModel string) {
	t.Helper()
	if tr.SystemPrompt != systemPrompt {
		t.Errorf("system prompt not captured: got %q", tr.SystemPrompt)
	}
	if tr.Provider != wantProvider || tr.Model != wantModel {
		t.Errorf("model/provider: got %q/%q, want %q/%q", tr.Provider, tr.Model, wantProvider, wantModel)
	}
	if tr.UserPrompt == "" {
		t.Error("user prompt not captured")
	}
}

// runTool records each dispatch and bounded result into a non-nil Trace.
func TestTrace_RecordsToolDispatches(t *testing.T) {
	repo, sha := runToolRepo(t)
	tr := &engine.ReviewTrace{}
	rc := Context{RepoDir: repo, Rev: sha, Runner: gitcmd.New(), Trace: tr}
	ctx := stdctx.Background()

	runTool(ctx, rc, 0, "grep", json.RawMessage(`{"pattern":"func Bar"}`))
	runTool(ctx, rc, 1, "file_read", json.RawMessage(`{"file":"main.go","start":1,"end":2}`))
	runTool(ctx, rc, 2, "symbol_context", json.RawMessage(`{"relation":"document_symbols","file":"main.go"}`))

	if len(tr.Turns) != 3 {
		t.Fatalf("want 3 recorded turns, got %d: %+v", len(tr.Turns), tr.Turns)
	}
	if tr.Turns[0].Turn != 0 || tr.Turns[0].Tool != "grep" || tr.Turns[0].Args != "func Bar" {
		t.Errorf("grep turn: %+v", tr.Turns[0])
	}
	if tr.Turns[1].Turn != 1 || tr.Turns[1].Tool != "file_read" || tr.Turns[1].Args != "main.go:1-2" {
		t.Errorf("file_read turn: %+v", tr.Turns[1])
	}
	if !strings.Contains(tr.Turns[0].Result, "File: main.go") || !strings.Contains(tr.Turns[1].Result, "package main") {
		t.Errorf("tool results not captured: %+v", tr.Turns)
	}
	if tr.Turns[2].Tool != "symbol_context" || !strings.Contains(tr.Turns[2].Result, "Document symbols for main.go") {
		t.Errorf("symbol_context result not captured: %+v", tr.Turns[2])
	}
	if strings.Contains(tr.Turns[1].Args, "package main") {
		t.Errorf("file content leaked into args label: %q", tr.Turns[1].Args)
	}
}

// Prompt is recorded once; the final response is captured verbatim.
func TestTrace_PromptAndResponse(t *testing.T) {
	tr := &engine.ReviewTrace{}
	tr.SetPrompt("first prompt")
	tr.SetPrompt("second prompt") // ignored: first non-empty wins
	tr.SetFinalResponse(`{"findings":[]}`)

	if tr.UserPrompt != "first prompt" {
		t.Errorf("prompt: want first prompt, got %q", tr.UserPrompt)
	}
	if tr.FinalResponse != `{"findings":[]}` {
		t.Errorf("response not captured: %q", tr.FinalResponse)
	}
}

// A nil Trace makes every recorder a no-op and runTool still works.
func TestTrace_NilIsNoOp(t *testing.T) {
	repo, sha := runToolRepo(t)
	rc := Context{RepoDir: repo, Rev: sha, Runner: gitcmd.New(), Trace: nil}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil Trace panicked: %v", r)
		}
	}()
	out, isErr := runTool(stdctx.Background(), rc, 0, "grep", json.RawMessage(`{"pattern":"func Bar"}`))
	if isErr || !strings.Contains(out, "Bar") {
		t.Errorf("nil-trace grep: got %q isErr=%v", out, isErr)
	}

	var tr *engine.ReviewTrace
	tr.SetPrompt("x")
	tr.SetFinalResponse("y")
	tr.RecordTool(0, "grep", "z") // all no-ops, no panic
	tr.RecordToolResult(0, "grep", "z", "out", false)
}
