package sarif

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestEmitSARIFHelpURI(t *testing.T) {
	var buf bytes.Buffer
	findings := []Finding{
		{File: "a.go", Line: 1, Severity: "high", Category: "Security"},
		{File: "b.go", Line: 2, Severity: "low", Category: "style"}, // unmapped
	}
	urls := map[string]string{"security": "https://docs.example/sec"}
	if err := EmitSARIFWithLinks(&buf, findings, "", urls); err != nil {
		t.Fatalf("EmitSARIFWithLinks: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"helpUri": "https://docs.example/sec"`) {
		t.Fatalf("mapped rule must carry helpUri:\n%s", out)
	}

	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	rules := m["runs"].([]any)[0].(map[string]any)["tool"].(map[string]any)["driver"].(map[string]any)["rules"].([]any)
	for _, r := range rules {
		rm := r.(map[string]any)
		if rm["id"] == "style" {
			if _, ok := rm["helpUri"]; ok {
				t.Fatalf("unmapped rule must omit helpUri: %v", rm)
			}
		}
	}
}

// Nil map => byte-for-byte EmitSARIF (no helpUri key anywhere).
func TestEmitSARIFNilLinksUnchanged(t *testing.T) {
	var a, b bytes.Buffer
	fs := []Finding{{File: "a.go", Line: 1, Severity: "high", Category: "bug"}}
	if err := EmitSARIF(&a, fs, "v1"); err != nil {
		t.Fatal(err)
	}
	if err := EmitSARIFWithLinks(&b, fs, "v1", nil); err != nil {
		t.Fatal(err)
	}
	if a.String() != b.String() {
		t.Fatalf("nil map must equal EmitSARIF:\n%s\n---\n%s", a.String(), b.String())
	}
	if strings.Contains(a.String(), "helpUri") {
		t.Fatalf("default output must not contain helpUri:\n%s", a.String())
	}
}
