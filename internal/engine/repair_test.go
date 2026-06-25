package engine_test

import (
	stdctx "context"
	"errors"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

// repairRepo stages a one-line change whose new-file line is exactly `line` so a
// finding anchored to it (QuotedCode == line) survives drift-reject and the repair
// span is `line`. Returns the repo dir.
func repairRepo(t *testing.T, line string) string {
	t.Helper()
	dir := initRepo(t)
	writeFile(t, dir, "app.go", "package app\n\nfunc Existing() int { return 1 }\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")
	writeFile(t, dir, "app.go", "package app\n\nfunc Existing() int { return 1 }\n\nfunc Risky() {\n\t"+line+"\n}\n")
	git(t, dir, "add", "app.go")
	return dir
}

// emptyPatchFinding is a floor-meeting single-line finding with NO SuggestedPatch:
// classify rejects it as "empty" (repairable), the canonical repair candidate.
func emptyPatchFinding(quoted string) engine.Finding {
	return engine.Finding{File: "app.go", Severity: "high", Category: "security", Rationale: "hardcoded credential", QuotedCode: quoted}
}

func repairReq(dir string, on bool, post bool) engine.Request {
	return engine.Request{Mode: 0, RepoDir: dir, Gate: "high", Extensions: []string{"go"}, PatchRepair: on, Post: post}
}

func TestRepairOffByteIdentical(t *testing.T) {
	line := `password := "hunter2"`
	dir := repairRepo(t, line)
	fa := &fakeAgent{
		findings: []engine.Finding{emptyPatchFinding(line)},
		repair:   func(engine.RepairRequest) (string, error) { return `password := os.Getenv("PW")`, nil },
	}
	eng := engine.New(fa, gitcmd.New())
	res, err := eng.Review(stdctx.Background(), repairReq(dir, false, true))
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(fa.repairCalls) != 0 {
		t.Fatalf("OFF must make zero repair calls, got %d", len(fa.repairCalls))
	}
	if res.Findings[0].SuggestedPatch != "" {
		t.Errorf("OFF must not mutate SuggestedPatch: %q", res.Findings[0].SuggestedPatch)
	}
	if _, ok := res.Stats["patch_repair"]; ok {
		t.Error("OFF must not emit patch_repair stat")
	}
}

func TestRepairDryRunNoCalls(t *testing.T) {
	line := `password := "hunter2"`
	dir := repairRepo(t, line)
	fa := &fakeAgent{
		findings: []engine.Finding{emptyPatchFinding(line)},
		repair:   func(engine.RepairRequest) (string, error) { return `password := os.Getenv("PW")`, nil },
	}
	eng := engine.New(fa, gitcmd.New())
	if _, err := eng.Review(stdctx.Background(), repairReq(dir, true, false)); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(fa.repairCalls) != 0 {
		t.Fatalf("dry-run (Post=false) must make zero repair calls, got %d", len(fa.repairCalls))
	}
}

func TestRepairEmptyPatchRepaired(t *testing.T) {
	line := `password := "hunter2"`
	dir := repairRepo(t, line)
	fixed := `password := os.Getenv("PW")`
	fa := &fakeAgent{
		findings: []engine.Finding{emptyPatchFinding(line)},
		repair:   func(engine.RepairRequest) (string, error) { return fixed, nil },
	}
	eng := engine.New(fa, gitcmd.New())
	res, err := eng.Review(stdctx.Background(), repairReq(dir, true, true))
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(fa.repairCalls) != 1 {
		t.Fatalf("want 1 repair call, got %d", len(fa.repairCalls))
	}
	if fa.repairCalls[0].Span == "" {
		t.Error("repair request must carry the verbatim span")
	}
	if res.Findings[0].SuggestedPatch != fixed {
		t.Errorf("SuggestedPatch not committed: %q", res.Findings[0].SuggestedPatch)
	}
	st := res.Stats["patch_repair"].(map[string]any)
	if st["attempted"].(float64) != 1 || st["repaired"].(float64) != 1 {
		t.Errorf("stats: %+v", st)
	}
}

func TestRepairJunkReplyFallsBack(t *testing.T) {
	line := `password := "hunter2"`
	dir := repairRepo(t, line)
	fa := &fakeAgent{
		findings: []engine.Finding{emptyPatchFinding(line)},
		// no-op reply == the raw line → re-validation rejects (reason no_op).
		repair: func(engine.RepairRequest) (string, error) { return line, nil },
	}
	eng := engine.New(fa, gitcmd.New())
	res, err := eng.Review(stdctx.Background(), repairReq(dir, true, true))
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if res.Findings[0].SuggestedPatch != "" {
		t.Errorf("junk reply must NOT be committed: %q", res.Findings[0].SuggestedPatch)
	}
	st := res.Stats["patch_repair"].(map[string]any)
	if st["attempted"].(float64) != 1 || st["repaired"].(float64) != 0 {
		t.Errorf("stats: %+v", st)
	}
}

func TestRepairErrorFallsBack(t *testing.T) {
	line := `password := "hunter2"`
	dir := repairRepo(t, line)
	fa := &fakeAgent{
		findings: []engine.Finding{emptyPatchFinding(line)},
		repair:   func(engine.RepairRequest) (string, error) { return "", errors.New("boom") },
	}
	eng := engine.New(fa, gitcmd.New())
	res, err := eng.Review(stdctx.Background(), repairReq(dir, true, true))
	if err != nil {
		t.Fatalf("repair error must not fail the review: %v", err)
	}
	if res.Findings[0].SuggestedPatch != "" {
		t.Errorf("error must fall back to original: %q", res.Findings[0].SuggestedPatch)
	}
	st := res.Stats["patch_repair"].(map[string]any)
	if st["repaired"].(float64) != 0 {
		t.Errorf("stats: %+v", st)
	}
}

func TestRepairBelowFloorSkipped(t *testing.T) {
	line := `password := "hunter2"`
	dir := repairRepo(t, line)
	f := emptyPatchFinding(line)
	f.Severity = "low"
	fa := &fakeAgent{
		findings: []engine.Finding{f},
		repair:   func(engine.RepairRequest) (string, error) { return `password := os.Getenv("PW")`, nil },
	}
	eng := engine.New(fa, gitcmd.New())
	// gate low so the low finding survives the gate but is below the repair floor.
	if _, err := eng.Review(stdctx.Background(), engine.Request{Mode: 0, RepoDir: dir, Gate: "low", Extensions: []string{"go"}, PatchRepair: true, Post: true}); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(fa.repairCalls) != 0 {
		t.Fatalf("below-floor finding must not trigger repair, got %d calls", len(fa.repairCalls))
	}
}

func TestRepairCleanPatchNotRepaired(t *testing.T) {
	line := `password := "hunter2"`
	dir := repairRepo(t, line)
	f := emptyPatchFinding(line)
	f.SuggestedPatch = `password := os.Getenv("PW")` // already clean (reason ok)
	fa := &fakeAgent{
		findings: []engine.Finding{f},
		repair:   func(engine.RepairRequest) (string, error) { return "SHOULD NOT BE CALLED", nil },
	}
	eng := engine.New(fa, gitcmd.New())
	res, err := eng.Review(stdctx.Background(), repairReq(dir, true, true))
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(fa.repairCalls) != 0 {
		t.Fatalf("clean patch must not be repaired, got %d calls", len(fa.repairCalls))
	}
	if res.Findings[0].SuggestedPatch != `password := os.Getenv("PW")` {
		t.Errorf("clean patch mutated: %q", res.Findings[0].SuggestedPatch)
	}
}

func TestRepairAnchorMismatchSkipped(t *testing.T) {
	line := `password := "hunter2"`
	dir := repairRepo(t, line)
	f := emptyPatchFinding(line)
	f.SuggestedPatch = `password := os.Getenv("PW")`
	f.QuotedCode = `totally := "different"` // anchor disagrees with the file → anchor_mismatch
	fa := &fakeAgent{
		findings: []engine.Finding{f},
		repair:   func(engine.RepairRequest) (string, error) { return "x", nil },
	}
	eng := engine.New(fa, gitcmd.New())
	if _, err := eng.Review(stdctx.Background(), repairReq(dir, true, true)); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(fa.repairCalls) != 0 {
		t.Fatalf("anchor_mismatch is not repairable: want 0 calls, got %d", len(fa.repairCalls))
	}
}

func TestRepairFencedSpanSkipped(t *testing.T) {
	line := "x := \"```\""
	dir := repairRepo(t, line)
	fa := &fakeAgent{
		findings: []engine.Finding{emptyPatchFinding(line)},
		repair:   func(engine.RepairRequest) (string, error) { return "y := 1", nil },
	}
	eng := engine.New(fa, gitcmd.New())
	if _, err := eng.Review(stdctx.Background(), repairReq(dir, true, true)); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(fa.repairCalls) != 0 {
		t.Fatalf("span containing ``` must be skipped, got %d calls", len(fa.repairCalls))
	}
}

func TestRepairMultiLineSkipped(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "app.go", "package app\n\nfunc Existing() int { return 1 }\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")
	// two adjacent changed lines; the finding spans both (EndLine != Line).
	writeFile(t, dir, "app.go", "package app\n\nfunc Existing() int { return 1 }\n\nfunc Risky() {\n\ta := \"1\"\n\tb := \"2\"\n}\n")
	git(t, dir, "add", "app.go")
	f := engine.Finding{File: "app.go", Severity: "high", Category: "security", Rationale: "spans two lines", QuotedCode: "a := \"1\"\nb := \"2\""}
	fa := &fakeAgent{
		findings: []engine.Finding{f},
		repair:   func(engine.RepairRequest) (string, error) { return "z := 9", nil },
	}
	eng := engine.New(fa, gitcmd.New())
	res, err := eng.Review(stdctx.Background(), repairReq(dir, true, true))
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	// Guard: the finding must actually have anchored as a multi-line span, else the
	// test would pass for the wrong reason (e.g. dropped at the gate, not the EndLine check).
	if len(res.Findings) != 1 || res.Findings[0].EndLine == res.Findings[0].Line {
		t.Fatalf("setup: want one multi-line finding (EndLine != Line), got %+v", res.Findings)
	}
	if len(fa.repairCalls) != 0 {
		t.Fatalf("multi-line finding (EndLine != Line) must not be repaired in V1, got %d calls", len(fa.repairCalls))
	}
}

