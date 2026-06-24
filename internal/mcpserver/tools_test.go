package mcpserver_test

import (
	stdctx "context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/anchor"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
	"github.com/vanducng/miu-cr/internal/mcpserver"
	"github.com/vanducng/miu-cr/internal/store/sqlite"
)

func init() { engine.SetAnchorer(anchor.ResolveLineNumbers) }

// fakeAgent returns canned findings; no network, no API key.
type fakeAgent struct{ findings []engine.Finding }

func (f *fakeAgent) Review(_ stdctx.Context, _ engine.AgentContext) (engine.ReviewOutput, error) {
	findings := make([]engine.Finding, len(f.findings))
	copy(findings, f.findings)
	return engine.ReviewOutput{
		Findings:    findings,
		Walkthrough: "Sample walkthrough: exercises the review path.",
	}, nil
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func stagedRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	git(t, dir, "config", "user.email", "t@example.com")
	git(t, dir, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "app.go"), []byte("package app\n\nfunc Existing() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")
	if err := os.WriteFile(filepath.Join(dir, "app.go"), []byte("package app\n\nfunc Existing() int { return 1 }\n\nfunc Risky() {\n\tpassword := \"hunter2\"\n\t_ = password\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "app.go")
	return dir
}

// connect wires an in-memory client to a server built over deps.
func connect(t *testing.T, deps mcpserver.Deps, opts mcpserver.Options) *mcp.ClientSession {
	t.Helper()
	srv, err := mcpserver.New(deps, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ct, st := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(stdctx.Background(), st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(stdctx.Background(), ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestReviewRunReturnsFindings(t *testing.T) {
	dir := stagedRepo(t)
	t.Chdir(dir) // review_run reviews repo "."

	fa := &fakeAgent{findings: []engine.Finding{
		{File: "app.go", Severity: "high", Category: "security", Rationale: "hardcoded credential", QuotedCode: "password := \"hunter2\""},
		{File: "app.go", Severity: "critical", Category: "hallucination", Rationale: "drift", QuotedCode: "never_in_file()"},
	}}
	eng := engine.New(fa, gitcmd.New())

	cs := connect(t, mcpserver.Deps{Engine: eng}, mcpserver.Options{})
	res, err := cs.CallTool(stdctx.Background(), &mcp.CallToolParams{
		Name:      "review_run",
		Arguments: map[string]any{"staged": true, "gate": "high"},
	})
	if err != nil {
		t.Fatalf("CallTool review_run: %v", err)
	}
	if res.IsError {
		t.Fatalf("review_run returned error: %+v", res.Content)
	}
	var out struct {
		Findings []engine.Finding `json:"findings"`
	}
	marshalStructured(t, res, &out)
	if len(out.Findings) != 1 {
		t.Fatalf("drift-reject: want 1 anchored finding, got %d: %+v", len(out.Findings), out.Findings)
	}
	if out.Findings[0].Line == 0 {
		t.Errorf("anchored finding must have a non-zero line: %+v", out.Findings[0])
	}
}

func TestReviewRunReviewGetRoundTrip(t *testing.T) {
	dir := stagedRepo(t)
	t.Chdir(dir)

	st, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	fa := &fakeAgent{findings: []engine.Finding{
		{File: "app.go", Severity: "high", Category: "security", Rationale: "hardcoded credential", QuotedCode: "password := \"hunter2\""},
	}}
	eng := engine.New(fa, gitcmd.New())
	eng.Store = sqlite.EngineStore{S: st}

	cs := connect(t, mcpserver.Deps{Engine: eng, Store: st}, mcpserver.Options{})

	runRes, err := cs.CallTool(stdctx.Background(), &mcp.CallToolParams{
		Name:      "review_run",
		Arguments: map[string]any{"staged": true, "gate": "high"},
	})
	if err != nil {
		t.Fatalf("CallTool review_run: %v", err)
	}
	if runRes.IsError {
		t.Fatalf("review_run returned error: %+v", runRes.Content)
	}
	var run struct {
		ID       string           `json:"id"`
		Findings []engine.Finding `json:"findings"`
	}
	marshalStructured(t, runRes, &run)
	if run.ID == "" {
		t.Fatal("review_run must return a non-empty id when a Store is wired")
	}
	if len(run.Findings) != 1 {
		t.Fatalf("want 1 finding, got %d: %+v", len(run.Findings), run.Findings)
	}

	getRes, err := cs.CallTool(stdctx.Background(), &mcp.CallToolParams{
		Name:      "review_get",
		Arguments: map[string]any{"id": run.ID},
	})
	if err != nil {
		t.Fatalf("CallTool review_get: %v", err)
	}
	if getRes.IsError {
		t.Fatalf("review_get returned error: %+v", getRes.Content)
	}
	var got struct {
		ID       string           `json:"id"`
		Findings []engine.Finding `json:"findings"`
	}
	marshalStructured(t, getRes, &got)
	if got.ID != run.ID {
		t.Fatalf("review_get id mismatch: want %q, got %q", run.ID, got.ID)
	}
	if len(got.Findings) != len(run.Findings) {
		t.Fatalf("round-trip findings count mismatch: want %d, got %d", len(run.Findings), len(got.Findings))
	}
	if got.Findings[0] != run.Findings[0] {
		t.Fatalf("round-trip finding mismatch:\nwant %+v\ngot  %+v", run.Findings[0], got.Findings[0])
	}
}

func TestReviewRunEnforcesByteBound(t *testing.T) {
	dir := stagedRepo(t)
	t.Chdir(dir)

	big := strings.Repeat("x", 4096)
	fa := &fakeAgent{findings: []engine.Finding{
		{File: "app.go", Severity: "high", Category: "security", Rationale: big, QuotedCode: "password := \"hunter2\""},
	}}
	eng := engine.New(fa, gitcmd.New())

	cs := connect(t, mcpserver.Deps{Engine: eng}, mcpserver.Options{MaxBytes: 64})
	res, err := cs.CallTool(stdctx.Background(), &mcp.CallToolParams{
		Name:      "review_run",
		Arguments: map[string]any{"staged": true, "gate": "high"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("oversized output should surface as a tool error")
	}
}

// review_get on an unknown id must surface via policy.toolErr as a tool error,
// not a transport error, with a non-empty message.
func TestReviewGetMissingIDIsToolError(t *testing.T) {
	dir := stagedRepo(t)
	t.Chdir(dir)

	st, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	eng := engine.New(&fakeAgent{}, gitcmd.New())
	eng.Store = sqlite.EngineStore{S: st}
	cs := connect(t, mcpserver.Deps{Engine: eng, Store: st}, mcpserver.Options{})

	res, err := cs.CallTool(stdctx.Background(), &mcp.CallToolParams{
		Name:      "review_get",
		Arguments: map[string]any{"id": "does-not-exist"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("review_get on missing id should be a tool error")
	}
}

// review_run against a non-git directory must surface the engine error through
// policy.toolErr as a tool error.
func TestReviewRunNonGitDirIsToolError(t *testing.T) {
	dir := t.TempDir() // not a git repo
	t.Chdir(dir)

	eng := engine.New(&fakeAgent{}, gitcmd.New())
	cs := connect(t, mcpserver.Deps{Engine: eng}, mcpserver.Options{})

	res, err := cs.CallTool(stdctx.Background(), &mcp.CallToolParams{
		Name:      "review_run",
		Arguments: map[string]any{"staged": true},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("review_run in a non-git dir should be a tool error")
	}
}

// review_run must enforce the same mode/gate contract as the CLI at the MCP
// boundary: an invalid mode combo or out-of-set gate is rejected before the
// engine runs, surfaced as a tool error.
func TestReviewRunRejectsInvalidInvocation(t *testing.T) {
	dir := stagedRepo(t)
	t.Chdir(dir)

	eng := engine.New(&fakeAgent{}, gitcmd.New())
	cs := connect(t, mcpserver.Deps{Engine: eng}, mcpserver.Options{})

	cases := []struct {
		name string
		args map[string]any
	}{
		{"staged plus commit", map[string]any{"staged": true, "commit": "HEAD"}},
		{"bad gate", map[string]any{"staged": true, "gate": "warn"}},
		{"no mode", map[string]any{}},
		{"half range", map[string]any{"from": "main"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := cs.CallTool(stdctx.Background(), &mcp.CallToolParams{
				Name:      "review_run",
				Arguments: tc.args,
			})
			if err != nil {
				t.Fatalf("CallTool: %v", err)
			}
			if !res.IsError {
				t.Fatalf("invalid invocation %v should be a tool error", tc.args)
			}
		})
	}
}

func TestListToolsExposesBoth(t *testing.T) {
	eng := engine.New(&fakeAgent{}, gitcmd.New())
	cs := connect(t, mcpserver.Deps{Engine: eng}, mcpserver.Options{})
	lt, err := cs.ListTools(stdctx.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range lt.Tools {
		names[tool.Name] = true
	}
	if !names["review_run"] || !names["review_get"] {
		t.Fatalf("tools/list missing review_run/review_get: %v", names)
	}
}

func marshalStructured(t *testing.T, res *mcp.CallToolResult, dst any) {
	t.Helper()
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured: %v", err)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatalf("unmarshal structured: %v", err)
	}
}
