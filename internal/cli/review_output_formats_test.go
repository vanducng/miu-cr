package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runReviewFmt runs the review command with outputFormat forced (the root
// PersistentPreRunE that normally sets it doesn't run when reviewCommand is built
// directly in tests).
func runReviewFmt(t *testing.T, format string, r Reviewer, args ...string) (string, error) {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "synthetic-test-key")
	prev := reviewer
	SetReviewer(r)
	t.Cleanup(func() { SetReviewer(prev) })
	prevFmt := outputFormat
	t.Cleanup(func() { outputFormat = prevFmt })
	outputFormat = format

	opts := &options{output: format}
	cmd := reviewCommand(opts)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	return buf.String(), err
}

func TestReviewSARIFOutput(t *testing.T) {
	r := &fakeReviewer{outcome: ReviewOutcome{Findings: []ReviewFinding{
		{File: "src/a.go", Line: 3, EndLine: 4, Severity: "medium", Category: "bug", Rationale: "leak", QuotedCode: "f()", SuggestedPatch: "g()"},
	}}}
	out, _ := runReviewFmt(t, "sarif", r, "--staged", "--gate", "none")

	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("sarif output is not valid JSON: %v\n%s", err, out)
	}
	if m["version"] != "2.1.0" {
		t.Fatalf("want SARIF 2.1.0, got %v", m["version"])
	}
	// The miucr.cli/v1 envelope must NOT appear in SARIF output.
	if strings.Contains(out, "miucr.cli/v1") || strings.Contains(out, `"ok":true`) {
		t.Fatalf("envelope leaked into SARIF output: %s", out)
	}
	res := m["runs"].([]any)[0].(map[string]any)["results"].([]any)
	if len(res) != 1 {
		t.Fatalf("want 1 result, got %d", len(res))
	}
}

func TestReviewSARIFRepoRelativePath(t *testing.T) {
	r := &fakeReviewer{outcome: ReviewOutcome{Findings: []ReviewFinding{
		{File: "/abs/leak/path.go", Line: 1, Severity: "low", Category: "x"},
	}}}
	out, _ := runReviewFmt(t, "sarif", r, "--staged", "--gate", "none")
	if strings.Contains(out, `"uri": "/abs`) {
		t.Fatalf("absolute path leaked into SARIF uri: %s", out)
	}
	if !strings.Contains(out, `"uri": "abs/leak/path.go"`) {
		t.Fatalf("expected repo-relative uri, got: %s", out)
	}
}

func TestReviewInvalidFilterMode(t *testing.T) {
	r := &fakeReviewer{outcome: ReviewOutcome{}}
	_, err := runReviewFmt(t, "json", r, "--staged", "--filter-mode", "bogus")
	if err == nil {
		t.Fatal("want error for invalid --filter-mode")
	}
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "flags.invalid_filter_mode" {
		t.Fatalf("want flags.invalid_filter_mode, got %+v", err)
	}
}

func TestReviewSARIFOutWritesFileOnSuccess(t *testing.T) {
	r := &fakeReviewer{outcome: ReviewOutcome{Findings: []ReviewFinding{
		{File: "src/a.go", Line: 3, Severity: "high", Category: "bug", Rationale: "leak"},
	}}}
	path := filepath.Join(t.TempDir(), "out.sarif")
	// Default -o json: the envelope goes to stdout, SARIF goes to the file.
	out, err := runReviewFmt(t, "json", r, "--staged", "--gate", "none", "--sarif-out", path)
	if err != nil {
		t.Fatalf("review with --sarif-out failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "miucr.cli/v1") {
		t.Fatalf("stdout should stay the JSON envelope, got: %s", out)
	}
	b, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatalf("SARIF file not written: %v", rerr)
	}
	var m map[string]any
	if jerr := json.Unmarshal(b, &m); jerr != nil {
		t.Fatalf("SARIF file is not valid JSON: %v\n%s", jerr, b)
	}
	if m["version"] != "2.1.0" {
		t.Fatalf("want SARIF 2.1.0 in file, got %v", m["version"])
	}
	res := m["runs"].([]any)[0].(map[string]any)["results"].([]any)
	if len(res) != 1 {
		t.Fatalf("want 1 result in SARIF file, got %d", len(res))
	}
}

func TestReviewSARIFOutNoFileOnError(t *testing.T) {
	r := &fakeReviewer{err: errors.New("synthetic review failure")}
	path := filepath.Join(t.TempDir(), "out.sarif")
	_, err := runReviewFmt(t, "json", r, "--staged", "--gate", "none", "--sarif-out", path)
	if err == nil {
		t.Fatal("want error from failed review")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("a failed review must leave no SARIF file, stat err = %v", statErr)
	}
}

func TestReviewValidFilterModeThreaded(t *testing.T) {
	r := &fakeReviewer{outcome: ReviewOutcome{}}
	if _, err := runReviewFmt(t, "json", r, "--staged", "--filter-mode", "added", "--gate", "none"); err != nil {
		t.Fatalf("valid --filter-mode rejected: %v", err)
	}
	if r.gotReq.FilterMode != "added" {
		t.Fatalf("filter-mode not threaded into request: %q", r.gotReq.FilterMode)
	}
}

func TestReviewInvalidMinSeverity(t *testing.T) {
	r := &fakeReviewer{outcome: ReviewOutcome{}}
	_, err := runReviewFmt(t, "json", r, "--staged", "--min-severity", "bogus")
	if err == nil {
		t.Fatal("want error for invalid --min-severity")
	}
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "flags.invalid_min_severity" {
		t.Fatalf("want flags.invalid_min_severity, got %+v", err)
	}
	if ce.Exit != 2 {
		t.Fatalf("want exit 2 for invalid --min-severity, got %d", ce.Exit)
	}
}

func TestReviewInvalidFormat(t *testing.T) {
	r := &fakeReviewer{outcome: ReviewOutcome{}}
	_, err := runReviewFmt(t, "json", r, "--staged", "--format", "fancy")
	if err == nil {
		t.Fatal("want error for invalid --format")
	}
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "flags.invalid_format" {
		t.Fatalf("want flags.invalid_format, got %+v", err)
	}
	if ce.Exit != 2 {
		t.Fatalf("want exit 2 for invalid --format, got %d", ce.Exit)
	}
}

func TestReviewValidFormatAccepted(t *testing.T) {
	r := &fakeReviewer{outcome: ReviewOutcome{}}
	if _, err := runReviewFmt(t, "json", r, "--staged", "--format", "minimal", "--gate", "none"); err != nil {
		t.Fatalf("valid --format rejected: %v", err)
	}
}

func TestReviewWalkthroughDiagramThreaded(t *testing.T) {
	r := &fakeReviewer{outcome: ReviewOutcome{}}
	if _, err := runReviewFmt(t, "json", r, "--staged", "--walkthrough-diagram", "--gate", "none"); err != nil {
		t.Fatalf("--walkthrough-diagram rejected: %v", err)
	}
	if !r.gotReq.WantDiagram {
		t.Fatal("--walkthrough-diagram not threaded into request")
	}
	// Default off.
	r2 := &fakeReviewer{outcome: ReviewOutcome{}}
	if _, err := runReviewFmt(t, "json", r2, "--staged", "--gate", "none"); err != nil {
		t.Fatalf("default review rejected: %v", err)
	}
	if r2.gotReq.WantDiagram {
		t.Fatal("WantDiagram must default off")
	}
}
