package agent

import "testing"

// m6Prompt is the M6 USER-turn shape (no rules, no semantic block): the preamble
// followed directly by the diff. Captured as a literal so the parity assertion is
// against the real M6 form, NOT a post-plumbing tautology.
func m6Prompt(diff string) string {
	return "Review the following change. Report findings as specified.\n\n" + diff
}

func TestBuildUserPromptM6ParityEmpty(t *testing.T) {
	diff := "=== File: a.go ===\n+func boom() {}\n"
	got := BuildUserPrompt(PromptParts{Diff: diff})
	if want := m6Prompt(diff); got != want {
		t.Fatalf("empty parts diverged from M6:\n got=%q\nwant=%q", got, want)
	}
}

func TestBuildUserPromptM6ParityEmptySemantic(t *testing.T) {
	diff := "=== File: a.go ===\n+x := 1\n"
	// Whitespace-only SemanticContext must collapse to the M6 prompt.
	got := BuildUserPrompt(PromptParts{Diff: diff, SemanticContext: "   \n\t "})
	if want := m6Prompt(diff); got != want {
		t.Fatalf("whitespace SemanticContext changed the prompt:\n got=%q\nwant=%q", got, want)
	}
}

func TestBuildUserPromptInjectsSemanticBlock(t *testing.T) {
	diff := "=== File: a.go ===\n+y := 2\n"
	got := BuildUserPrompt(PromptParts{Diff: diff, SemanticContext: "- [bug] off-by-one"})
	if got == m6Prompt(diff) {
		t.Fatal("non-empty SemanticContext did not change the prompt")
	}
	if !contains(got, semanticAdvisoryHeader) {
		t.Fatalf("advisory header missing:\n%s", got)
	}
	if !contains(got, "- [bug] off-by-one") {
		t.Fatalf("advisory body missing:\n%s", got)
	}
	if !contains(got, diff) {
		t.Fatalf("diff missing:\n%s", got)
	}
}

func TestBuildUserPromptRulesAndSemanticOrder(t *testing.T) {
	diff := "DIFF"
	got := BuildUserPrompt(PromptParts{Rules: "RULES", SemanticContext: "SEM", Diff: diff})
	ri := indexOf(got, "RULES")
	si := indexOf(got, "SEM")
	di := indexOf(got, diff)
	if !(ri >= 0 && si >= 0 && di >= 0 && ri < si && si < di) {
		t.Fatalf("expected rules<semantic<diff order, got rules=%d sem=%d diff=%d:\n%s", ri, si, di, got)
	}
}

func contains(s, sub string) bool { return indexOf(s, sub) >= 0 }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
