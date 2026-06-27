package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vanducng/miu-cr/internal/config"
)

// writeUserConfig points HOME at a temp dir and writes config.toml there.
func writeUserConfig(t *testing.T, body string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "miu", "cr")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// runReviewKeepHome runs the review command WITHOUT resetting HOME (unlike
// runReview), so a caller's writeUserConfig'd config.toml is loaded by the
// [review] flag-fallback. The fakeReviewer records the resolved gate.
func runReviewKeepHome(t *testing.T, r Reviewer, args ...string) (string, error) {
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
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	return buf.String(), err
}

func runConfigShow(t *testing.T, pretty bool, args ...string) string {
	t.Helper()
	prevFmt, prevPretty := outputFormat, prettyOutput
	t.Cleanup(func() { outputFormat, prettyOutput = prevFmt, prevPretty })
	if pretty {
		outputFormat, prettyOutput = "pretty", true
	} else {
		outputFormat, prettyOutput = "json", false
	}
	cmd := configCommand(&options{})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(append([]string{"show"}, args...))
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config show: %v", err)
	}
	return buf.String()
}

const secretToken = "sk-ant-LEAKEDAUTHTOKEN999"
const secretDSN = "postgres://u:DSNPASSWORD123@host:5432/db"

func configWithSecrets() string {
	return `default_provider = "anthropic"

[providers.anthropic]
kind = "anthropic"
auth_token = "` + secretToken + `"

[store]
backend = "postgres"
dsn = "` + secretDSN + `"
`
}

// TestConfigShowNeverLeaksSecrets: a configured auth_token AND store DSN must not
// appear in config show output, in json or pretty, with or without --all.
func TestConfigShowNeverLeaksSecrets(t *testing.T) {
	for _, pretty := range []bool{false, true} {
		for _, all := range []bool{false, true} {
			writeUserConfig(t, configWithSecrets())
			var args []string
			if all {
				args = append(args, "--all")
			}
			out := runConfigShow(t, pretty, args...)
			for _, secret := range []string{secretToken, "DSNPASSWORD123", secretDSN} {
				if strings.Contains(out, secret) {
					t.Fatalf("secret leaked (pretty=%v all=%v): %q in %s", pretty, all, secret, out)
				}
			}
		}
	}
}

// TestConfigShowAllIncludesDefaults: --all includes built-in defaults that the
// default view (deltas only) strips.
func TestConfigShowAllIncludesDefaults(t *testing.T) {
	writeUserConfig(t, `default_provider = "anthropic"
`)
	deltaOut := runConfigShow(t, false)
	allOut := runConfigShow(t, false, "--all")
	// The default OpenAI base URL is a built-in default save.go strips; it must
	// appear under --all but not in the bare delta view.
	if strings.Contains(deltaOut, config.DefaultOpenAIBaseURL) {
		t.Fatalf("delta view should omit built-in defaults, got %s", deltaOut)
	}
	if !strings.Contains(allOut, config.DefaultOpenAIBaseURL) {
		t.Fatalf("--all must include built-in defaults, got %s", allOut)
	}
}

// TestReviewGateConfigDefaultHonoredAndFlagWins: a [review].gate fills an unset
// --gate but an explicit --gate overrides it.
func TestReviewGateConfigDefaultHonoredAndFlagWins(t *testing.T) {
	const body = `[review]
gate = "low"
`
	// Config default honored when --gate absent.
	writeUserConfig(t, body)
	r := &fakeReviewer{outcome: ReviewOutcome{Findings: []ReviewFinding{{File: "a.go", Line: 1, Severity: "medium"}}}}
	// gate=low + a medium finding → gate_failed (exit 2). Confirms the config gate took effect.
	_, err := runReviewKeepHome(t, r, "--staged")
	if err == nil {
		t.Fatal("config gate=low should have failed on a medium finding")
	}
	if r.gotReq.Gate != "low" {
		t.Fatalf("config [review].gate=low not applied, gotReq.Gate=%q", r.gotReq.Gate)
	}

	// Explicit --gate wins over config.
	writeUserConfig(t, body)
	r2 := &fakeReviewer{outcome: ReviewOutcome{Findings: []ReviewFinding{{File: "a.go", Line: 1, Severity: "medium"}}}}
	_, err2 := runReviewKeepHome(t, r2, "--staged", "--gate", "critical")
	if err2 != nil {
		t.Fatalf("explicit --gate critical should pass a medium finding, got %v", err2)
	}
	if r2.gotReq.Gate != "critical" {
		t.Fatalf("explicit --gate should win, gotReq.Gate=%q", r2.gotReq.Gate)
	}
}

func TestReviewTimeoutConfigWinsOverDeepContext(t *testing.T) {
	writeUserConfig(t, `[review]
timeout = "600s"
`)
	r := &fakeReviewer{outcome: ReviewOutcome{Findings: []ReviewFinding{}}}
	_, err := runReviewKeepHome(t, r, "--staged", "--deep-context")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if r.gotReq.Timeout != 600*time.Second {
		t.Fatalf("[review].timeout should win over --deep-context default, got %s", r.gotReq.Timeout)
	}
}

func TestReviewContextConfigDefaultsHonoredAndFlagsWin(t *testing.T) {
	writeUserConfig(t, `[review]
expand = 12
token_budget = 0
deep_context = true
context_hops = 3
conversation = true
`)
	r := &fakeReviewer{outcome: ReviewOutcome{Findings: []ReviewFinding{}}}
	_, err := runReviewKeepHome(t, r, "--staged")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if r.gotReq.ExpandWindow != 12 || r.gotReq.TokenBudget != 0 || !r.gotReq.DeepContext || r.gotReq.ContextHops != 3 || r.gotReq.ContextHopsAuto {
		t.Fatalf("config context defaults not applied: %+v", r.gotReq)
	}

	writeUserConfig(t, `[review]
expand = 12
token_budget = 0
deep_context = true
context_hops = 3
conversation = true
`)
	r2 := &fakeReviewer{outcome: ReviewOutcome{Findings: []ReviewFinding{}}}
	_, err = runReviewKeepHome(t, r2, "--staged", "--expand", "7", "--token-budget", "123", "--deep-context=false", "--context-hops", "1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if r2.gotReq.ExpandWindow != 7 || r2.gotReq.TokenBudget != 123 || r2.gotReq.DeepContext || r2.gotReq.ContextHops != 1 || r2.gotReq.ContextHopsAuto {
		t.Fatalf("explicit flags should win: %+v", r2.gotReq)
	}
}

func TestConfigShowKeepsExplicitZeroReviewBudget(t *testing.T) {
	writeUserConfig(t, `[review]
token_budget = 0
`)
	out := runConfigShow(t, true)
	if !strings.Contains(out, "token_budget = 0") {
		t.Fatalf("config show should keep explicit token_budget=0, got %s", out)
	}
}

// TestReviewBadConfigGateIsTypedConfigInvalid: a bad [review] enum → config.invalid.
func TestReviewBadConfigFilterModeIsTypedConfigInvalid(t *testing.T) {
	writeUserConfig(t, `[review]
filter_mode = "bogus"
`)
	r := &fakeReviewer{}
	_, err := runReviewKeepHome(t, r, "--staged")
	var ce *CLIError
	if !asCLIError(err, &ce) || ce.Code != "config.invalid" {
		t.Fatalf("want config.invalid, got %+v", err)
	}
}
