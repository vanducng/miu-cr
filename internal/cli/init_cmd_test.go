package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/config"
)

// runInit drives initCommand with injected stdin and a fake HOME/cwd, returning
// stdout (the envelope) and stderr (prompts/payoff) separately.
func runInit(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	prettyOutput = false
	cmd := initCommand(&options{output: "json"})
	var so, se bytes.Buffer
	cmd.SetOut(&so)
	cmd.SetErr(&se)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err = cmd.Execute()
	return so.String(), se.String(), err
}

func isolateInitEnv(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Chdir(repo)
	return repo
}

func readConfig(t *testing.T) string {
	t.Helper()
	p := config.FilePathOrEmpty()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read config %s: %v", p, err)
	}
	return string(data)
}

// Interactive happy path: anthropic + env var + scaffold rules -> delta-only
// config, no secret, payoff ends on the literal first-review command.
func TestInitInteractiveEnvDefault(t *testing.T) {
	isolateInitEnv(t)
	// provider(anthropic), key source(env), env name(default), scaffold rules(yes default)
	stdout, stderr, err := runInit(t, "1\n1\n\ny\n")
	if err != nil {
		t.Fatalf("init: %v\nstderr=%s", err, stderr)
	}

	env := decodeEnvelope(t, []byte(stdout))
	if !env.OK || env.Kind != "init.result" {
		t.Fatalf("want ok init.result, got %+v", env)
	}

	body := readConfig(t)
	if strings.Contains(body, "auth_token =") {
		t.Fatalf("env-default init must write no secret:\n%s", body)
	}
	if !strings.Contains(body, "ANTHROPIC_API_KEY") {
		t.Fatalf("want auth_env recorded:\n%s", body)
	}
	if strings.Contains(body, "[providers.openai]") {
		t.Fatalf("built-in profiles must not be persisted:\n%s", body)
	}
	if !strings.Contains(stderr, "miucr review --staged") {
		t.Fatalf("payoff must end on the first-review command:\n%s", stderr)
	}
}

// Paste-now writes a literal auth_token only after the plaintext warning + confirm.
func TestInitPasteNowWritesSecretWithWarning(t *testing.T) {
	isolateInitEnv(t)
	// provider(anthropic), key source(paste), key, confirm-plaintext(y), no rules
	_, stderr, err := runInit(t, "1\n2\nsk-synthetic-key\ny\nn\n")
	if err != nil {
		t.Fatalf("init: %v\nstderr=%s", err, stderr)
	}
	if !strings.Contains(stderr, "plaintext on disk") {
		t.Fatalf("want plaintext warning:\n%s", stderr)
	}
	body := readConfig(t)
	if !strings.Contains(body, "auth_token") || !strings.Contains(body, "sk-synthetic-key") {
		t.Fatalf("paste-now must write the literal token:\n%s", body)
	}
}

// Declining the plaintext confirm aborts without writing.
func TestInitPasteNowDeclineAborts(t *testing.T) {
	isolateInitEnv(t)
	_, _, err := runInit(t, "1\n2\nsk-synthetic-key\nn\n")
	if err == nil {
		t.Fatal("want init.aborted on declined paste")
	}
	if code := cliErrCode(t, err); code != "init.aborted" {
		t.Fatalf("want init.aborted, got %s", code)
	}
}

// Existing config -> Overwrite? prompt; N aborts.
func TestInitExistingConfigOverwriteNoAborts(t *testing.T) {
	isolateInitEnv(t)
	seed := config.Defaults()
	seed.DefaultProvider = "openai"
	if err := config.Save(seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, stderr, err := runInit(t, "n\n")
	if err == nil {
		t.Fatalf("want abort on overwrite=N\nstderr=%s", stderr)
	}
	if code := cliErrCode(t, err); code != "init.aborted" {
		t.Fatalf("want init.aborted, got %s", code)
	}
}

// --non-interactive --provider --auth-env --yes -> zero prompts, delta-only config.
func TestInitNonInteractive(t *testing.T) {
	isolateInitEnv(t)
	stdout, _, err := runInit(t, "",
		"--non-interactive", "--provider", "openai", "--auth-env", "MY_OPENAI_KEY", "--yes", "--no-rules")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	env := decodeEnvelope(t, []byte(stdout))
	if !env.OK || env.Kind != "init.result" {
		t.Fatalf("want ok init.result, got %+v", env)
	}
	body := readConfig(t)
	if !strings.Contains(body, "MY_OPENAI_KEY") {
		t.Fatalf("want auth_env from flag:\n%s", body)
	}
	if strings.Contains(body, "auth_token =") {
		t.Fatalf("non-interactive default must write no secret:\n%s", body)
	}
}

// --force overwrites an existing config in non-interactive mode with no prompt.
func TestInitNonInteractiveForceOverwrites(t *testing.T) {
	isolateInitEnv(t)
	if err := config.Save(config.Defaults()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, _, err := runInit(t, "", "--non-interactive", "--provider", "anthropic", "--force", "--no-rules")
	if err != nil {
		t.Fatalf("init --force: %v", err)
	}
}

// Detection maps a manifest to the right rule-file stem; no match -> generic.
func TestInitDetectionStem(t *testing.T) {
	cases := []struct {
		manifest string
		wantStem string
	}{
		{"go.mod", "go"},
		{"package.json", "typescript"},
		{"pyproject.toml", "python"},
		{"setup.py", "python"},
		{"Cargo.toml", "rules"}, // no detector branch -> generic
	}
	for _, tc := range cases {
		t.Run(tc.manifest, func(t *testing.T) {
			isolateInitEnv(t)
			if err := os.WriteFile(tc.manifest, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, _, err := runInit(t, "",
				"--non-interactive", "--provider", "anthropic", "--auth-env", "ANTHROPIC_API_KEY", "--yes")
			if err != nil {
				t.Fatalf("init: %v", err)
			}
			want := filepath.Join(".miu", "cr", "rules", tc.wantStem+".md")
			if _, err := os.Stat(want); err != nil {
				t.Fatalf("want scaffolded %s: %v", want, err)
			}
		})
	}
}

func cliErrCode(t *testing.T, err error) string {
	t.Helper()
	var ce *CLIError
	if !errors.As(err, &ce) {
		t.Fatalf("want *CLIError, got %T: %v", err, err)
	}
	return ce.Code
}
