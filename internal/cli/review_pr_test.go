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
