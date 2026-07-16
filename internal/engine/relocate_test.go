package engine_test

import (
	stdctx "context"
	"errors"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

// driftFinding is a floor-meeting finding whose QuotedCode does NOT exist at the
// reviewed revision (a paraphrased anchor), so the anchorer rejects it (Line==0)
// and drift-drop would discard it: the canonical anchor-recovery candidate.
func driftFinding(rationale string) engine.Finding {
	return engine.Finding{File: "app.go", Severity: "high", Category: "security", Rationale: rationale, QuotedCode: `passwd := "hunter2"`}
}

func recoveryReq(dir string, on bool) engine.Request {
	return engine.Request{Mode: 0, RepoDir: dir, Gate: "high", Extensions: []string{"go"}, AnchorRecovery: on}
}

func TestAnchorRecoveryRescuesDroppedFinding(t *testing.T) {
	line := `password := "hunter2"`
	dir := repairRepo(t, line)
	fa := &fakeAgent{
		findings: []engine.Finding{driftFinding("hardcoded credential")},
		relocate: func(engine.RelocateRequest) (string, error) { return line, nil },
	}
	eng := engine.New(fa, gitcmd.New())
	res, err := eng.Review(stdctx.Background(), recoveryReq(dir, true))
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(fa.relocateCalls) != 1 {
		t.Fatalf("want 1 relocate call, got %d", len(fa.relocateCalls))
	}
	call := fa.relocateCalls[0]
	if call.File != "app.go" || call.QuotedCode != `passwd := "hunter2"` {
		t.Errorf("relocate request must carry the finding's file + failed quote, got %+v", call)
	}
	if !strings.Contains(call.Excerpt, line) {
		t.Errorf("relocate request excerpt must carry the file's diff, got %q", call.Excerpt)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("recovered finding must be kept, got %d findings", len(res.Findings))
	}
	// repairRepo stages: 1 package / 2 blank / 3 Existing / 4 blank / 5 Risky / 6 line / 7 }.
	if res.Findings[0].Line != 6 {
		t.Errorf("recovered finding line = %d, want 6", res.Findings[0].Line)
	}
	if res.Findings[0].QuotedCode != line {
		t.Errorf("recovered quote not committed: %q", res.Findings[0].QuotedCode)
	}
	if res.Stats["findings_dropped"].(float64) != 0 {
		t.Errorf("findings_dropped = %v, want 0 (finding was recovered)", res.Stats["findings_dropped"])
	}
	if res.Stats["findings_recovered"].(float64) != 1 {
		t.Errorf("findings_recovered = %v, want 1", res.Stats["findings_recovered"])
	}
	st := res.Stats["anchor_recovery"].(map[string]any)
	if st["attempted"].(float64) != 1 || st["recovered"].(float64) != 1 {
		t.Errorf("anchor_recovery stats: %+v", st)
	}
}

func TestAnchorRecoveryGarbageReplyStillDropped(t *testing.T) {
	dir := repairRepo(t, `password := "hunter2"`)
	fa := &fakeAgent{
		findings: []engine.Finding{driftFinding("hardcoded credential")},
		relocate: func(engine.RelocateRequest) (string, error) { return "this_line_is_nowhere_in_the_file()", nil },
	}
	eng := engine.New(fa, gitcmd.New())
	res, err := eng.Review(stdctx.Background(), recoveryReq(dir, true))
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(fa.relocateCalls) != 1 {
		t.Fatalf("want 1 relocate call, got %d", len(fa.relocateCalls))
	}
	if len(res.Findings) != 0 {
		t.Fatalf("a still-unanchorable quote must NOT be kept, got %+v", res.Findings)
	}
	if res.Stats["findings_dropped"].(float64) != 1 {
		t.Errorf("findings_dropped = %v, want 1", res.Stats["findings_dropped"])
	}
	if _, ok := res.Stats["findings_recovered"]; ok {
		t.Error("findings_recovered must be omitted when nothing was recovered")
	}
	st := res.Stats["anchor_recovery"].(map[string]any)
	if st["attempted"].(float64) != 1 || st["recovered"].(float64) != 0 {
		t.Errorf("anchor_recovery stats: %+v", st)
	}
}

func TestAnchorRecoveryErrorFallsBack(t *testing.T) {
	dir := repairRepo(t, `password := "hunter2"`)
	fa := &fakeAgent{
		findings: []engine.Finding{driftFinding("hardcoded credential")},
		relocate: func(engine.RelocateRequest) (string, error) { return "", errors.New("boom") },
	}
	eng := engine.New(fa, gitcmd.New())
	res, err := eng.Review(stdctx.Background(), recoveryReq(dir, true))
	if err != nil {
		t.Fatalf("a relocation error must not fail the review: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("errored recovery must leave the finding dropped, got %+v", res.Findings)
	}
	st := res.Stats["anchor_recovery"].(map[string]any)
	if st["attempted"].(float64) != 1 || st["recovered"].(float64) != 0 {
		t.Errorf("anchor_recovery stats: %+v", st)
	}
}

func TestAnchorRecoveryCapRespectedSeverityFirst(t *testing.T) {
	dir := repairRepo(t, `password := "hunter2"`)
	findings := []engine.Finding{
		{File: "app.go", Severity: "medium", Category: "x", Rationale: "m1", QuotedCode: "nope_a()"},
		{File: "app.go", Severity: "critical", Category: "x", Rationale: "c1", QuotedCode: "nope_b()"},
		{File: "app.go", Severity: "high", Category: "x", Rationale: "h1", QuotedCode: "nope_c()"},
		{File: "app.go", Severity: "medium", Category: "x", Rationale: "m2", QuotedCode: "nope_d()"},
	}
	fa := &fakeAgent{
		findings: findings,
		relocate: func(engine.RelocateRequest) (string, error) { return "", nil },
	}
	eng := engine.New(fa, gitcmd.New())
	req := recoveryReq(dir, true)
	req.Gate = "medium"
	res, err := eng.Review(stdctx.Background(), req)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(fa.relocateCalls) != 3 {
		t.Fatalf("cap must limit to 3 calls, got %d", len(fa.relocateCalls))
	}
	var gotSev []string
	for _, c := range fa.relocateCalls {
		gotSev = append(gotSev, c.Severity)
	}
	if gotSev[0] != "critical" || gotSev[1] != "high" || gotSev[2] != "medium" {
		t.Errorf("cap must spend highest-severity-first, got %v", gotSev)
	}
	st := res.Stats["anchor_recovery"].(map[string]any)
	if st["attempted"].(float64) != 3 || st["skipped_cap"].(float64) != 1 {
		t.Errorf("anchor_recovery stats: %+v", st)
	}
}

func TestAnchorRecoverySeverityFloor(t *testing.T) {
	dir := repairRepo(t, `password := "hunter2"`)
	low := driftFinding("low issue")
	low.Severity = "low"
	info := driftFinding("info issue")
	info.Severity = "info"
	fa := &fakeAgent{
		findings: []engine.Finding{low, info},
		relocate: func(engine.RelocateRequest) (string, error) { return `password := "hunter2"`, nil },
	}
	eng := engine.New(fa, gitcmd.New())
	req := recoveryReq(dir, true)
	req.Gate = "low"
	res, err := eng.Review(stdctx.Background(), req)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(fa.relocateCalls) != 0 {
		t.Fatalf("below-floor findings must not trigger recovery, got %d calls", len(fa.relocateCalls))
	}
	if _, ok := res.Stats["anchor_recovery"]; ok {
		t.Error("no attempts => no anchor_recovery stat")
	}
}

func TestAnchorRecoveryOffByteIdentical(t *testing.T) {
	line := `password := "hunter2"`
	dir := repairRepo(t, line)
	fa := &fakeAgent{
		findings: []engine.Finding{driftFinding("hardcoded credential")},
		relocate: func(engine.RelocateRequest) (string, error) { return line, nil },
	}
	eng := engine.New(fa, gitcmd.New())
	res, err := eng.Review(stdctx.Background(), recoveryReq(dir, false))
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(fa.relocateCalls) != 0 {
		t.Fatalf("OFF must make zero relocate calls, got %d", len(fa.relocateCalls))
	}
	if len(res.Findings) != 0 {
		t.Fatalf("OFF must drop the drifted finding as before, got %+v", res.Findings)
	}
	if _, ok := res.Stats["anchor_recovery"]; ok {
		t.Error("OFF must not emit anchor_recovery stat")
	}
	if _, ok := res.Stats["findings_recovered"]; ok {
		t.Error("OFF must not emit findings_recovered stat")
	}
}

func TestAnchorRecoveryMultiLineQuoteSetsEndLine(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "app.go", "package app\n\nfunc Existing() int { return 1 }\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")
	writeFile(t, dir, "app.go", "package app\n\nfunc Existing() int { return 1 }\n\nfunc Risky() {\n\ta := \"1\"\n\tb := \"2\"\n}\n")
	git(t, dir, "add", "app.go")
	f := driftFinding("spans two lines")
	fa := &fakeAgent{
		findings: []engine.Finding{f},
		relocate: func(engine.RelocateRequest) (string, error) { return "a := \"1\"\nb := \"2\"", nil },
	}
	eng := engine.New(fa, gitcmd.New())
	res, err := eng.Review(stdctx.Background(), recoveryReq(dir, true))
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("want 1 recovered finding, got %d", len(res.Findings))
	}
	if res.Findings[0].Line != 6 || res.Findings[0].EndLine != 7 {
		t.Errorf("recovered span = %d-%d, want 6-7", res.Findings[0].Line, res.Findings[0].EndLine)
	}
}

