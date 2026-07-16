package engine

import (
	stdctx "context"
	"fmt"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine/diff"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

// callerRepo: ChangedFunc is touched by the hunk (line 4 inside its span),
// UntouchedFunc is not. lib/use.go calls ChangedFunc; app/second.go (also
// changed) calls it too; lib2/alt.go redefines the same name.
func callerRepo(t *testing.T) (string, string) {
	t.Helper()
	return refDefsRepo(t, map[string]string{
		"app/main.go":   "package app\n\nfunc ChangedFunc() int {\n\treturn 2\n}\n\nfunc UntouchedFunc() int {\n\treturn 1\n}\n",
		"app/second.go": "package app\n\nfunc SecondCaller() int {\n\treturn ChangedFunc()\n}\n",
		"lib/use.go":    "package lib\n\nfunc UseIt() int {\n\treturn ChangedFunc()\n}\n\nvar fn = ChangedFunc\n\nfunc MyChangedFunc() int { return UntouchedFunc() }\n",
		"lib2/alt.go":   "package lib2\n\nfunc ChangedFunc() int {\n\treturn 9\n}\n",
	})
}

func callerSelected() []diff.Diff {
	return []diff.Diff{
		{NewPath: "app/main.go", Diff: "@@ -4,1 +4,1 @@\n-\treturn 1\n+\treturn 2\n"},
		{NewPath: "app/second.go", Diff: "@@ -4,1 +4,1 @@\n-\treturn 0\n+\treturn ChangedFunc()\n"},
	}
}

func TestCallerContextResolvesChangedSymbolAndFilters(t *testing.T) {
	dir, rev := callerRepo(t)
	out := buildCallerContext(stdctx.Background(), dir, callerSelected(), rev, gitcmd.New(), refDefsIndex(dir, rev))

	if !strings.HasPrefix(out, strings.TrimRight(callerContextHeader, "\n")) {
		t.Fatalf("missing header: %q", out)
	}
	if got := strings.Count(out, "- ChangedFunc ←"); got != 1 {
		t.Fatalf("want exactly 1 call site for ChangedFunc, got %d: %q", got, out)
	}
	if !strings.Contains(out, "- ChangedFunc ← lib/use.go:4: return ChangedFunc()") {
		t.Fatalf("call site in unchanged file missing: %q", out)
	}
	if strings.Contains(out, "app/second.go") {
		t.Fatalf("call sites in changed files must be excluded: %q", out)
	}
	if strings.Contains(out, "lib2/alt.go") {
		t.Fatalf("definition lines must be excluded: %q", out)
	}
	if strings.Contains(out, "lib/use.go:7") || strings.Contains(out, "var fn") {
		t.Fatalf("name without a following '(' must be excluded: %q", out)
	}
	if strings.Contains(out, "MyChangedFunc") {
		t.Fatalf("name embedded in a longer identifier must be excluded: %q", out)
	}
	if strings.Contains(out, "UntouchedFunc") {
		t.Fatalf("symbol outside the hunk's span must not be a changed symbol: %q", out)
	}
}

func TestCallerContextCaps(t *testing.T) {
	var defs, hunk, callers []string
	defs = append(defs, "package app", "")
	callers = append(callers, "package lib", "")
	for i := 1; i <= callerContextMaxSymbols+2; i++ {
		defs = append(defs, fmt.Sprintf("func CapFunc%02d() int { return 1 }", i))
		hunk = append(hunk, fmt.Sprintf("func CapFunc%02d() int { return 1 }", i))
		callers = append(callers, fmt.Sprintf("func Use%02d() int { return CapFunc%02d() }", i, i))
	}
	for i := 0; i < callerContextMaxSites+2; i++ {
		callers = append(callers, fmt.Sprintf("func Extra%02d() int { return CapFunc01() }", i))
	}
	dir, rev := refDefsRepo(t, map[string]string{
		"app/funcs.go":   strings.Join(defs, "\n") + "\n",
		"lib/callers.go": strings.Join(callers, "\n") + "\n",
	})
	selected := []diff.Diff{{NewPath: "app/funcs.go", Diff: addedLinesDiff(defs...)}}

	out := buildCallerContext(stdctx.Background(), dir, selected, rev, gitcmd.New(), refDefsIndex(dir, rev))
	if got := strings.Count(out, "- CapFunc01 ←"); got != callerContextMaxSites {
		t.Fatalf("per-symbol site cap: want %d, got %d: %q", callerContextMaxSites, got, out)
	}
	last := fmt.Sprintf("CapFunc%02d", callerContextMaxSymbols)
	if !strings.Contains(out, "- "+last+" ←") {
		t.Fatalf("symbol %s (within the cap) missing: %q", last, out)
	}
	for i := callerContextMaxSymbols + 1; i <= callerContextMaxSymbols+2; i++ {
		if over := fmt.Sprintf("CapFunc%02d", i); strings.Contains(out, over) {
			t.Fatalf("symbol %s beyond the cap must be dropped: %q", over, out)
		}
	}
}

func TestCallerContextDeterministicAcrossRuns(t *testing.T) {
	dir, rev := callerRepo(t)
	first := buildCallerContext(stdctx.Background(), dir, callerSelected(), rev, gitcmd.New(), refDefsIndex(dir, rev))
	if first == "" {
		t.Fatal("expected non-empty block")
	}
	for i := 0; i < 3; i++ {
		got := buildCallerContext(stdctx.Background(), dir, callerSelected(), rev, gitcmd.New(), refDefsIndex(dir, rev))
		if got != first {
			t.Fatalf("run %d diverged:\nfirst: %q\ngot:   %q", i, first, got)
		}
	}
}

func TestCallerContextEmptyWhenNoCallers(t *testing.T) {
	dir, rev := refDefsRepo(t, map[string]string{
		"app/solo.go": "package app\n\nfunc SoloFunc() int {\n\treturn 1\n}\n",
	})
	selected := []diff.Diff{{NewPath: "app/solo.go", Diff: "@@ -4,1 +4,1 @@\n-\treturn 0\n+\treturn 1\n"}}
	if out := buildCallerContext(stdctx.Background(), dir, selected, rev, gitcmd.New(), refDefsIndex(dir, rev)); out != "" {
		t.Fatalf("no callers must yield an empty block, got %q", out)
	}
	if out := buildCallerContext(stdctx.Background(), dir, selected, rev, gitcmd.New(), nil); out != "" {
		t.Fatalf("nil index must yield an empty block, got %q", out)
	}
}
