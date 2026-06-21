package anchor

import (
	"testing"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/diff"
)

const testDiff = `diff --git a/pkg/example/handler.go b/pkg/example/handler.go
--- a/pkg/example/handler.go
+++ b/pkg/example/handler.go
@@ -10,7 +10,7 @@ func HandleRequest(w http.ResponseWriter, r *http.Request) {
     ctx := r.Context()
-    log.Print("handling request")
+    log.Printf("handling request: %s", r.URL.Path)
     err := process(ctx)`

const multiLineDiff = `diff --git a/test.go b/test.go
--- a/test.go
+++ b/test.go
@@ -5,4 +5,4 @@ import "fmt"
 func foo() {
-    x := 1
-    y := 2
+    x := 10
+    y := 20
 }`

const fallbackDiff = `diff --git a/test.go b/test.go
--- a/test.go
+++ b/test.go
@@ -1,3 +1,4 @@
 package main
+import "fmt"
 func foo() {}`

const oldPathDiff = `diff --git a/old_name.go b/new_name.go
--- a/old_name.go
+++ b/new_name.go
@@ -1,3 +1,3 @@
 package main
-func oldFunc() {}
+func newFunc() {}`

const diffMarkerDiff = `diff --git a/test.go b/test.go
--- a/test.go
+++ b/test.go
@@ -1,2 +1,3 @@
 x := 1
+y := 2
 z := 3`

func TestResolveLineNumbers_Table(t *testing.T) {
	tests := []struct {
		name      string
		diffs     []diff.Diff
		finding   engine.Finding
		wantStart int
		wantEnd   int
	}{
		{
			name:      "SingleLineHunkMatch",
			diffs:     []diff.Diff{{NewPath: "pkg/example/handler.go", Diff: testDiff}},
			finding:   engine.Finding{File: "pkg/example/handler.go", QuotedCode: `    log.Print("handling request")`},
			wantStart: 11, wantEnd: 11,
		},
		{
			name:      "WhitespaceTolerant",
			diffs:     []diff.Diff{{NewPath: "pkg/example/handler.go", Diff: testDiff}},
			finding:   engine.Finding{File: "pkg/example/handler.go", QuotedCode: `log.Print("handling request")`},
			wantStart: 11, wantEnd: 11,
		},
		{
			name:      "MultiLineHunkMatch",
			diffs:     []diff.Diff{{NewPath: "test.go", Diff: multiLineDiff}},
			finding:   engine.Finding{File: "test.go", QuotedCode: "    x := 1\n    y := 2"},
			wantStart: 6, wantEnd: 7,
		},
		{
			name: "FallbackToFileContent",
			diffs: []diff.Diff{{NewPath: "test.go", Diff: fallbackDiff,
				NewFileContent: "package main\nimport \"fmt\"\nfunc foo() {}"}},
			finding:   engine.Finding{File: "test.go", QuotedCode: "package main\nimport \"fmt\""},
			wantStart: 1, wantEnd: 2,
		},
		{
			name:      "NoMatchKeepsZero",
			diffs:     []diff.Diff{{NewPath: "test.go", Diff: testDiff}},
			finding:   engine.Finding{File: "test.go", QuotedCode: "totally unrelated code"},
			wantStart: 0, wantEnd: 0,
		},
		{
			name:      "NoExistingCode",
			diffs:     []diff.Diff{{NewPath: "test.go", Diff: testDiff}},
			finding:   engine.Finding{File: "test.go", Rationale: "comment without quoted code"},
			wantStart: 0, wantEnd: 0,
		},
		{
			name:      "PathNotFound",
			diffs:     []diff.Diff{{NewPath: "other.go", Diff: testDiff}},
			finding:   engine.Finding{File: "missing.go", QuotedCode: "some code"},
			wantStart: 0, wantEnd: 0,
		},
		{
			name:      "OldPathMapping",
			diffs:     []diff.Diff{{OldPath: "old_name.go", NewPath: "new_name.go", Diff: oldPathDiff}},
			finding:   engine.Finding{File: "old_name.go", QuotedCode: "func oldFunc() {}"},
			wantStart: 2, wantEnd: 2,
		},
		{
			name:      "DiffMarkerInExistingCode",
			diffs:     []diff.Diff{{NewPath: "test.go", Diff: diffMarkerDiff}},
			finding:   engine.Finding{File: "test.go", QuotedCode: "+y := 2"},
			wantStart: 2, wantEnd: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveLineNumbers([]engine.Finding{tt.finding}, tt.diffs)
			if len(got) != 1 {
				t.Fatalf("expected 1 finding, got %d", len(got))
			}
			if got[0].Line != tt.wantStart || got[0].EndLine != tt.wantEnd {
				t.Errorf("got %d..%d, want %d..%d", got[0].Line, got[0].EndLine, tt.wantStart, tt.wantEnd)
			}
		})
	}
}

