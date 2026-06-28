package cli

import (
	"bytes"
	stdctx "context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/store"
	"github.com/vanducng/miu-cr/internal/store/sqlite"
)

func seededTraceStore(t *testing.T, blob string) (*sqlite.Store, string) {
	t.Helper()
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	id, err := s.SaveReview(stdctx.Background(), store.ReviewRecord{
		Mode:        "staged",
		Status:      "done",
		RepoDir:     "/tmp/repo",
		Findings:    []engine.Finding{{File: "a.go", Line: 1, Severity: "low", Category: "style"}},
		RawPrompt:   "review this diff",
		RawResponse: "no findings",
		TraceJSON:   blob,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return s, id
}

func runTrace(t *testing.T, st store.Store, pretty bool, args ...string) (string, error) {
	t.Helper()
	prev := historyStoreFactory
	SetHistoryStoreFactory(func(stdctx.Context) (store.Store, func(), error) { return st, func() {}, nil })
	t.Cleanup(func() { historyStoreFactory = prev })
	prevPretty := prettyOutput
	prettyOutput = pretty
	t.Cleanup(func() { prettyOutput = prevPretty })

	cmd := traceCommand(&options{output: "json"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	return buf.String(), err
}

func sampleTraceJSON(t *testing.T, tr engine.ReviewTrace) string {
	t.Helper()
	b, err := json.Marshal(tr)
	if err != nil {
		t.Fatalf("marshal trace: %v", err)
	}
	return string(b)
}

func TestTraceShowOrderedSteps(t *testing.T) {
	tr := engine.ReviewTrace{
		SystemPrompt:  "you are a code reviewer",
		UserPrompt:    "diff: --- a/a.go",
		DiffMeta:      engine.DiffMeta{BaseSHA: "abc", HeadSHA: "def", Source: "staged"},
		SelectedFiles: []string{"a.go"},
		InjectedRules: []engine.RuleRef{{Stem: "go", Provenance: "built-in"}},
		Provider:      "anthropic",
		Model:         "claude",
		FinalResponse: "no findings",
		Turns:         []engine.TurnRecord{{Turn: 1, Tool: "grep", Args: "func"}},
	}
	st, id := seededTraceStore(t, sampleTraceJSON(t, tr))

	out, err := runTrace(t, st, false, id)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	env := decodeEnv(t, out)
	if env.Kind != "trace.show" {
		t.Fatalf("kind: want trace.show, got %q", env.Kind)
	}
	data := env.Data.(map[string]any)
	if data["id"] != id {
		t.Fatalf("id mismatch: %v", data["id"])
	}
	steps, _ := data["steps"].([]any)
	want := []string{"system_prompt", "diff_meta", "selected_files", "injected_rules", "user_prompt", "model", "final_response", "tool_calls"}
	if len(steps) != len(want) {
		t.Fatalf("want %d steps, got %d: %v", len(want), len(steps), steps)
	}
	for i, w := range want {
		got := steps[i].(map[string]any)["step"]
		if got != w {
			t.Fatalf("step %d: want %q, got %q", i, w, got)
		}
	}
	// the system prompt (the headline gap) is present in the rendered trace.
	if !strings.Contains(out, "you are a code reviewer") {
		t.Fatalf("system prompt missing from trace.show: %s", out)
	}
}

// Proves the pretty renderer's []engine.TurnRecord case is REACHABLE, the
// historical tool_calls step renders its turns (not just the JSON path).
func TestTraceShowPrettyRendersToolCalls(t *testing.T) {
	tr := engine.ReviewTrace{
		SystemPrompt: "sys",
		Turns:        []engine.TurnRecord{{Turn: 1, Tool: "grep", Args: "needleArg"}},
	}
	st, id := seededTraceStore(t, sampleTraceJSON(t, tr))
	out, err := runTrace(t, st, true, id) // pretty
	if err != nil {
		t.Fatalf("trace -o pretty: %v", err)
	}
	if !strings.Contains(out, "grep") || !strings.Contains(out, "needleArg") {
		t.Fatalf("tool_calls turn not rendered in pretty trace (TurnRecord case unreachable?):\n%s", out)
	}
}

func TestTraceShowRedactedTokenNeverAppears(t *testing.T) {
	// A trace whose free-text already passed redactTrace at persist; assert the
	// rendered view carries no secret-shaped token even if one slipped in.
	const tok = "sk-ant-deadbeefdeadbeefdeadbeefdeadbeef"
	tr := engine.ReviewTrace{
		SystemPrompt:  "you are a reviewer",
		UserPrompt:    "diff with " + tok + " inside",
		SelectedFiles: []string{"a.go"},
	}
	// Mirror the engine's persist-time free-text redaction (config.RedactString)
	// so the stored blob is realistic.
	tr.UserPrompt = config.RedactString(tr.UserPrompt)
	st, id := seededTraceStore(t, sampleTraceJSON(t, tr))

	out, err := runTrace(t, st, false, id)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	if strings.Contains(out, tok) {
		t.Fatalf("redacted token leaked into trace view: %s", out)
	}
	if strings.Contains(out, "sk-ant") {
		t.Fatalf("token prefix leaked into trace view: %s", out)
	}
}

func TestTraceShowMissingID(t *testing.T) {
	st, _ := seededTraceStore(t, "")
	_, err := runTrace(t, st, false, "does-not-exist")
	if err == nil {
		t.Fatal("unknown id must error")
	}
	var ce *CLIError
	if !errors.As(err, &ce) || ce.Code != "trace.not_found" {
		t.Fatalf("want trace.not_found, got %v", err)
	}
}

func TestTraceResolvesPRRef(t *testing.T) {
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	id, err := s.SaveReview(stdctx.Background(), store.ReviewRecord{
		Mode: "pr", Status: "done", Owner: "acme", Repo: "widget", Number: 7,
		TraceJSON: `{"system_prompt":"hi"}`,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	out, err := runTrace(t, s, false, "acme/widget#7")
	if err != nil {
		t.Fatalf("trace by ref: %v", err)
	}
	if !strings.Contains(out, id) {
		t.Fatalf("ref did not resolve to review %s:\n%s", id, out)
	}
	if _, err := runTrace(t, s, false, "acme/widget#999"); err == nil {
		t.Fatal("unknown PR ref must error")
	}
}

// sinkingReviewer fires req.TraceSink (when set) with two ordered steps before
// returning, simulating the engine's live capture seams.
type sinkingReviewer struct{ sawSink bool }

func (s *sinkingReviewer) Review(_ stdctx.Context, req ReviewRequest) (ReviewOutcome, error) {
	if req.TraceSink != nil {
		s.sawSink = true
		req.TraceSink("system_prompt", "you are a reviewer")
		req.TraceSink("user_prompt", "diff: --- a/a.go")
	}
	return ReviewOutcome{Findings: []ReviewFinding{}, Stats: map[string]any{}}, nil
}

func (s *sinkingReviewer) GateFailed([]ReviewFinding, string) bool { return false }

// runReviewSplit runs `review` with separate stdout/stderr so --trace NDJSON
// (stderr) can be asserted independently of the stdout result envelope.
func runReviewSplit(t *testing.T, r Reviewer, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "synthetic-test-key")
	prev := reviewer
	SetReviewer(r)
	t.Cleanup(func() { SetReviewer(prev) })
	prevFmt := outputFormat
	t.Cleanup(func() { outputFormat = prevFmt })
	prettyOutput = false
	outputFormat = "json"

	cmd := reviewCommand(&options{output: "json"})
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	e := cmd.Execute()
	return out.String(), errb.String(), e
}

func TestReviewTraceFlagStreamsNDJSONToStderr(t *testing.T) {
	r := &sinkingReviewer{}
	stdout, stderr, err := runReviewSplit(t, r, "--staged", "--gate", "none", "--trace")
	if err != nil {
		t.Fatalf("review --trace: %v", err)
	}
	if !r.sawSink {
		t.Fatal("--trace did not wire a TraceSink onto the request")
	}
	// stdout is the result envelope, unchanged + no NDJSON.
	if !strings.Contains(stdout, `"kind":"review.result"`) {
		t.Fatalf("stdout result envelope missing: %s", stdout)
	}
	if strings.Contains(stdout, `"step":`) {
		t.Fatalf("NDJSON leaked into stdout: %s", stdout)
	}
	// stderr carries one NDJSON line per step.
	lines := nonEmptyLines(stderr)
	stepLines := 0
	for _, l := range lines {
		var s traceStep
		if json.Unmarshal([]byte(l), &s) == nil && s.Step != "" {
			stepLines++
		}
	}
	if stepLines != 2 {
		t.Fatalf("want 2 NDJSON step lines on stderr, got %d: %q", stepLines, stderr)
	}
}

func TestReviewNoTraceFlagNoNDJSON(t *testing.T) {
	r := &sinkingReviewer{}
	stdout, stderr, err := runReviewSplit(t, r, "--staged", "--gate", "none")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if r.sawSink {
		t.Fatal("TraceSink wired without --trace")
	}
	if strings.Contains(stderr, `"step":`) {
		t.Fatalf("NDJSON emitted without --trace: %s", stderr)
	}
	if !strings.Contains(stdout, `"kind":"review.result"`) {
		t.Fatalf("stdout result envelope missing: %s", stdout)
	}
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func TestTraceShowEmptyBlob(t *testing.T) {
	// An old review (no trace_json) renders cleanly, not a panic.
	st, id := seededTraceStore(t, "")
	out, err := runTrace(t, st, true, id)
	if err != nil {
		t.Fatalf("trace empty: %v", err)
	}
	if !strings.Contains(out, "no trace recorded") {
		t.Fatalf("want graceful empty render, got %s", out)
	}
}
