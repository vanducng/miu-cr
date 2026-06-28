package cli

import (
	"bytes"
	stdctx "context"
	"encoding/json"
	"testing"
)

type fakePRReviewer struct {
	outcome ReviewOutcome
	gotReq  PRReviewRequest
}

func (f *fakePRReviewer) ReviewPR(_ stdctx.Context, req PRReviewRequest) (ReviewOutcome, error) {
	f.gotReq = req
	return f.outcome, nil
}

func (f *fakePRReviewer) GateFailed(findings []ReviewFinding, gate string) bool {
	if gate == "" || gate == "none" {
		return false
	}
	rank := map[string]int{"info": 1, "low": 2, "medium": 3, "high": 4, "critical": 5}
	max := 0
	for _, fn := range findings {
		if r := rank[fn.Severity]; r > max {
			max = r
		}
	}
	return max >= rank[gate]
}

// stubGateReviewer is a local-mode Reviewer whose gate verdict is fixed, used to
// prove the --pr gate is evaluated from the PRReviewer, not this instance.
type stubGateReviewer struct{ gateFailed bool }

func (s *stubGateReviewer) Review(stdctx.Context, ReviewRequest) (ReviewOutcome, error) {
	return ReviewOutcome{}, nil
}
func (s *stubGateReviewer) GateFailed([]ReviewFinding, string) bool { return s.gateFailed }

func runPR(t *testing.T, pr PRReviewer, rev Reviewer, args ...string) (string, error) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	return runPRCommand(t, pr, rev, args...)
}

func runPRKeepHome(t *testing.T, pr PRReviewer, rev Reviewer, args ...string) (string, error) {
	t.Helper()
	return runPRCommand(t, pr, rev, args...)
}