func TestResolveLineNumbers_EmptyInputs(t *testing.T) {
	if r := ResolveLineNumbers([]engine.Finding{}, []diff.Diff{{}}); len(r) != 0 {
		t.Errorf("empty findings: expected 0 results, got %d", len(r))
	}
	r := ResolveLineNumbers([]engine.Finding{{}}, []diff.Diff{})
	if len(r) != 1 || r[0].Line != 0 {
		t.Errorf("empty diffs: expected 1 result with Line=0, got %d", len(r))
	}
}

// Red-team CRITICAL: the engine must ALWAYS re-anchor from QuotedCode and never
// trust a model-supplied line number. A wrong non-zero line + a bad quote must
// still resolve to Line==0 (dropped) — proving the model's line can't smuggle
// past drift-reject. A correct quote with a wrong line must be recomputed.
func TestResolveLineNumbers_HallucinatedLineCannotBypassDriftReject(t *testing.T) {
	diffs := []diff.Diff{{NewPath: "pkg/example/handler.go", Diff: testDiff}}

	bad := ResolveLineNumbers([]engine.Finding{
		{File: "pkg/example/handler.go", Line: 42, EndLine: 42, QuotedCode: "totally unrelated code"},
	}, diffs)
	if bad[0].Line != 0 || bad[0].EndLine != 0 {
		t.Errorf("hallucinated line + bad quote: expected 0..0, got %d..%d", bad[0].Line, bad[0].EndLine)
	}

	good := ResolveLineNumbers([]engine.Finding{
		{File: "pkg/example/handler.go", Line: 99, EndLine: 99, QuotedCode: `log.Print("handling request")`},
	}, diffs)
	if good[0].Line != 11 || good[0].EndLine != 11 {
		t.Errorf("good quote + wrong line: expected recomputed 11..11, got %d..%d", good[0].Line, good[0].EndLine)
	}
}