// A trace-enabled review records each recovery attempt/outcome as a live
// "anchor_recovery" trace step (identity + outcome only, no code quotes).
func TestAnchorRecoveryEmitsTraceStep(t *testing.T) {
	line := `password := "hunter2"`
	dir := repairRepo(t, line)
	fa := &fakeAgent{
		findings: []engine.Finding{driftFinding("hardcoded credential")},
		relocate: func(engine.RelocateRequest) (string, error) { return line, nil },
	}
	var recs []engine.AnchorRecoveryRecord
	eng := engine.New(fa, gitcmd.New())
	req := recoveryReq(dir, true)
	req.TraceSink = func(step string, payload any) {
		if step == "anchor_recovery" {
			recs = append(recs, payload.(engine.AnchorRecoveryRecord))
		}
	}
	if _, err := eng.Review(stdctx.Background(), req); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(recs) != 1 || !recs[0].Recovered || recs[0].File != "app.go" || recs[0].Line != 6 {
		t.Fatalf("anchor_recovery trace steps = %+v, want one recovered record for app.go line 6", recs)
	}
}

// The anchor-recovery second pass must be metered: its tokens fold into the
// quota Record and the usage stats, mirroring --patch-repair.
func TestAnchorRecoveryUsageIsMetered(t *testing.T) {
	line := `password := "hunter2"`
	dir := repairRepo(t, line)
	fa := &fakeAgent{
		usage:         engine.Usage{InputTokens: 1000, OutputTokens: 200, CacheReadTokens: 500},
		relocateUsage: engine.Usage{InputTokens: 30, OutputTokens: 10, CacheReadTokens: 5},
		findings:      []engine.Finding{driftFinding("hardcoded credential")},
		relocate:      func(engine.RelocateRequest) (string, error) { return line, nil },
	}
	gate := &fakeGate{}
	eng := engine.New(fa, gitcmd.New())
	req := recoveryReq(dir, true)
	req.Quota = gate
	res, err := eng.Review(stdctx.Background(), req)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	want := engine.Usage{InputTokens: 1030, OutputTokens: 210, CacheReadTokens: 505}
	if gate.recorded != want {
		t.Fatalf("metered usage = %+v, want review+recovery %+v", gate.recorded, want)
	}
	st := res.Stats["anchor_recovery"].(map[string]any)
	if st["input_tokens"].(float64) != 30 || st["output_tokens"].(float64) != 10 {
		t.Fatalf("anchor_recovery must report its own token stat, got %+v", st)
	}
	if res.Stats["total_input_tokens"].(float64) != 1535 { // 1030 uncached + 505 cache-read
		t.Fatalf("total_input_tokens = %v, want 1535", res.Stats["total_input_tokens"])
	}
}
