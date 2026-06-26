package context

import (
	stdctx "context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine/diff"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

func gitTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func initRelatedRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitTest(t, dir, "init", "-q")
	gitTest(t, dir, "config", "user.email", "t@example.com")
	gitTest(t, dir, "config", "user.name", "t")
	return dir
}

func TestBuildRelatedContextGoHops(t *testing.T) {
	dir := initRelatedRepo(t)
	writeTestFile(t, dir, "go.mod", "module example.com/app\n\ngo 1.24\n")
	writeTestFile(t, dir, "cmd/app/main.go", "package main\n\nimport \"example.com/app/internal/service\"\n\nfunc main() { service.Run() }\n")
	writeTestFile(t, dir, "internal/service/service.go", "package service\n\nimport \"example.com/app/internal/store\"\n\nfunc Run() { store.Save() }\n")
	writeTestFile(t, dir, "internal/store/store.go", "package store\n\nfunc Save() {}\n")
	writeTestFile(t, dir, "internal/api/api.go", "package api\n\nimport \"example.com/app/internal/service\"\n\nfunc Handler() { service.Run() }\n")
	gitTest(t, dir, "add", ".")
	gitTest(t, dir, "commit", "-q", "-m", "base")

	oneHop := BuildRelatedContext(stdctx.Background(), dir, "HEAD", []diff.Diff{{NewPath: "cmd/app/main.go", Ref: "HEAD"}}, gitcmd.New(), RelatedOptions{HopDepth: 1, MaxFiles: 10})
	if !containsString(oneHop.Files, "internal/service/service.go") {
		t.Fatalf("one-hop context missed direct import: %#v\n%s", oneHop.Files, oneHop.Text)
	}
	if containsString(oneHop.Files, "internal/store/store.go") {
		t.Fatalf("one-hop context included transitive import: %#v\n%s", oneHop.Files, oneHop.Text)
	}

	res := BuildRelatedContext(stdctx.Background(), dir, "HEAD", []diff.Diff{{NewPath: "cmd/app/main.go", Ref: "HEAD"}}, gitcmd.New(), RelatedOptions{HopDepth: 2, MaxFiles: 10})
	for _, want := range []string{"internal/service/service.go", "internal/api/api.go", "internal/store/store.go"} {
		if !containsString(res.Files, want) {
			t.Fatalf("missing related file %s from %#v\n%s", want, res.Files, res.Text)
		}
	}
	if containsString(res.Files, "cmd/app/main.go") {
		t.Fatalf("changed root must not be repeated as related context: %#v", res.Files)
	}
	if !strings.Contains(res.Text, "--- related_file: internal/service/service.go (hop 1) ---") {
		t.Fatalf("missing hop label for service:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "--- related_file: internal/store/store.go (hop 2) ---") {
		t.Fatalf("missing hop label for store:\n%s", res.Text)
	}

	capped := BuildRelatedContext(stdctx.Background(), dir, "HEAD", []diff.Diff{{NewPath: "cmd/app/main.go", Ref: "HEAD"}}, gitcmd.New(), RelatedOptions{HopDepth: 2, MaxFiles: 1})
	if len(capped.Files) != 1 || !capped.Truncated {
		t.Fatalf("cap should render one file and mark truncated, files=%#v truncated=%v", capped.Files, capped.Truncated)
	}
}

func TestBuildRelatedContextStagedReadsIndex(t *testing.T) {
	dir := initRelatedRepo(t)
	writeTestFile(t, dir, "go.mod", "module example.com/app\n\ngo 1.24\n")
	writeTestFile(t, dir, "cmd/app/main.go", "package main\n\nimport \"example.com/app/internal/service\"\n\nfunc main() { service.Run() }\n")
	writeTestFile(t, dir, "internal/service/service.go", "package service\n\nconst Marker = \"index content\"\nfunc Run() {}\n")
	gitTest(t, dir, "add", ".")
	gitTest(t, dir, "commit", "-q", "-m", "base")
	writeTestFile(t, dir, "cmd/app/main.go", "package main\n\nimport \"example.com/app/internal/service\"\n\nfunc main() { service.Run(); service.Run() }\n")
	gitTest(t, dir, "add", "cmd/app/main.go")
	writeTestFile(t, dir, "internal/service/service.go", "package service\n\nconst Marker = \"live worktree content\"\nfunc Run() {}\n")

	res := BuildRelatedContext(stdctx.Background(), dir, "", []diff.Diff{{NewPath: "cmd/app/main.go", Ref: ""}}, gitcmd.New(), RelatedOptions{HopDepth: 1, MaxFiles: 10})
	if !strings.Contains(res.Text, "index content") {
		t.Fatalf("related context did not read the index blob:\n%s", res.Text)
	}
	if strings.Contains(res.Text, "live worktree content") {
		t.Fatalf("related context read unstaged worktree content:\n%s", res.Text)
	}
}

func TestBuildRelatedContextPythonRelativeImports(t *testing.T) {
	dir := initRelatedRepo(t)
	writeTestFile(t, dir, "app/handlers/view.py", "from ..models import user\nfrom . import helpers\n\ndef run(): pass\n")
	writeTestFile(t, dir, "app/models/user.py", "class User: pass\n")
	writeTestFile(t, dir, "app/handlers/helpers.py", "def helper(): pass\n")
	gitTest(t, dir, "add", ".")
	gitTest(t, dir, "commit", "-q", "-m", "base")

	res := BuildRelatedContext(stdctx.Background(), dir, "HEAD", []diff.Diff{{NewPath: "app/handlers/view.py", Ref: "HEAD"}}, gitcmd.New(), RelatedOptions{HopDepth: 1, MaxFiles: 10})
	for _, want := range []string{"app/models/user.py", "app/handlers/helpers.py"} {
		if !containsString(res.Files, want) {
			t.Fatalf("missing python related file %s from %#v\n%s", want, res.Files, res.Text)
		}
	}
}

func TestTruncateRelatedUTF8Bytes(t *testing.T) {
	got := string(truncateRelatedUTF8Bytes([]byte("abé"), 3))
	if got != "ab" {
		t.Fatalf("truncated in the middle of a rune: %q", got)
	}
}

func containsString(in []string, want string) bool {
	for _, s := range in {
		if s == want {
			return true
		}
	}
	return false
}
