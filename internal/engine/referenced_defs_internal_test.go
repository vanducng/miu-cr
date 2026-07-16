package engine

import (
	stdctx "context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine/diff"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
	"github.com/vanducng/miu-cr/internal/engine/tools/symbolcontext"
)

func refDefsGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func refDefsWrite(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func refDefsRepo(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	refDefsGit(t, dir, "init", "-q")
	refDefsGit(t, dir, "config", "user.email", "t@example.com")
	refDefsGit(t, dir, "config", "user.name", "t")
	refDefsGit(t, dir, "config", "commit.gpgsign", "false")
	for name, content := range files {
		refDefsWrite(t, dir, name, content)
	}
	refDefsGit(t, dir, "add", "-A")
	refDefsGit(t, dir, "commit", "-q", "-m", "base")
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return dir, strings.TrimSpace(string(out))
}

func refDefsIndex(dir, rev string) *symbolcontext.Index {
	return symbolcontext.NewIndex(config.SymbolContext{}, symbolcontext.Context{RepoDir: dir, Rev: rev, Runner: gitcmd.New()})
}

func addedLinesDiff(lines ...string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "@@ -0,0 +1,%d @@\n", len(lines))
	for _, l := range lines {
		sb.WriteString("+" + l + "\n")
	}
	return sb.String()
}

func TestReferencedDefsResolvesUsedSymbolsAndFilters(t *testing.T) {
	dir, rev := refDefsRepo(t, map[string]string{
		"lib/util.go": "package lib\n\nfunc HelperThing() int { return 1 }\n\nfunc OtherHelper() int { return 2 }\n\nfunc Update() int { return 3 }\n",
		"app/main.go": "package app\n\nfunc LocalFunc() int { return 0 }\n",
	})
	selected := []diff.Diff{{
		NewPath: "app/main.go",
		Diff: addedLinesDiff(
			"func LocalFunc() int {",
			"\treturn HelperThing() + OtherHelper() + Update()",
			"\t_ = OtherHelper()",
			"}",
		),
	}}

	out := buildReferencedDefsContext(stdctx.Background(), selected, refDefsIndex(dir, rev))
	if !strings.Contains(out, referencedDefsHeader) {
		t.Fatalf("missing header: %q", out)
	}
	if !strings.Contains(out, "HelperThing (function) lib/util.go:3") {
		t.Fatalf("missing resolved definition for HelperThing: %q", out)
	}
	if !strings.Contains(out, "OtherHelper (function) lib/util.go:5") {
		t.Fatalf("missing resolved definition for OtherHelper: %q", out)
	}
	if strings.Contains(out, "LocalFunc") {
		t.Fatalf("identifier defined in the changed file must be excluded: %q", out)
	}
	if strings.Contains(out, "Update") {
		t.Fatalf("keyword-named identifier must be filtered: %q", out)
	}
	if strings.Contains(out, "func (") || strings.Contains(out, "- return") || strings.Contains(out, "- int") {
		t.Fatalf("keywords/short lowercase words leaked: %q", out)
	}
	// OtherHelper appears twice on added lines, HelperThing once: frequency
	// ranking puts OtherHelper first.
	if strings.Index(out, "OtherHelper") > strings.Index(out, "HelperThing") {
		t.Fatalf("frequency ranking violated: %q", out)
	}
}

func TestReferencedDefsDeterministicAcrossRuns(t *testing.T) {
	dir, rev := refDefsRepo(t, map[string]string{
		"lib/util.go": "package lib\n\nfunc AlphaHelper() int { return 1 }\n\nfunc BravoHelper() int { return 2 }\n\nfunc CharlieHelper() int { return 3 }\n",
	})
	selected := []diff.Diff{{
		NewPath: "app/main.go",
		Diff:    addedLinesDiff("x := AlphaHelper() + BravoHelper() + CharlieHelper()", "y := CharlieHelper()"),
	}}

	first := buildReferencedDefsContext(stdctx.Background(), selected, refDefsIndex(dir, rev))
	if first == "" {
		t.Fatal("expected non-empty block")
	}
	for i := 0; i < 3; i++ {
		got := buildReferencedDefsContext(stdctx.Background(), selected, refDefsIndex(dir, rev))
		if got != first {
			t.Fatalf("run %d diverged:\nfirst: %q\ngot:   %q", i, first, got)
		}
	}
}

func TestReferencedDefsEnforcesByteCap(t *testing.T) {
	files := map[string]string{}
	var refs []string
	for f := 0; f < 3; f++ {
		var sb strings.Builder
		sb.WriteString("package lib\n\n")
		for i := 0; i < referencedDefsMaxNames; i++ {
			name := fmt.Sprintf("Zz%s%02d", strings.Repeat("a", 300), i)
			fmt.Fprintf(&sb, "func %s() int { return 1 }\n", name)
			if f == 0 {
				refs = append(refs, "\t_ = "+name+"()")
			}
		}
		files[fmt.Sprintf("lib/f%d.go", f)] = sb.String()
	}
	dir, rev := refDefsRepo(t, files)
	selected := []diff.Diff{{NewPath: "app/main.go", Diff: addedLinesDiff(refs...)}}

	out := buildReferencedDefsContext(stdctx.Background(), selected, refDefsIndex(dir, rev))
	if !strings.HasPrefix(out, referencedDefsHeader) {
		t.Fatalf("missing header: %q", out[:80])
	}
	if len(out) > referencedDefsBytes {
		t.Fatalf("block exceeds byte cap: %d > %d", len(out), referencedDefsBytes)
	}
}

func TestReferencedDefsEmptyWhenNothingResolves(t *testing.T) {
	dir, rev := refDefsRepo(t, map[string]string{
		"lib/util.go": "package lib\n\nfunc HelperThing() int { return 1 }\n",
	})
	selected := []diff.Diff{{
		NewPath: "app/main.go",
		Diff:    addedLinesDiff("return nil", "select unknownthing from missingplace", "x := unresolvableIdent()"),
	}}
	if out := buildReferencedDefsContext(stdctx.Background(), selected, refDefsIndex(dir, rev)); out != "" {
		t.Fatalf("expected empty block when nothing resolves, got %q", out)
	}
	if out := buildReferencedDefsContext(stdctx.Background(), nil, refDefsIndex(dir, rev)); out != "" {
		t.Fatalf("expected empty block for empty selection, got %q", out)
	}
	if out := buildReferencedDefsContext(stdctx.Background(), selected, nil); out != "" {
		t.Fatalf("expected empty block for nil index, got %q", out)
	}
}
