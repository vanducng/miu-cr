package diff

import (
	"context"
	"errors"
	"testing"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
)

// A combined (merge) diff must fail loudly, never parse to zero diffs and report
// a false "clean".
func TestParseDiffText_CombinedDiffRejected(t *testing.T) {
	combined := `commit deadbeef
Merge: 1111111 2222222

diff --cc f.txt
index 0f7bc76,422c2b7..de98044
--- a/f.txt
+++ b/f.txt
@@@ -1,2 -1,2 +1,3 @@@
  a
+ b
 +c
`
	_, err := ParseDiffText(context.Background(), combined, t.TempDir(), "", nil)
	var ce *clierr.CLIError
	if !errors.As(err, &ce) || ce.Code != "git.combined_diff_unsupported" {
		t.Fatalf("want git.combined_diff_unsupported CLIError, got %v", err)
	}
}

func TestParseDiffText_MultiFileSplit(t *testing.T) {
	diffText := `diff --git a/a.go b/a.go
index 1111111..2222222 100644
--- a/a.go
+++ b/a.go
@@ -1,2 +1,2 @@
 keep
-old
+new
diff --git a/b.go b/b.go
index 3333333..4444444 100644
--- a/b.go
+++ b/b.go
@@ -1,1 +1,2 @@
 base
+added
`
	diffs, err := ParseDiffText(context.Background(), diffText, t.TempDir(), "", nil)
	if err != nil {
		t.Fatalf("ParseDiffText: %v", err)
	}
	if len(diffs) != 2 {
		t.Fatalf("expected 2 diffs, got %d", len(diffs))
	}
	if diffs[0].NewPath != "a.go" || diffs[1].NewPath != "b.go" {
		t.Fatalf("paths = %q,%q want a.go,b.go", diffs[0].NewPath, diffs[1].NewPath)
	}
	if diffs[1].Insertions != 1 {
		t.Errorf("b.go Insertions = %d, want 1", diffs[1].Insertions)
	}
}

func TestParseDiffText_Rename(t *testing.T) {
	diffText := `diff --git a/pkg/old name.go b/pkg/new name.go
similarity index 95%
rename from pkg/old name.go
rename to pkg/new name.go
index 1234567..89abcde 100644
--- a/pkg/old name.go
+++ b/pkg/new name.go
@@ -1,3 +1,3 @@
 line1
-line2
+line2 changed
 line3
`
	diffs, err := ParseDiffText(context.Background(), diffText, t.TempDir(), "", nil)
	if err != nil {
		t.Fatalf("ParseDiffText: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d", len(diffs))
	}
	d := diffs[0]
	if !d.IsRenamed {
		t.Errorf("IsRenamed = false, want true")
	}
	if d.OldPath != "pkg/old name.go" {
		t.Errorf("OldPath = %q, want %q", d.OldPath, "pkg/old name.go")
	}
	if d.NewPath != "pkg/new name.go" {
		t.Errorf("NewPath = %q, want %q", d.NewPath, "pkg/new name.go")
	}
	if d.IsNew || d.IsDeleted {
		t.Errorf("IsNew/IsDeleted = %v/%v, want false/false", d.IsNew, d.IsDeleted)
	}
}

func TestParseDiffText_PureRename(t *testing.T) {
	diffText := `diff --git a/old.go b/new.go
similarity index 100%
rename from old.go
rename to new.go
`
	diffs, err := ParseDiffText(context.Background(), diffText, t.TempDir(), "", nil)
	if err != nil {
		t.Fatalf("ParseDiffText: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d", len(diffs))
	}
	d := diffs[0]
	if !d.IsRenamed || d.OldPath != "old.go" || d.NewPath != "new.go" {
		t.Errorf("got IsRenamed=%v OldPath=%q NewPath=%q, want true/old.go/new.go",
			d.IsRenamed, d.OldPath, d.NewPath)
	}
}

func TestParseDiffText_DeletedFile(t *testing.T) {
	diffText := `diff --git a/gone.go b/gone.go
deleted file mode 100644
index 1234567..0000000
--- a/gone.go
+++ /dev/null
@@ -1,2 +0,0 @@
-line1
-line2
`
	diffs, err := ParseDiffText(context.Background(), diffText, t.TempDir(), "", nil)
	if err != nil {
		t.Fatalf("ParseDiffText: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d", len(diffs))
	}
	d := diffs[0]
	if !d.IsDeleted {
		t.Errorf("IsDeleted = false, want true")
	}
	if d.NewPath != "/dev/null" {
		t.Errorf("NewPath = %q, want /dev/null", d.NewPath)
	}
	if d.OldPath != "gone.go" {
		t.Errorf("OldPath = %q, want gone.go", d.OldPath)
	}
}

