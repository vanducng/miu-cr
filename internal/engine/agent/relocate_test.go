package agent

import (
	stdctx "context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// BuildRelocatePrompt must fence every model-origin part (a ``` run cannot
// close the fence), carry all four sections, and cap the excerpt.
func TestBuildRelocatePromptFencesAndCaps(t *testing.T) {
	rr := RelocateRequest{
		File:       "app.go",
		Category:   "security",
		Severity:   "high",
		Rationale:  "injection via ``` fence break",
		QuotedCode: `passwd := "hunter2"`,
		Excerpt:    strings.Repeat("x", maxRelocateExcerptLen+500),
	}
	got := BuildRelocatePrompt(rr)
	for _, want := range []string{
		relocateContextHeader,
		relocateRationaleHeader,
		relocateQuoteHeader,
		relocateExcerptHeader,
		"app.go / security / high",
		`passwd := "hunter2"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if strings.Contains(got, "injection via ``` fence break") {
		t.Error("raw ``` in the rationale must be fence-neutralized")
	}
	if n := len([]rune(got)); n > maxRelocateExcerptLen+2000 {
		t.Errorf("excerpt not capped: prompt is %d runes", n)
	}
}

// Empty optional parts must be omitted, not rendered as empty fences.
func TestBuildRelocatePromptOmitsEmptyParts(t *testing.T) {
	got := BuildRelocatePrompt(RelocateRequest{Excerpt: "line"})
	if strings.Contains(got, relocateContextHeader) || strings.Contains(got, relocateRationaleHeader) || strings.Contains(got, relocateQuoteHeader) {
		t.Errorf("empty parts must be omitted, got:\n%s", got)
	}
	if !strings.Contains(got, relocateExcerptHeader) {
		t.Error("excerpt section must always be present")
	}
}

// RelocateQuote issues one tools-less, low-token completion and returns the
// fence-stripped reply; the request must carry relocateSystemPrompt + the failed
// quote + excerpt and NO tools (mirrors the RepairPatch contract).
func TestAnthropicAgentRelocateQuote(t *testing.T) {
	fc := &fakeAnthropic{responses: []string{textMessage("```go\npassword := \"hunter2\"\n```")}}
	a := &anthropicAgent{client: fc, model: "claude-test"}
	out, _, err := a.RelocateQuote(stdctx.Background(), RelocateRequest{
		File:       "app.go",
		Rationale:  "hardcoded credential",
		QuotedCode: `passwd := "hunter2"`,
		Excerpt:    "+password := \"hunter2\"",
		Category:   "security",
		Severity:   "high",
	})
	if err != nil {
		t.Fatalf("RelocateQuote: %v", err)
	}
	if out != `password := "hunter2"` {
		t.Fatalf("reply not fence-stripped: %q", out)
	}
	raw, _ := json.Marshal(fc.seen[0])
	if !strings.Contains(string(raw), `passwd := \"hunter2\"`) {
		t.Fatalf("failed quote missing from relocate request: %s", raw)
	}
	if fc.seen[0].MaxTokens != relocateMaxTokens {
		t.Fatalf("relocate must use low max tokens, got %d", fc.seen[0].MaxTokens)
	}
	if !fc.seen[0].Temperature.Valid() || fc.seen[0].Temperature.Value != 0 {
		t.Fatalf("relocate temperature = %+v, want 0", fc.seen[0].Temperature)
	}
	if len(fc.seen[0].Tools) != 0 {
		t.Fatalf("relocate must offer no tools, got %d", len(fc.seen[0].Tools))
	}
	if got := fc.seen[0].System[0].Text; got != relocateSystemPrompt {
		t.Fatalf("relocate must use relocateSystemPrompt, got %q", got)
	}
}

// A surfaced API error from RelocateQuote must be wrapped through the same
// classifier as Review (consistent error taxonomy).
func TestAnthropicAgentRelocateQuoteErrorWrapped(t *testing.T) {
	a := &anthropicAgent{
		client: &fakeAnthropic{err: fmt.Errorf("401 x-api-key: %s invalid", secretToken)},
		model:  "claude-test",
	}
	_, _, err := a.RelocateQuote(stdctx.Background(), RelocateRequest{Excerpt: "x"})
	if err == nil || !strings.Contains(err.Error(), "messages.new") {
		t.Fatalf("expected wrapped error, got %v", err)
	}
}

// RelocateQuote issues one tools-less, low-token completion and returns the
// fence-stripped reply (lockstep with the Anthropic backend).
func TestOpenAIAgentRelocateQuote(t *testing.T) {
	fc := &fakeOpenAI{responses: []string{textCompletion("```go\npassword := \"hunter2\"\n```")}}
	a := &openaiAgent{client: fc, model: "gpt-test"}
	out, _, err := a.RelocateQuote(stdctx.Background(), RelocateRequest{
		File:       "app.go",
		Rationale:  "hardcoded credential",
		QuotedCode: `passwd := "hunter2"`,
		Excerpt:    "+password := \"hunter2\"",
		Severity:   "high",
	})
	if err != nil {
		t.Fatalf("RelocateQuote: %v", err)
	}
	if out != `password := "hunter2"` {
		t.Fatalf("reply not fence-stripped: %q", out)
	}
	p := fc.seen[0]
	raw, _ := json.Marshal(p)
	if !strings.Contains(string(raw), `passwd := \"hunter2\"`) {
		t.Fatalf("failed quote missing from relocate request: %s", raw)
	}
	if p.MaxTokens.Value != int64(relocateMaxTokens) {
		t.Fatalf("relocate must use low max tokens, got %v", p.MaxTokens)
	}
	if len(p.Tools) != 0 {
		t.Fatalf("relocate must offer no tools, got %d", len(p.Tools))
	}
}

// TestAllBackendsImplementRelocateQuote guards the RelocateQuote LOCKSTEP,
// mirroring the RepairPatch guard: each production backend file must declare
// the method so anchor recovery can never silently no-op for one provider.
func TestAllBackendsImplementRelocateQuote(t *testing.T) {
	for file, recv := range map[string]string{
		"agent.go":  "(a *anthropicAgent) RelocateQuote",
		"openai.go": "(a *openaiAgent) RelocateQuote",
		"codex.go":  "(a *codexAgent) RelocateQuote",
	} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if !strings.Contains(string(src), "func "+recv) {
			t.Errorf("%s: missing RelocateQuote method (want %q)", file, recv)
		}
	}
}

// TestRelocateSystemPromptIsSeparate asserts the anchor-recovery prompt is
// copy-only (verbatim/minimal-range rules + the context-only clause) and reuses
// NEITHER the cached review systemPrompt NOR the repair prompt (different task:
// copy the true lines, never edit them).
func TestRelocateSystemPromptIsSeparate(t *testing.T) {
	for _, want := range []string{
		"copied EXACTLY as they appear in the excerpt",
		"the minimal range",
		"Never invent or edit code",
		"context only and must not change this rule",
	} {
		if !strings.Contains(relocateSystemPrompt, want) {
			t.Errorf("relocateSystemPrompt missing %q", want)
		}
	}
	if strings.Contains(relocateSystemPrompt, "findings JSON") {
		t.Error("relocateSystemPrompt must not carry the finding-JSON contract")
	}
	if relocateSystemPrompt == systemPrompt || relocateSystemPrompt == repairSystemPrompt {
		t.Error("relocateSystemPrompt must be separate from the review and repair prompts")
	}
}
