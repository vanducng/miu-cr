package engine

import (
	"testing"

	"github.com/vanducng/miu-cr/internal/engine/diff"
)

func paths(ds []diff.Diff) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.ReviewPath()
	}
	return out
}

func TestSelectFiles_AllowlistExcludeBinary(t *testing.T) {
	in := []diff.Diff{
		{NewPath: "internal/app/main.go"},
		{NewPath: "internal/app/main_test.go"},
		{NewPath: "web/index.html"},
		{NewPath: "assets/logo.png", IsBinary: true},
		{OldPath: "old/removed.go", NewPath: "/dev/null", IsDeleted: true},
		{NewPath: "vendor/lib/dep.go"},
		{NewPath: "README.md"},
	}
	opts := FilterOptions{
		Extensions: []string{"go", ".html"},
		Exclude:    []string{"**/*_test.go", "vendor/**"},
	}
	got := paths(SelectFiles(in, opts))
	want := []string{"internal/app/main.go", "web/index.html", "old/removed.go"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestSelectFiles_IncludeGlob(t *testing.T) {
	in := []diff.Diff{
		{NewPath: "internal/engine/x.go"},
		{NewPath: "cmd/main.go"},
	}
	opts := FilterOptions{
		Extensions: []string{"go"},
		Include:    []string{"internal/**"},
	}
	got := paths(SelectFiles(in, opts))
	if len(got) != 1 || got[0] != "internal/engine/x.go" {
		t.Fatalf("include glob failed: %v", got)
	}
}

func TestSelectFiles_NoExtensionAllowsAll(t *testing.T) {
	in := []diff.Diff{
		{NewPath: "a.go"},
		{NewPath: "b.rs"},
		{NewPath: "c"},
	}
	got := SelectFiles(in, FilterOptions{})
	if len(got) != 3 {
		t.Fatalf("expected all 3 kept, got %d", len(got))
	}
}

func TestSelectFiles_SkipsDevNull(t *testing.T) {
	in := []diff.Diff{
		{NewPath: "/dev/null", IsDeleted: true},
		{NewPath: ""},
		{NewPath: "keep.go"},
	}
	got := paths(SelectFiles(in, FilterOptions{Extensions: []string{"go"}}))
	if len(got) != 1 || got[0] != "keep.go" {
		t.Fatalf("expected only keep.go, got %v", got)
	}
}
