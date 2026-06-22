package agent

import (
	stdctx "context"
	"strings"
	"testing"
)

// The agent must emit a per-turn "thinking…" milestone and a tool-dispatch
// milestone (here "→ grep needle") to a non-nil Progress sink, with zero network.
func TestAgentProgressEmitsTurnAndToolMilestones(t *testing.T) {
	fc := &fakeAnthropic{responses: []string{
		toolUseMessage("tu_1", "grep", map[string]any{"pattern": "needle"}),
		textMessage(`{"findings":[]}`),
	}}
	a := &anthropicAgent{client: fc, model: "claude-test"}

	var msgs []string
	_, err := a.Review(stdctx.Background(), Context{
		Text: "ctx", RepoDir: t.TempDir(),
		Progress: func(m string) { msgs = append(msgs, m) },
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}

	var sawTurn, sawTool bool
	for _, m := range msgs {
		if strings.HasPrefix(m, "thinking… (turn ") {
			sawTurn = true
		}
		if m == "→ grep needle" {
			sawTool = true
		}
	}
	if !sawTurn {
		t.Errorf("expected a per-turn milestone, got %v", msgs)
	}
	if !sawTool {
		t.Errorf("expected a \"→ grep needle\" tool milestone, got %v", msgs)
	}
}

// A nil Progress sink must run the same tool/parse loop without panicking.
func TestAgentNilProgressIsSilentNoOp(t *testing.T) {
	fc := &fakeAnthropic{responses: []string{
		toolUseMessage("tu_1", "grep", map[string]any{"pattern": "needle"}),
		textMessage(`{"findings":[]}`),
	}}
	a := &anthropicAgent{client: fc, model: "claude-test"}
	if _, err := a.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir()}); err != nil {
		t.Fatalf("nil Progress must not error: %v", err)
	}
}

// fileReadLabel renders "path:start-end" only when a range is set, never leaking
// anything beyond the path + line numbers it is given.
func TestFileReadLabel(t *testing.T) {
	if got := fileReadLabel(fileReadArgs{File: "a.go"}); got != "a.go" {
		t.Errorf("no-range label: want a.go, got %q", got)
	}
	if got := fileReadLabel(fileReadArgs{File: "a.go", Start: 10, End: 20}); got != "a.go:10-20" {
		t.Errorf("range label: want a.go:10-20, got %q", got)
	}
}