func TestNormalizeLine(t *testing.T) {
	tests := []struct{ input, want string }{
		{"  hello  ", "hello"},
		{"+added line", "added line"},
		{"-deleted line", "deleted line"},
		{"\tindented\t", "indented"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := normalizeLine(tt.input); got != tt.want {
			t.Errorf("normalizeLine(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSplitAndNormalize_SkipsEmptyLines(t *testing.T) {
	lines := splitAndNormalize("line1\n\nline2")
	if len(lines) != 2 || lines[0] != "line1" || lines[1] != "line2" {
		t.Errorf("got %v", lines)
	}
}

func TestExtractSideLines(t *testing.T) {
	hunk := diff.Hunk{
		OldStart: 10, OldCount: 3, NewStart: 10, NewCount: 4,
		Lines: []diff.HunkLine{
			{Type: diff.HunkContext, Content: "    ctx := r.Context()"},
			{Type: diff.HunkDeleted, Content: `    log.Print("old")`},
			{Type: diff.HunkAdded, Content: `    log.Printf("new: %s", r.URL)`},
			{Type: diff.HunkContext, Content: "    err := process(ctx)"},
		},
	}

	gotNew := extractSideLines(&hunk, true)
	wantNew := []indexedLine{{10, "ctx := r.Context()"}, {11, `log.Printf("new: %s", r.URL)`}, {12, "err := process(ctx)"}}
	assertIndexed(t, "new-side", gotNew, wantNew)

	gotOld := extractSideLines(&hunk, false)
	wantOld := []indexedLine{{10, "ctx := r.Context()"}, {11, `log.Print("old")`}, {12, "err := process(ctx)"}}
	assertIndexed(t, "old-side", gotOld, wantOld)
}

func TestExtractSideLines_DivergentStartLines(t *testing.T) {
	hunk := diff.Hunk{
		OldStart: 5, OldCount: 2, NewStart: 8, NewCount: 3,
		Lines: []diff.HunkLine{
			{Type: diff.HunkContext, Content: "A"},
			{Type: diff.HunkAdded, Content: "B"},
			{Type: diff.HunkContext, Content: "C"},
		},
	}
	newSide := extractSideLines(&hunk, true)
	for i, w := range []int{8, 9, 10} {
		if newSide[i].lineNum != w {
			t.Errorf("new-side[%d].lineNum = %d, want %d", i, newSide[i].lineNum, w)
		}
	}
	oldSide := extractSideLines(&hunk, false)
	for i, w := range []int{5, 6} {
		if oldSide[i].lineNum != w {
			t.Errorf("old-side[%d].lineNum = %d, want %d", i, oldSide[i].lineNum, w)
		}
	}
}

func TestExtractSideLines_OnlyAddedOnlyDeleted(t *testing.T) {
	added := diff.Hunk{OldStart: 1, NewStart: 1, Lines: []diff.HunkLine{
		{Type: diff.HunkAdded, Content: "line1"}, {Type: diff.HunkAdded, Content: "line2"}}}
	if got := extractSideLines(&added, true); len(got) != 2 {
		t.Fatalf("only-added new-side: expected 2, got %d", len(got))
	}
	if got := extractSideLines(&added, false); len(got) != 0 {
		t.Errorf("only-added old-side: expected 0, got %d", len(got))
	}

	deleted := diff.Hunk{OldStart: 3, NewStart: 3, Lines: []diff.HunkLine{
		{Type: diff.HunkDeleted, Content: "old1"}, {Type: diff.HunkDeleted, Content: "old2"}}}
	oldSide := extractSideLines(&deleted, false)
	if len(oldSide) != 2 || oldSide[0].lineNum != 3 || oldSide[1].lineNum != 4 {
		t.Errorf("only-deleted old-side: got %v", oldSide)
	}
	if got := extractSideLines(&deleted, true); len(got) != 0 {
		t.Errorf("only-deleted new-side: expected 0, got %d", len(got))
	}
}

func TestMatchConsecutive(t *testing.T) {
	tests := []struct {
		name               string
		lines              []indexedLine
		target             []string
		wantStart, wantEnd int
		wantOK             bool
	}{
		{"SingleLine", []indexedLine{{5, "hello"}, {6, "world"}, {7, "foo"}}, []string{"world"}, 6, 6, true},
		{"MultiLine", []indexedLine{{1, "a"}, {2, "b"}, {3, "c"}, {4, "d"}}, []string{"b", "c"}, 2, 3, true},
		{"NoMatch", []indexedLine{{1, "a"}, {2, "b"}}, []string{"x"}, 0, 0, false},
		{"FirstMatchWins", []indexedLine{{10, "x"}, {11, "y"}, {20, "x"}, {21, "y"}}, []string{"x", "y"}, 10, 11, true},
		{"TargetLongerThanLines", []indexedLine{{1, "a"}}, []string{"a", "b"}, 0, 0, false},
		{"EmptySideLines", nil, []string{"a"}, 0, 0, false},
		{"MatchAtEnd", []indexedLine{{1, "a"}, {2, "b"}, {3, "c"}}, []string{"b", "c"}, 2, 3, true},
		{"MatchAtStart", []indexedLine{{1, "a"}, {2, "b"}, {3, "c"}}, []string{"a", "b"}, 1, 2, true},
		{"ExactFull", []indexedLine{{1, "a"}, {2, "b"}}, []string{"a", "b"}, 1, 2, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end, ok := matchConsecutive(tt.lines, tt.target)
			if ok != tt.wantOK || start != tt.wantStart || end != tt.wantEnd {
				t.Errorf("got (%d,%d,%v), want (%d,%d,%v)", start, end, ok, tt.wantStart, tt.wantEnd, tt.wantOK)
			}
		})
	}
}

func TestResolveFromHunk_Scenarios(t *testing.T) {
	tests := []struct {
		name      string
		diffText  string
		quoted    string
		wantStart int
		wantEnd   int
	}{
		{
			name: "AddedLines",
			diffText: `diff --git a/test.go b/test.go
--- a/test.go
+++ b/test.go
@@ -3,3 +3,5 @@
 func main() {
+    x := 1
+    y := 2
     fmt.Println("hello")
 }`,
			quoted: "    x := 1\n    y := 2", wantStart: 4, wantEnd: 5,
		},
		{
			name: "OldSideAcrossAddedLines",
			diffText: `diff --git a/test.go b/test.go
--- a/test.go
+++ b/test.go
@@ -5,3 +5,4 @@
     x := 1
+    z := 99
     y := 2
 }`,
			quoted: "    x := 1\n    y := 2", wantStart: 5, wantEnd: 6,
		},
		{
			name: "ContextLinesOnly",
			diffText: `diff --git a/test.go b/test.go
--- a/test.go
+++ b/test.go
@@ -3,3 +3,4 @@
 func main() {
     fmt.Println("hello")
+    fmt.Println("world")
 }`,
			quoted: `    fmt.Println("hello")`, wantStart: 4, wantEnd: 4,
		},
		{
			name: "SingleAddedLine",
			diffText: `diff --git a/test.go b/test.go
--- a/test.go
+++ b/test.go
@@ -1,2 +1,3 @@
 package main
+import "fmt"
 func main() {}`,
			quoted: `import "fmt"`, wantStart: 2, wantEnd: 2,
		},
		{
			name: "NewSidePriority",
			diffText: `diff --git a/test.go b/test.go
--- a/test.go
+++ b/test.go
@@ -5,3 +8,4 @@
 func main() {
     fmt.Println("hello")
+    fmt.Println("world")
 }`,
			quoted: `    fmt.Println("hello")`, wantStart: 9, wantEnd: 9,
		},
		{
			name: "MultiHunkMatchInSecond",
			diffText: `diff --git a/test.go b/test.go
--- a/test.go
+++ b/test.go
@@ -2,3 +2,3 @@
 func foo() {
-    old1()
+    new1()
 }
@@ -20,3 +20,4 @@
 func bar() {
+    added_in_bar()
     existing()
 }`,
			quoted: "    added_in_bar()", wantStart: 21, wantEnd: 21,
		},
		{
			name: "AddedWithContext",
			diffText: `diff --git a/test.go b/test.go
--- a/test.go
+++ b/test.go
@@ -10,3 +10,5 @@
 func process() {
+    validate()
+    transform()
     save()
 }`,
			quoted: "    validate()\n    transform()\n    save()", wantStart: 11, wantEnd: 13,
		},
		{
			name: "NewSideAcrossDeletedLines",
			diffText: `diff --git a/test.go b/test.go
--- a/test.go
+++ b/test.go
@@ -5,4 +5,3 @@
     a := 1
-    unused := 0
     b := 2
 }`,
			quoted: "    a := 1\n    b := 2", wantStart: 5, wantEnd: 6,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveLineNumbers([]engine.Finding{{File: "test.go", QuotedCode: tt.quoted}},
				[]diff.Diff{{NewPath: "test.go", Diff: tt.diffText}})
			if got[0].Line != tt.wantStart || got[0].EndLine != tt.wantEnd {
				t.Errorf("got %d..%d, want %d..%d", got[0].Line, got[0].EndLine, tt.wantStart, tt.wantEnd)
			}
		})
	}
}

func TestResolveLineNumbers_MultipleFindingsOnSameFile(t *testing.T) {
	raw := `diff --git a/test.go b/test.go
--- a/test.go
+++ b/test.go
@@ -1,4 +1,6 @@
 package main
+import "fmt"
+import "os"
 func main() {
-    old()
+    new()
 }`
	diffs := []diff.Diff{{NewPath: "test.go", Diff: raw}}
	got := ResolveLineNumbers([]engine.Finding{
		{File: "test.go", QuotedCode: `import "fmt"`},
		{File: "test.go", QuotedCode: `import "os"`},
		{File: "test.go", QuotedCode: "    old()"},
	}, diffs)
	checks := []struct{ start, end int }{{2, 2}, {3, 3}, {3, 3}}
	for i, c := range checks {
		if got[i].Line != c.start || got[i].EndLine != c.end {
			t.Errorf("finding[%d]: got %d..%d, want %d..%d", i, got[i].Line, got[i].EndLine, c.start, c.end)
		}
	}
}

func TestResolveLineNumbers_MixedStrategies(t *testing.T) {
	raw := `diff --git a/test.go b/test.go
--- a/test.go
+++ b/test.go
@@ -5,3 +5,4 @@
 func foo() {
+    newLine()
     bar()
 }`
	diffs := []diff.Diff{{
		NewPath: "test.go", Diff: raw,
		NewFileContent: "package main\nimport \"fmt\"\n\nfunc helper() {}\nfunc foo() {\n    newLine()\n    bar()\n}",
	}}
	got := ResolveLineNumbers([]engine.Finding{
		{File: "test.go", QuotedCode: "    newLine()"},
		{File: "test.go", QuotedCode: "func helper() {}"},
		{File: "test.go", QuotedCode: "this_does_not_exist_anywhere()"},
	}, diffs)
	checks := []struct{ start, end int }{{6, 6}, {4, 4}, {0, 0}}
	for i, c := range checks {
		if got[i].Line != c.start || got[i].EndLine != c.end {
			t.Errorf("finding[%d]: got %d..%d, want %d..%d", i, got[i].Line, got[i].EndLine, c.start, c.end)
		}
	}
}

// Regression: a multi-line QuotedCode spanning an interior blank line must still
// anchor. splitAndNormalize drops the blank from the target, so both the hunk side
// and the file-content side must drop their blanks too (preserving real line
// numbers) — otherwise the quote never matches consecutively and is drift-rejected.
func TestResolveLineNumbers_InteriorBlankLine(t *testing.T) {
	hunkBlank := `diff --git a/test.go b/test.go
--- a/test.go
+++ b/test.go
@@ -3,4 +3,6 @@
 func foo() {
+    a()
+
+    b()
 }`
	t.Run("HunkSide", func(t *testing.T) {
		got := ResolveLineNumbers([]engine.Finding{
			{File: "test.go", QuotedCode: "    a()\n\n    b()"},
		}, []diff.Diff{{NewPath: "test.go", Diff: hunkBlank}})
		if got[0].Line != 4 || got[0].EndLine != 6 {
			t.Errorf("hunk-side interior blank: got %d..%d, want 4..6", got[0].Line, got[0].EndLine)
		}
	})

	t.Run("FileContentSide", func(t *testing.T) {
		got := ResolveLineNumbers([]engine.Finding{
			{File: "test.go", QuotedCode: "a()\n\nb()"},
		}, []diff.Diff{{
			NewPath:        "test.go",
			Diff:           "diff --git a/test.go b/test.go\n--- a/test.go\n+++ b/test.go\n@@ -1,1 +1,1 @@\n unrelated",
			NewFileContent: "a()\n\nb()\n",
		}})
		if got[0].Line != 1 || got[0].EndLine != 3 {
			t.Errorf("file-content interior blank: got %d..%d, want 1..3", got[0].Line, got[0].EndLine)
		}
	})
}

func assertIndexed(t *testing.T, label string, got, want []indexedLine) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: expected %d lines, got %d", label, len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s[%d]: got %v, want %v", label, i, got[i], want[i])
		}
	}
}
