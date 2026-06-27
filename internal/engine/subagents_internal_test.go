package engine

import (
	"strings"
	"testing"
)

func TestSubagentDiffBudgetSubtractsSharedContext(t *testing.T) {
	req := Request{TokenBudget: 100, RulesTokenBudget: 20}
	shared := reviewSharedContext{
		projectContext:  strings.Repeat("p", 120),
		semanticContext: strings.Repeat("s", 80),
		relatedContext:  strings.Repeat("r", 40),
	}
	got := subagentDiffBudget(req, shared)
	if got != 20 {
		t.Fatalf("subagentDiffBudget = %d, want 20", got)
	}
}

func TestSubagentDiffBudgetDisabledStaysDisabled(t *testing.T) {
	got := subagentDiffBudget(Request{TokenBudget: 0, RulesTokenBudget: 4096}, reviewSharedContext{
		projectContext: strings.Repeat("p", 400),
	})
	if got != 0 {
		t.Fatalf("subagentDiffBudget disabled = %d, want 0", got)
	}
}