func TestRepairCapRespectedSeverityFirst(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "app.go", "package app\n\nfunc Existing() int { return 1 }\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")
	// three distinct repairable lines, each anchored.
	writeFile(t, dir, "app.go", "package app\n\nfunc Existing() int { return 1 }\n\nfunc Risky() {\n\ta := \"1\"\n\tb := \"2\"\n\tc := \"3\"\n}\n")
	git(t, dir, "add", "app.go")
	findings := []engine.Finding{
		{File: "app.go", Severity: "medium", Category: "x", Rationale: "r", QuotedCode: `a := "1"`},
		{File: "app.go", Severity: "critical", Category: "x", Rationale: "r", QuotedCode: `b := "2"`},
		{File: "app.go", Severity: "high", Category: "x", Rationale: "r", QuotedCode: `c := "3"`},
	}
	fa := &fakeAgent{
		findings: findings,
		repair:   func(engine.RepairRequest) (string, error) { return "z := 9", nil },
	}
	eng := engine.New(fa, gitcmd.New())
	res, err := eng.Review(stdctx.Background(), engine.Request{Mode: 0, RepoDir: dir, Gate: "medium", Extensions: []string{"go"}, PatchRepair: true, Post: true, MaxRepair: 2})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(fa.repairCalls) != 2 {
		t.Fatalf("cap=2 must limit calls, got %d", len(fa.repairCalls))
	}
	// highest-severity-first: critical then high get the budget; medium is skipped.
	gotSev := map[string]bool{}
	for _, c := range fa.repairCalls {
		gotSev[c.Severity] = true
	}
	if !gotSev["critical"] || !gotSev["high"] || gotSev["medium"] {
		t.Errorf("cap must prefer highest severity, got calls for %v", gotSev)
	}
	st := res.Stats["patch_repair"].(map[string]any)
	if st["attempted"].(float64) != 2 || st["skipped_cap"].(float64) != 1 {
		t.Errorf("stats: %+v", st)
	}
}
