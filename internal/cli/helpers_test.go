package cli

import (
	"bytes"
	"errors"
	"testing"
)

func TestExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"plain error", errors.New("boom"), 1},
		{"clierr with exit", &CLIError{Code: "x", Message: "m", Exit: 3}, 3},
		{"clierr zero exit falls back to 1", &CLIError{Code: "x", Message: "m"}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExitCode(tt.err); got != tt.want {
				t.Errorf("ExitCode = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRedactDetails(t *testing.T) {
	if redactDetails(nil) != nil {
		t.Error("nil details should pass through")
	}
	out := redactDetails(map[string]any{
		"url":   "https://user:s3cretpw@host/x",
		"count": 7,
	})
	if got, _ := out["url"].(string); got == "" || got == "https://user:s3cretpw@host/x" {
		t.Errorf("url password not redacted: %v", out["url"])
	}
	if out["count"] != 7 {
		t.Errorf("non-string value altered: %v", out["count"])
	}
}

func TestWriteErrorRedactsAndUsesCLIErrorFields(t *testing.T) {
	var buf bytes.Buffer
	err := &CLIError{
		Code:    "agent.bad_key",
		Message: "rejected token sk-ant-leakedsecret99",
		Hint:    "set ANTHROPIC_API_KEY",
		Retry:   true,
	}
	if werr := writeError(&buf, "review", err); werr != nil {
		t.Fatalf("writeError: %v", werr)
	}
	env := decodeEnvelope(t, buf.Bytes())
	if env.OK {
		t.Error("error envelope ok = true, want false")
	}
	if env.Error == nil || env.Error.Code != "agent.bad_key" {
		t.Fatalf("error code = %+v, want agent.bad_key", env.Error)
	}
	if !env.Error.Retryable {
		t.Error("retryable = false, want true")
	}
	if bytes.Contains(buf.Bytes(), []byte("sk-ant-leakedsecret99")) {
		t.Errorf("token leaked in error message: %s", buf.String())
	}
}

func TestWriteErrorPlainError(t *testing.T) {
	var buf bytes.Buffer
	if err := writeError(&buf, "review", errors.New("unexpected")); err != nil {
		t.Fatalf("writeError: %v", err)
	}
	env := decodeEnvelope(t, buf.Bytes())
	if env.Error == nil || env.Error.Code != "internal.error" {
		t.Fatalf("plain error code = %+v, want internal.error", env.Error)
	}
}

func TestCommandPath(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{nil, "miucr"},
		{[]string{"review"}, "review"},
		{[]string{"review", "--staged"}, "review --staged"},
		{[]string{"a", "b", "c", "d"}, "a b"},
	}
	for _, tt := range tests {
		if got := commandPath(tt.args); got != tt.want {
			t.Errorf("commandPath(%v) = %q, want %q", tt.args, got, tt.want)
		}
	}
}

func TestIsMCPServeCommand(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{[]string{"mcp"}, true},
		{[]string{"--verbose", "mcp"}, true},
		{[]string{"review"}, false},
		{[]string{"review", "mcp"}, false},
		{nil, false},
	}
	for _, tt := range tests {
		if got := isMCPServeCommand(tt.args); got != tt.want {
			t.Errorf("isMCPServeCommand(%v) = %v, want %v", tt.args, got, tt.want)
		}
	}
}

func TestVersionStringTaggedWins(t *testing.T) {
	prev := version
	t.Cleanup(func() { version = prev })
	version = "v1.2.3"
	if got := versionString(); got != "v1.2.3" {
		t.Errorf("versionString = %q, want v1.2.3", got)
	}
}
