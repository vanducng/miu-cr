package agent

import (
	stdctx "context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

// runTool records each dispatch into a non-nil Trace (turn, tool, args), with
// args holding only the path/pattern label — never the file content or a token.
func TestTrace_RecordsToolDispatches(t *testing.T) {
	repo, sha := runToolRepo(t)
	tr := &engine.ReviewTrace{}
	rc := Context{RepoDir: repo, Rev: sha, Runner: gitcmd.New(), Trace: tr}
	ctx := stdctx.Background()

	runTool(ctx, rc, 0, "grep", json.RawMessage(`{"pattern":"func Bar"}`))
	runTool(ctx, rc, 1, "file_read", json.RawMessage(`{"file":"main.go","start":1,"end":2}`))

	if len(tr.Turns) != 2 {
		t.Fatalf("want 2 recorded turns, got %d: %+v", len(tr.Turns), tr.Turns)
	}
	if tr.Turns[0] != (engine.TurnRecord{Turn: 0, Tool: "grep", Args: "func Bar"}) {
		t.Errorf("grep turn: %+v", tr.Turns[0])
	}
	if tr.Turns[1] != (engine.TurnRecord{Turn: 1, Tool: "file_read", Args: "main.go:1-2"}) {
		t.Errorf("file_read turn: %+v", tr.Turns[1])
	}
	// The transcript holds only the label, never the read file content.
	if strings.Contains(tr.Turns[1].Args, "package main") {
		t.Errorf("file content leaked into transcript: %q", tr.Turns[1].Args)
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
}