func TestParseDiffText_NewFile(t *testing.T) {
	diffText := `diff --git a/fresh.go b/fresh.go
new file mode 100644
index 0000000..1234567
--- /dev/null
+++ b/fresh.go
@@ -0,0 +1,2 @@
+line1
+line2
`
	diffs, err := ParseDiffText(context.Background(), diffText, t.TempDir(), "", nil)
	if err != nil {
		t.Fatalf("ParseDiffText: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d", len(diffs))
	}
	d := diffs[0]
	if !d.IsNew {
		t.Errorf("IsNew = false, want true")
	}
	if d.IsDeleted {
		t.Errorf("IsDeleted = true, want false")
	}
	if d.Insertions != 2 {
		t.Errorf("Insertions = %d, want 2", d.Insertions)
	}
}

func TestParseDiffText_Binary(t *testing.T) {
	diffText := `diff --git a/logo.png b/logo.png
index 1111111..2222222 100644
Binary files a/logo.png and b/logo.png differ
`
	diffs, err := ParseDiffText(context.Background(), diffText, t.TempDir(), "", nil)
	if err != nil {
		t.Fatalf("ParseDiffText: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d", len(diffs))
	}
	if !diffs[0].IsBinary {
		t.Errorf("IsBinary = false, want true")
	}
}

// git C-quotes paths with non-ASCII/control bytes in the diff header
// (`"a/caf\303\251.go"`); the parser must unquote them so the file is reviewed
// rather than silently dropped. The blob read fails (the temp dir has no such
// file) which is fine, we only assert the header parsed into the right path.
func TestParseDiffText_QuotedNonASCIIHeader(t *testing.T) {
	diffText := "diff --git \"a/caf\\303\\251.go\" \"b/caf\\303\\251.go\"\n" +
		"index 1111111..2222222 100644\n" +
		"--- \"a/caf\\303\\251.go\"\n" +
		"+++ \"b/caf\\303\\251.go\"\n" +
		"@@ -1,1 +1,2 @@\n" +
		" package main\n" +
		"+// added\n"
	diffs, err := ParseDiffText(context.Background(), diffText, t.TempDir(), "", nil)
	if err != nil {
		t.Fatalf("ParseDiffText: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d", len(diffs))
	}
	if diffs[0].NewPath != "café.go" {
		t.Errorf("NewPath = %q, want café.go", diffs[0].NewPath)
	}
	if diffs[0].OldPath != "café.go" {
		t.Errorf("OldPath = %q, want café.go", diffs[0].OldPath)
	}
	if diffs[0].Insertions != 1 {
		t.Errorf("Insertions = %d, want 1", diffs[0].Insertions)
	}
}

// A `diff --git` line that matches neither header form must not crash and must
// not produce a Diff: the file is observably dropped (warned), never silently
// folded into a prior file's hunks.
func TestParseDiffText_UnparseableHeaderDropped(t *testing.T) {
	diffText := "diff --git mangled-no-prefixes\n" +
		"index 1111111..2222222 100644\n" +
		"@@ -1,1 +1,1 @@\n" +
		"-a\n" +
		"+b\n"
	diffs, err := ParseDiffText(context.Background(), diffText, t.TempDir(), "", nil)
	if err != nil {
		t.Fatalf("ParseDiffText: %v", err)
	}
	if len(diffs) != 0 {
		t.Fatalf("unparseable header must yield no diff, got %d: %+v", len(diffs), diffs)
	}
}

func TestUnquoteGitPath(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{`"a/caf\303\251.go"`, "a/café.go"},
		{`"b/plain.go"`, "b/plain.go"},
		{`"a/tab\there.go"`, "a/tab\there.go"},
		{`"unterminated`, "unterminated"}, // malformed degrades to trimmed inner text
	}
	for _, tt := range tests {
		if got := unquoteGitPath(tt.in); got != tt.want {
			t.Errorf("unquoteGitPath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// Regression: an added/context content line whose text merely contains the
// substring "Binary files " must NOT flag the file as binary (which would silently
// drop a real text file from review). Only git's unprefixed "...differ" notice does.
func TestParseDiffText_BinarySubstringNotBinary(t *testing.T) {
	diffText := `diff --git a/notes.go b/notes.go
index 1111111..2222222 100644
--- a/notes.go
+++ b/notes.go
@@ -1,1 +1,2 @@
 package main
+// Binary files are great and should be reviewed differ
`
	diffs, err := ParseDiffText(context.Background(), diffText, t.TempDir(), "", nil)
	if err != nil {
		t.Fatalf("ParseDiffText: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d", len(diffs))
	}
	if diffs[0].IsBinary {
		t.Errorf("IsBinary = true, want false (content substring is not a binary marker)")
	}
}
