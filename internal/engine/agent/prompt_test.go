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

func TestBuildUserPromptDiagramOffByteIdentical(t *testing.T) {
	diff := "=== File: a.go ===\n+func boom() {}\n"
	// WantDiagram off must be byte-identical to the diagram-absent prompt (the
	// prompt cache hinges on OFF never altering the USER turn).
	off := BuildUserPrompt(PromptParts{Diff: diff})
	explicitOff := BuildUserPrompt(PromptParts{Diff: diff, WantDiagram: false})
	if off != explicitOff {
		t.Fatalf("WantDiagram=false changed the prompt:\n a=%q\n b=%q", off, explicitOff)
	}
	if want := m6Prompt(diff); off != want {
		t.Fatalf("diagram-off prompt diverged from M6:\n got=%q\nwant=%q", off, want)
	}
}

func TestBuildUserPromptDiagramOnInjectsInstruction(t *testing.T) {
	diff := "=== File: a.go ===\n+func boom() {}\n"
	got := BuildUserPrompt(PromptParts{Diff: diff, WantDiagram: true})
	if got == m6Prompt(diff) {
		t.Fatal("WantDiagram=true did not change the prompt")
	}
	if !contains(got, diagramInstruction) {
		t.Fatalf("diagram instruction missing:\n%s", got)
	}
	// The instruction must precede the diff (rides the USER turn before context).
	if !(indexOf(got, diagramInstruction) < indexOf(got, diff)) {
		t.Fatalf("diagram instruction must lead the diff:\n%s", got)
	}
}

func TestSystemPromptConventionGuidance(t *testing.T) {
	// The convention cross-reference guidance must live in the cached systemPrompt
	// (so it's part of the trusted contract, not injectable USER prose).
	for _, want := range []string{
		"INCONSISTENT with an established pattern",
		"differs from <name>",
		"never invent one",
	} {
		if !contains(systemPrompt, want) {
			t.Fatalf("systemPrompt missing convention guidance %q", want)
		}
	}
}

func TestConventionCitationRidesRationale(t *testing.T) {
	// A rationale citing a sibling rides the existing rationale field verbatim —
	// no new finding field, contract unchanged.
	const cite = `differs from mapWriteError, which sets the wrapped sql code`
	body := `{"findings":[{"file":"a.go","existing_code":"return err","severity":"low","category":"maintainability","rationale":"` + cite + `"}]}`
	out, ok := parseFindings(body)
	if !ok {
		t.Fatalf("parseFindings failed on convention finding")
	}
	if len(out.Findings) != 1 {
		t.Fatalf("want 1 finding, got %d", len(out.Findings))
	}
	if out.Findings[0].Rationale != cite {
		t.Fatalf("rationale not preserved verbatim:\n got=%q\nwant=%q", out.Findings[0].Rationale, cite)
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
