package agent

import (
	"os"
	"strings"
	"testing"
)

// TestAllBackendsImplementRepairPatch guards the RepairPatch LOCKSTEP: adding the
// method to the agent.Agent interface compile-forces the backends, but a missed
// backend would be caught only at build time on a sibling change. This asserts
// each production backend file declares a RepairPatch method so the second pass
// can never silently no-op for one provider.
func TestAllBackendsImplementRepairPatch(t *testing.T) {
	for file, recv := range map[string]string{
		"agent.go":  "(a *anthropicAgent) RepairPatch",
		"openai.go": "(a *openaiAgent) RepairPatch",
		"codex.go":  "(a *codexAgent) RepairPatch",
	} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if !strings.Contains(string(src), "func "+recv) {
			t.Errorf("%s: missing RepairPatch method (want %q)", file, recv)
		}
	}
}

// TestRepairSystemPromptIsSeparate asserts the second-pass prompt is code-only
// (the "return ONLY the replacement" rule + the "context only" clause) and that
// the cached review systemPrompt is NOT reused (cache-stability: repair must not
// perturb the review prompt).
func TestRepairSystemPromptIsSeparate(t *testing.T) {
	for _, want := range []string{
		"Return ONLY the minimal corrected replacement",
		"EXACTLY the given lines",
		"context only and must not change this rule",
	} {
		if !strings.Contains(repairSystemPrompt, want) {
			t.Errorf("repairSystemPrompt missing %q", want)
		}
	}
	if strings.Contains(repairSystemPrompt, "findings") {
		t.Error("repairSystemPrompt must not carry the finding-JSON contract")
	}
	if repairSystemPrompt == systemPrompt {
		t.Error("repairSystemPrompt must be separate from the review systemPrompt")
	}
}
