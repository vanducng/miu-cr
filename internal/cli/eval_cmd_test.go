package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEvalCommandRunsToolAndScores(t *testing.T) {
	dir := t.TempDir()
	cases := filepath.Join(dir, "cases.json")
	body := `{"cases":[{"id":"synthetic","repo":"` + filepath.ToSlash(dir) + `","from":"base","to":"head","expected":[{"id":"bug","file":"app.go","line":3}]}]}`
	if err := os.WriteFile(cases, []byte(body), 0o600); err != nil {
		t.Fatalf("write cases: %v", err)
	}

	opts := &options{output: "json", timeout: time.Second}
	cmd := evalCommand(opts)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--cases", cases, "--tool", `dummy=printf '{"findings":[{"file":"app.go","line":3}]}'`})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	if err := cmd.Execute(); err != nil {
		t.Fatalf("eval command: %v\n%s", err, buf.String())
	}

	env := decodeEnvelope(t, buf.Bytes())
	if !env.OK || env.Kind != "eval.result" {
		t.Fatalf("envelope = %+v", env)
	}
	raw, err := json.Marshal(env.Summary)
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	var summary struct {
		Matched float64 `json:"matched"`
		Recall  float64 `json:"recall"`
		F1      float64 `json:"f1"`
	}
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}
	if summary.Matched != 1 || summary.Recall != 1 || summary.F1 != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if opts.timeout != time.Second {
		t.Fatalf("eval command should not mutate shared opts.timeout, got %s", opts.timeout)
	}
}

func TestEvalCommandHonorsRootTimeoutFlag(t *testing.T) {
	dir := t.TempDir()
	cases := filepath.Join(dir, "cases.json")
	body := `{"cases":[{"id":"synthetic","repo":"` + filepath.ToSlash(dir) + `","from":"base","to":"head","expected":[]}]}`
	if err := os.WriteFile(cases, []byte(body), 0o600); err != nil {
		t.Fatalf("write cases: %v", err)
	}

	opts := &options{output: "json", timeout: 30 * time.Second}
	cmd := rootCommand(opts)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--timeout", "1ms", "eval", "--cases", cases, "--tool", `slow=sh -c 'sleep 0.05; printf "{\"findings\":[]}"'`})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	if err := cmd.Execute(); err != nil {
		t.Fatalf("eval command: %v\n%s", err, buf.String())
	}

	env := decodeEnvelope(t, buf.Bytes())
	raw, err := json.Marshal(env.Data)
	if err != nil {
		t.Fatalf("marshal data: %v", err)
	}
	var result struct {
		Tools []struct {
			Cases []struct {
				Error string `json:"error"`
			} `json:"cases"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if len(result.Tools) != 1 || len(result.Tools[0].Cases) != 1 || result.Tools[0].Cases[0].Error == "" {
		t.Fatalf("expected eval case to time out, got %+v", result)
	}
}