func runPRCommand(t *testing.T, pr PRReviewer, rev Reviewer, args ...string) (string, error) {
	t.Helper()
	prevPR, prevRev := prReviewer, reviewer
	SetPRReviewer(pr)
	SetReviewer(rev)
	t.Cleanup(func() { SetPRReviewer(prevPR); SetReviewer(prevRev) })
	prettyOutput = false

	opts := &options{output: "json"}
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

func TestPRDryRunEmitsFindingsAndPRBlock(t *testing.T) {
	pr := &fakePRReviewer{outcome: ReviewOutcome{
		Findings: []ReviewFinding{{File: "a.go", Line: 3, Severity: "low"}},
		Stats:    map[string]any{"truncation_level": "full"},
		PR:       &PRResult{Owner: "vanducng", Repo: "miu-cr", Number: 7, HeadSHA: "deadbeef", IsFork: false},
	}}
	out, err := runPR(t, pr, &fakeReviewer{}, "--pr", "vanducng/miu-cr#7", "--no-post", "--gate", "high")
	if err != nil {
		t.Fatalf("dry-run must not error: %v", err)
	}
	var env Envelope
	if e := json.Unmarshal([]byte(out), &env); e != nil {
		t.Fatalf("invalid envelope: %v\n%s", e, out)
	}
	if !env.OK {
		t.Fatal("want ok=true")
	}
	data, _ := env.Data.(map[string]any)
	if _, ok := data["findings"].([]any); !ok {
		t.Errorf("missing data.findings: %v", data)
	}
	prBlock, ok := data["pr"].(map[string]any)
	if !ok {
		t.Fatalf("missing data.pr block: %v", data)
	}
	if prBlock["head_sha"] != "deadbeef" || prBlock["number"].(float64) != 7 {
		t.Errorf("bad pr block: %v", prBlock)
	}
	if pr.gotReq.Post {
		t.Error("--no-post must not request posting")
	}
}

func TestPRFormatThreadedToRequest(t *testing.T) {
	pr := &fakePRReviewer{outcome: ReviewOutcome{PR: &PRResult{Owner: "o", Repo: "r", Number: 1}}}
	if _, err := runPR(t, pr, &fakeReviewer{}, "--pr", "o/r#1", "--no-post", "--format", "minimal"); err != nil {
		t.Fatalf("--format minimal rejected on PR path: %v", err)
	}
	if pr.gotReq.Format != "minimal" {
		t.Fatalf("--format not threaded into PRReviewRequest: %q", pr.gotReq.Format)
	}
	// Default is full (empty threads through; renderer resolves "" → full).
	pr2 := &fakePRReviewer{outcome: ReviewOutcome{PR: &PRResult{Owner: "o", Repo: "r", Number: 2}}}
	if _, err := runPR(t, pr2, &fakeReviewer{}, "--pr", "o/r#2", "--no-post"); err != nil {
		t.Fatalf("default PR review rejected: %v", err)
	}
	if pr2.gotReq.Format != "" {
		t.Fatalf("Format must default empty (= full), got %q", pr2.gotReq.Format)
	}
}

func TestPRSkippedUnchangedEnvelope(t *testing.T) {
	pr := &fakePRReviewer{outcome: ReviewOutcome{
		SkippedUnchanged: true,
		PriorReviewID:    "prior-9",
		PR:               &PRResult{Owner: "o", Repo: "r", Number: 1, HeadSHA: "abc", SummaryAction: "none"},
	}}
	out, err := runPR(t, pr, &fakeReviewer{}, "--pr", "o/r#1", "--no-post")
	if err != nil {
		t.Fatalf("skip must exit 0: %v", err)
	}
	var env Envelope
	if e := json.Unmarshal([]byte(out), &env); e != nil {
		t.Fatalf("invalid envelope: %v\n%s", e, out)
	}
	data, _ := env.Data.(map[string]any)
	if data["skipped_unchanged"] != true {
		t.Fatalf("want data.skipped_unchanged=true, got %v", data["skipped_unchanged"])
	}
	if data["prior_review_id"] != "prior-9" {
		t.Fatalf("want data.prior_review_id=prior-9, got %v", data["prior_review_id"])
	}
	// Fix: the skip path must emit findings as an empty ARRAY (not null) and stats
	// as an object, a consumer expecting an array/object shape must not break.
	f, ok := data["findings"]
	if !ok || f == nil {
		t.Fatalf("skip envelope must carry findings as [] (not null/absent), got %#v", f)
	}
	if arr, isArr := f.([]any); !isArr || len(arr) != 0 {
		t.Fatalf("skip envelope findings must be an empty array, got %#v", f)
	}
	if s, ok := data["stats"]; !ok {
		t.Fatalf("skip envelope must carry a stats object, got absent (%v)", data)
	} else if _, isObj := s.(map[string]any); !isObj {
		t.Fatalf("skip envelope stats must be an object, got %#v", s)
	}
}

// A normal (non-skipped) review must NOT carry the additive skip fields.
func TestPRNormalRunOmitsSkipFields(t *testing.T) {
	pr := &fakePRReviewer{outcome: ReviewOutcome{
		Findings: []ReviewFinding{},
		Stats:    map[string]any{},
		PR:       &PRResult{Owner: "o", Repo: "r", Number: 1, HeadSHA: "abc"},
	}}
	out, err := runPR(t, pr, &fakeReviewer{}, "--pr", "o/r#1", "--no-post")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var env Envelope
	if e := json.Unmarshal([]byte(out), &env); e != nil {
		t.Fatalf("invalid envelope: %v\n%s", e, out)
	}
	data, _ := env.Data.(map[string]any)
	if _, ok := data["skipped_unchanged"]; ok {
		t.Fatalf("normal run must omit skipped_unchanged: %v", data)
	}
	if _, ok := data["prior_review_id"]; ok {
		t.Fatalf("normal run must omit prior_review_id: %v", data)
	}
}

func TestPRForceFlagThreaded(t *testing.T) {
	pr := &fakePRReviewer{outcome: ReviewOutcome{PR: &PRResult{Owner: "o", Repo: "r", Number: 1}}}
	if _, err := runPR(t, pr, &fakeReviewer{}, "--pr", "o/r#1", "--no-post", "--force"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !pr.gotReq.Force {
		t.Fatal("--force must thread Force=true into the PR request")
	}
}

func TestPRMinSeverityAndDiagramThreaded(t *testing.T) {
	pr := &fakePRReviewer{outcome: ReviewOutcome{PR: &PRResult{Owner: "o", Repo: "r", Number: 1}}}
	if _, err := runPR(t, pr, &fakeReviewer{}, "--pr", "o/r#1", "--no-post", "--min-severity", "high", "--walkthrough-diagram"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if pr.gotReq.MinSeverity != "high" {
		t.Fatalf("--min-severity not threaded: %q", pr.gotReq.MinSeverity)
	}
	if !pr.gotReq.WantDiagram {
		t.Fatal("--walkthrough-diagram must thread WantDiagram into the PR request")
	}
}

func TestPRDeepContextThreaded(t *testing.T) {
	pr := &fakePRReviewer{outcome: ReviewOutcome{PR: &PRResult{Owner: "o", Repo: "r", Number: 1}}}
	if _, err := runPR(t, pr, &fakeReviewer{}, "--pr", "o/r#1", "--no-post", "--deep-context"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !pr.gotReq.DeepContext {
		t.Fatal("--deep-context must thread DeepContext into the PR request")
	}
	if !pr.gotReq.ContextHopsAuto || pr.gotReq.ContextHops != 0 {
		t.Fatalf("--deep-context auto=%v hops=%d, want auto true hops 0", pr.gotReq.ContextHopsAuto, pr.gotReq.ContextHops)
	}

	pr = &fakePRReviewer{outcome: ReviewOutcome{PR: &PRResult{Owner: "o", Repo: "r", Number: 1}}}
	if _, err := runPR(t, pr, &fakeReviewer{}, "--pr", "o/r#1", "--no-post", "--deep-context", "--context-hops", "4"); err != nil {
		t.Fatalf("run explicit: %v", err)
	}
	if pr.gotReq.ContextHopsAuto || pr.gotReq.ContextHops != 4 {
		t.Fatalf("explicit --context-hops auto=%v hops=%d, want auto false hops 4", pr.gotReq.ContextHopsAuto, pr.gotReq.ContextHops)
	}
}

func TestPRReviewConfigContextDefaults(t *testing.T) {
	writeUserConfig(t, `[review]
deep_context = true
context_hops = 2
conversation = true
`)
	pr := &fakePRReviewer{outcome: ReviewOutcome{PR: &PRResult{Owner: "o", Repo: "r", Number: 1}}}
	if _, err := runPRKeepHome(t, pr, &fakeReviewer{}, "--pr", "o/r#1", "--no-post"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !pr.gotReq.DeepContext || pr.gotReq.ContextHops != 2 || pr.gotReq.ContextHopsAuto || !pr.gotReq.Conversation {
		t.Fatalf("config PR defaults not threaded: %+v", pr.gotReq)
	}

	writeUserConfig(t, `[review]
conversation = true
	`)
	pr = &fakePRReviewer{outcome: ReviewOutcome{PR: &PRResult{Owner: "o", Repo: "r", Number: 1}}}
	if _, err := runPRKeepHome(t, pr, &fakeReviewer{}, "--pr", "o/r#1", "--no-post", "--conversation=false"); err != nil {
		t.Fatalf("run explicit: %v", err)
	}
	if pr.gotReq.Conversation {
		t.Fatal("explicit --conversation=false must override config")
	}
}

func TestPRGateUsesPRReviewerNotLocalReviewer(t *testing.T) {
	pr := &fakePRReviewer{outcome: ReviewOutcome{
		Findings: []ReviewFinding{{File: "a.go", Line: 1, Severity: "critical"}},
		Stats:    map[string]any{},
		PR:       &PRResult{Owner: "o", Repo: "r", Number: 1, HeadSHA: "h"},
	}}
	// A local reviewer that would WRONGLY pass the gate; the --pr path must ignore it.
	local := &stubGateReviewer{gateFailed: false}
	_, err := runPR(t, pr, local, "--pr", "o/r#1", "--no-post", "--gate", "high")
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "review.gate_failed" || ce.Exit != 2 {
		t.Fatalf("want review.gate_failed exit 2 from the PR reviewer's own gate, got %+v", err)
	}
}

func TestPRPatchRepairRequiresSuggest(t *testing.T) {
	_, err := runPR(t, &fakePRReviewer{}, &fakeReviewer{}, "--pr", "o/r#1", "--no-post", "--patch-repair")
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "config.invalid" || ce.Exit != 2 {
		t.Fatalf("want config.invalid exit 2 for --patch-repair without --suggest, got %+v", err)
	}
}

func TestPRPatchRepairThreadedWithSuggest(t *testing.T) {
	pr := &fakePRReviewer{outcome: ReviewOutcome{PR: &PRResult{Owner: "o", Repo: "r", Number: 1}}}
	if _, err := runPR(t, pr, &fakeReviewer{}, "--pr", "o/r#1", "--no-post", "--suggest", "--patch-repair"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !pr.gotReq.Suggest {
		t.Fatal("--suggest must thread Suggest=true")
	}
	if !pr.gotReq.PatchRepair {
		t.Fatal("--patch-repair (with --suggest) must thread PatchRepair=true into the PR request")
	}
}

func TestPRPatchRepairOffByDefault(t *testing.T) {
	pr := &fakePRReviewer{outcome: ReviewOutcome{PR: &PRResult{Owner: "o", Repo: "r", Number: 1}}}
	if _, err := runPR(t, pr, &fakeReviewer{}, "--pr", "o/r#1", "--no-post", "--suggest"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if pr.gotReq.PatchRepair {
		t.Fatal("--patch-repair defaults OFF; must not thread PatchRepair=true without the flag")
	}
}

func TestPRPostNoPostConflict(t *testing.T) {
	_, err := runPR(t, &fakePRReviewer{}, &fakeReviewer{}, "--pr", "o/r#1", "--post", "--no-post")
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "flags.conflict" || ce.Exit != 2 {
		t.Fatalf("want flags.conflict exit 2, got %+v", err)
	}
}

func TestPRPostRequiresToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	_, err := runPR(t, &fakePRReviewer{}, &fakeReviewer{}, "--pr", "o/r#1", "--post")
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "github.post_requires_token" || ce.Exit != 2 {
		t.Fatalf("want github.post_requires_token exit 2, got %+v", err)
	}
}

func TestPRTokenNeverInEnvelope(t *testing.T) {
	const secret = "ghp_supersecrettoken123"
	pr := &fakePRReviewer{outcome: ReviewOutcome{
		Findings: []ReviewFinding{},
		Stats:    map[string]any{},
		PR:       &PRResult{Owner: "o", Repo: "r", Number: 1, HeadSHA: "abc"},
	}}
	out, err := runPR(t, pr, &fakeReviewer{}, "--pr", "o/r#1", "--no-post", "--token", secret)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if pr.gotReq.Token != secret {
		t.Fatalf("reviewer should receive the resolved token, got %q", pr.gotReq.Token)
	}
	if bytes.Contains([]byte(out), []byte(secret)) {
		t.Errorf("token leaked into envelope: %s", out)
	}
}

func TestPRTokenPrecedence(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "from_github_token")
	t.Setenv("GH_TOKEN", "from_gh_token")
	pr := &fakePRReviewer{outcome: ReviewOutcome{PR: &PRResult{Owner: "o", Repo: "r", Number: 1}}}

	// flag wins
	if _, err := runPR(t, pr, &fakeReviewer{}, "--pr", "o/r#1", "--no-post", "--token", "from_flag"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if pr.gotReq.Token != "from_flag" {
		t.Errorf("flag must win, got %q", pr.gotReq.Token)
	}

	// no flag → GITHUB_TOKEN over GH_TOKEN
	if _, err := runPR(t, pr, &fakeReviewer{}, "--pr", "o/r#1", "--no-post"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if pr.gotReq.Token != "from_github_token" {
		t.Errorf("GITHUB_TOKEN must beat GH_TOKEN, got %q", pr.gotReq.Token)
	}
}
