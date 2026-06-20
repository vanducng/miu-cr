package context

import (
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine/diff"
)

func sampleDiffs() []diff.Diff {
	return []diff.Diff{
		{
			NewPath: "main.go",
			Diff: "diff --git a/main.go b/main.go\n" +
				"@@ -1,3 +1,4 @@\n" +
				" package main\n" +
				"+import \"fmt\"\n" +
				" func main() {\n" +
				"-\tprintln(\"x\")\n" +
				"+\tfmt.Println(\"x\")\n" +
				" }\n",
			NewFileContent: "package main\nimport \"fmt\"\nfunc main() {\n\tfmt.Println(\"x\")\n}\n",
		},
	}
}

func TestAssembleContext_Deterministic(t *testing.T) {
	d := sampleDiffs()
	a := AssembleContext(d, AssembleOptions{ExpandWindow: 1})
	b := AssembleContext(d, AssembleOptions{ExpandWindow: 1})
	if a.Text != b.Text {
		t.Fatalf("non-deterministic output")
	}
	if !strings.Contains(a.Text, "=== File: main.go ===") {
		t.Fatalf("missing file header: %q", a.Text)
	}
	if !strings.Contains(a.Text, "--- New content ---") {
		t.Fatalf("missing new content window")
	}
	if a.Stats["truncation_level"] != LevelFull {
		t.Fatalf("expected full level, got %v", a.Stats["truncation_level"])
	}
}

func TestAssembleContext_TruncationLadder(t *testing.T) {
	d := sampleDiffs()

	full := AssembleContext(d, AssembleOptions{ExpandWindow: 1})
	fullTokens := full.Stats["est_tokens"].(int)

	// Budget below full but above hunks-only -> hunks_only.
	hunksOnly := render(d, 0, false)
	hunksTokens := estTokens(hunksOnly)
	if hunksTokens >= fullTokens {
		t.Fatalf("test fixture: hunks-only must be smaller than full")
	}
	a := AssembleContext(d, AssembleOptions{ExpandWindow: 1, TokenBudget: hunksTokens})
	if a.Stats["truncation_level"] != LevelHunksOnly {
		t.Fatalf("expected hunks_only, got %v", a.Stats["truncation_level"])
	}
	if strings.Contains(a.Text, "--- New content ---") {
		t.Fatalf("hunks_only must drop expansion windows")
	}

	// Tiny budget -> filenames_only.
	c := AssembleContext(d, AssembleOptions{ExpandWindow: 1, TokenBudget: 1})
	if c.Stats["truncation_level"] != LevelFilenamesOnly {
		t.Fatalf("expected filenames_only, got %v", c.Stats["truncation_level"])
	}
	if !strings.Contains(c.Text, "main.go") || strings.Contains(c.Text, "--- Diff ---") {
		t.Fatalf("filenames_only must list names without diffs: %q", c.Text)
	}
}

func TestAssembleContext_StaysUnderBudgetWhenPossible(t *testing.T) {
	d := sampleDiffs()
	full := AssembleContext(d, AssembleOptions{ExpandWindow: 1})
	tok := full.Stats["est_tokens"].(int)
	a := AssembleContext(d, AssembleOptions{ExpandWindow: 1, TokenBudget: tok + 100})
	if a.Stats["truncation_level"] != LevelFull {
		t.Fatalf("under-budget should stay full, got %v", a.Stats["truncation_level"])
	}
}
