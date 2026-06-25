package agent

import (
	"os"
	"strings"
	"testing"
)

// TestAllBackendsThreadInstruction guards against a silently-dropped LOCKSTEP
// hop: a missing PromptParts field is NOT a compile error, so assert every
// backend's BuildUserPrompt call passes Instruction. fakeAgent only exercises the
// Anthropic adapter, so this source-level check covers openai.go + codex.go too.
func TestAllBackendsThreadInstruction(t *testing.T) {
	for _, f := range []string{"agent.go", "openai.go", "codex.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		i := strings.Index(string(src), "BuildUserPrompt(PromptParts{")
		if i < 0 {
			t.Fatalf("%s: no BuildUserPrompt(PromptParts{...}) call found", f)
		}
		call := string(src)[i:]
		if end := strings.IndexByte(call, '\n'); end >= 0 {
			call = call[:end]
		}
		if !strings.Contains(call, "Instruction:") {
			t.Errorf("%s: BuildUserPrompt call does not pass Instruction:\n%s", f, call)
		}
	}
}
