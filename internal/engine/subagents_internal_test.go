package engine

import (
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine/diff"
)

func TestPlanSubagentsTracksDeletedFilesByOriginalPath(t *testing.T) {
	selected := []diff.Diff{
		{OldPath: "backend/removed.go", NewPath: "/dev/null", IsDeleted: true},
		{OldPath: "frontend/removed.ts", NewPath: "/dev/null", IsDeleted: true},
	}
	plans := planSubagents(SubagentConfig{Agents: []SubagentSpec{{Name: "backend", IncludeGlobs: []string{"backend/**"}}}}, selected)
	if len(plans) != 2 || len(plans[0].files) != 1 || len(plans[1].files) != 1 {
		t.Fatalf("deleted files must remain in separate plans: %+v", plans)
	}
	if plans[0].files[0].ReviewPath() != "backend/removed.go" || plans[1].files[0].ReviewPath() != "frontend/removed.ts" {
		t.Fatalf("unexpected deleted-file assignments: %+v", plans)
	}
}

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
