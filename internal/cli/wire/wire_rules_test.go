package wire

import (
	stdctx "context"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/agent"
)

// captureAgent records the agent.Context it was called with so we can assert the
// adapter forwards every field, Rules in particular, whose copy is easy to
// forget and would silently drop all project rules.
type captureAgent struct {
	got       agent.Context
	gotRepair agent.RepairRequest
}

func (c *captureAgent) Review(_ stdctx.Context, rc agent.Context) (engine.ReviewOutput, error) {
	c.got = rc
	return engine.ReviewOutput{}, nil
}

func (c *captureAgent) RepairPatch(_ stdctx.Context, rr agent.RepairRequest) (string, engine.Usage, error) {
	c.gotRepair = rr
	return "", engine.Usage{}, nil
}

func TestAgentAdapterForwardsRules(t *testing.T) {
	ca := &captureAgent{}
	a := agentAdapter{inner: ca}
	_, err := a.Review(stdctx.Background(), engine.AgentContext{
		Text:           "diff text",
		Rules:          "RULES_SECTION_MARKER",
		ProjectContext: "PROJECT_CONTEXT_MARKER",
		RelatedContext: "RELATED_CONTEXT_MARKER",
		Instruction:    "INSTRUCTION_MARKER",
		Conversation:   "CONVERSATION_MARKER",
		RepoDir:        "/repo",
		Rev:            "abc",
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if ca.got.Rules != "RULES_SECTION_MARKER" {
		t.Errorf("adapter dropped Rules: got %q", ca.got.Rules)
	}
	if ca.got.ProjectContext != "PROJECT_CONTEXT_MARKER" {
		t.Errorf("adapter dropped ProjectContext: got %q", ca.got.ProjectContext)
	}
	if ca.got.RelatedContext != "RELATED_CONTEXT_MARKER" {
		t.Errorf("adapter dropped RelatedContext: got %q", ca.got.RelatedContext)
	}
	if ca.got.Instruction != "INSTRUCTION_MARKER" {
		t.Errorf("adapter dropped Instruction: got %q", ca.got.Instruction)
	}
	if ca.got.Conversation != "CONVERSATION_MARKER" {
		t.Errorf("adapter dropped Conversation: got %q", ca.got.Conversation)
	}
	if ca.got.Text != "diff text" || ca.got.RepoDir != "/repo" || ca.got.Rev != "abc" {
		t.Errorf("adapter mangled other fields: %+v", ca.got)
	}
}

func TestWantConversationDropsOnFork(t *testing.T) {
	for _, tc := range []struct {
		requested, isFork, want bool
	}{
		{true, false, true},
		{true, true, false}, // dropped on fork PRs (Untrusted participant text)
		{false, false, false},
		{false, true, false},
	} {
		if got := wantConversation(tc.requested, tc.isFork); got != tc.want {
			t.Errorf("wantConversation(requested=%v, fork=%v)=%v, want %v", tc.requested, tc.isFork, got, tc.want)
		}
	}
}

func TestWantProjectContextDropsOnFork(t *testing.T) {
	for _, tc := range []struct {
		requested, isFork, want bool
	}{
		{true, false, true},
		{true, true, false},
		{false, false, false},
		{false, true, false},
	} {
		if got := wantProjectContext(tc.requested, tc.isFork); got != tc.want {
			t.Errorf("wantProjectContext(requested=%v, fork=%v)=%v, want %v", tc.requested, tc.isFork, got, tc.want)
		}
	}
}

func TestContextHopsForPRDropsOnFork(t *testing.T) {
	if got := contextHopsForPR(2, false); got != 2 {
		t.Fatalf("non-fork ContextHops = %d, want 2", got)
	}
	if got := contextHopsForPR(2, true); got != 0 {
		t.Fatalf("fork ContextHops = %d, want 0", got)
	}
	if !contextHopsAutoForPR(true, false) {
		t.Fatal("non-fork auto ContextHops should stay enabled")
	}
	if contextHopsAutoForPR(true, true) {
		t.Fatal("fork auto ContextHops should be disabled")
	}
}
