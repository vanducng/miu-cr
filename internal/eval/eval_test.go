package eval

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseOutputKeepsEnvelopeStats(t *testing.T) {
	parsed, err := parseOutput([]byte(`{"data":{"findings":[{"file":"app.go","line":7}],"stats":{"provider_ms":123,"context_bytes":456}}}`))
	if err != nil {
		t.Fatalf("parseOutput: %v", err)
	}
	if len(parsed.Findings) != 1 || parsed.Findings[0].File != "app.go" {
		t.Fatalf("findings = %+v", parsed.Findings)
	}
	if parsed.Stats["provider_ms"] != float64(123) || parsed.Stats["context_bytes"] != float64(456) {
		t.Fatalf("stats = %+v", parsed.Stats)
	}
}

func TestParseOutputKeepsErrorEnvelopeDetails(t *testing.T) {
	parsed, err := parseOutput([]byte(`{"ok":false,"kind":"error","error":{"code":"review.timeout","message":"review exceeded 15m","hint":"raise --timeout"}}`))
	if err != nil {
		t.Fatalf("parseOutput: %v", err)
	}
	want := "review.timeout: review exceeded 15m (raise --timeout)"
	if parsed.Error != want {
		t.Fatalf("error = %q, want %q", parsed.Error, want)
	}
}

func TestRunTimeoutKillsNestedChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group cleanup is Unix-only")
	}
	suite := Suite{Cases: []Case{{ID: "slow"}}}
	start := time.Now()
	result := Run(context.Background(), suite, []Tool{{Name: "slow", Command: `sh -c 'sleep 2'`}}, 100*time.Millisecond)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("timeout waited for nested child: %s", elapsed)
	}
	if got := result.Tools[0].Summary.FailedCases; got != 1 {
		t.Fatalf("failed cases = %d, want 1", got)
	}
}

func TestScoreCaseMatchesByFileAndLineOverlap(t *testing.T) {
	expected := []Finding{
		{ID: "a", File: "./internal/app.go", Line: 10, EndLine: 12},
		{ID: "b", File: "internal/db.go", Line: 5},
	}
	findings := []Finding{
		{File: "internal/app.go", Line: 11},
		{File: "internal/app.go", Line: 11},
		{File: "internal/other.go", Line: 1},
	}

	score, missed := ScoreCase(expected, findings)
	if score.Matched != 1 || score.Missed != 1 || score.FalsePositive != 2 {
		t.Fatalf("score = %+v, missed=%+v", score, missed)
	}
	if len(missed) != 1 || missed[0].ID != "b" {
		t.Fatalf("missed = %+v, want b", missed)
	}
	if score.Recall != 0.5 {
		t.Fatalf("recall = %v, want 0.5", score.Recall)
	}
}

func TestRunLeavesOmittedExpectedUnscored(t *testing.T) {
	result := Run(context.Background(), Suite{Cases: []Case{{ID: "unlabeled"}}}, []Tool{{Name: "tool", Command: `printf '{"findings":[{"file":"app.go","line":1}]}'`}}, time.Second)
	score := result.Tools[0].Cases[0].Score
	if score.Found != 1 || score.LabeledCases != 0 || score.FalsePositive != 0 || score.F1 != 0 {
		t.Fatalf("score = %+v, want found only", score)
	}
}

func TestRunDoesNotScoreEmptyUnlabeledSuiteAsPerfect(t *testing.T) {
	result := Run(context.Background(), Suite{Cases: []Case{{ID: "unlabeled"}}}, []Tool{{Name: "tool", Command: `printf '{"findings":[]}'`}}, time.Second)
	score := result.Tools[0].Summary
	if score.LabeledCases != 0 || score.Precision != 0 || score.Recall != 0 || score.F1 != 0 {
		t.Fatalf("summary = %+v, want unscored metrics", score)
	}
}

func TestRunRedactsToolStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stderr syntax is Unix-only")
	}
	result := Run(context.Background(), Suite{Cases: []Case{{ID: "stderr"}}}, []Tool{{Name: "tool", Command: `sh -c 'echo sk-ant-do-not-leak-0123456789 >&2; exit 7'`}}, time.Second)
	errText := result.Tools[0].Cases[0].Error
	if strings.Contains(errText, "sk-ant-do-not-leak") {
		t.Fatalf("stderr leaked secret: %q", errText)
	}
	if errText == "" {
		t.Fatal("expected redacted stderr error")
	}
}
