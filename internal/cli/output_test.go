package cli

import (
	"bytes"
	"encoding/json"
	"testing"
)

func decodeEnvelope(t *testing.T, b []byte) Envelope {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	return env
}

func TestWriteSuccessEnvelopeDefaults(t *testing.T) {
	var buf bytes.Buffer
	if err := writeSuccess(&buf, "version", "version", map[string]any{"version": "v0.1.0"}, nil); err != nil {
		t.Fatalf("writeSuccess: %v", err)
	}
	env := decodeEnvelope(t, buf.Bytes())

	if !env.OK {
		t.Errorf("ok = false, want true")
	}
	if env.APIVersion != "miucr.cli/v1" {
		t.Errorf("api_version = %q, want miucr.cli/v1", env.APIVersion)
	}
	if env.RequestID == "" {
		t.Errorf("request_id is empty")
	}

	// artifacts/warnings must be [] not null in the raw JSON.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(buf.Bytes(), &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	if string(raw["artifacts"]) != "[]" {
		t.Errorf("artifacts = %s, want []", raw["artifacts"])
	}
	if string(raw["warnings"]) != "[]" {
		t.Errorf("warnings = %s, want []", raw["warnings"])
	}
}

func TestScrubOutputRedactsSecretKeyButExemptsProse(t *testing.T) {
	in := map[string]any{
		"api_key":         "sk-ant-supersecret",
		"rationale":       "this code leaks api_key=abc123 to logs; remove it",
		"suggested_patch": "- token=hardcoded\n+ token=os.Getenv(\"TOKEN\")",
	}
	out, ok := scrubOutput(in).(map[string]any)
	if !ok {
		t.Fatalf("scrubOutput returned %T, want map", scrubOutput(in))
	}

	if out["api_key"] != "***" {
		t.Errorf("api_key = %v, want ***", out["api_key"])
	}
	if got := out["rationale"].(string); got != "this code leaks api_key=abc123 to logs; remove it" {
		t.Errorf("rationale was redacted: %q", got)
	}
	if got := out["suggested_patch"].(string); got != "- token=hardcoded\n+ token=os.Getenv(\"TOKEN\")" {
		t.Errorf("suggested_patch was redacted: %q", got)
	}
}
