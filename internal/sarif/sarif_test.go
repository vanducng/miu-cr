package sarif

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func decode(t *testing.T, findings []Finding) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	if err := EmitSARIF(&buf, findings, "v0.17.0"); err != nil {
		t.Fatalf("EmitSARIF: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("emitted invalid JSON: %v\n%s", err, buf.String())
	}
	return m
}

func TestEmitSARIFShape(t *testing.T) {
	m := decode(t, []Finding{{
		File: "src/app.go", Line: 10, EndLine: 12, Severity: "high",
		Category: "bug", Rationale: "off-by-one", QuotedCode: "for i := 0; i <= n; i++ {",
		SuggestedPatch: "for i := 0; i < n; i++ {",
	}})

	if m["version"] != version {
		t.Fatalf("version = %v, want %s", m["version"], version)
	}
	if !strings.Contains(m["$schema"].(string), "sarif-schema-2.1.0") {
		t.Fatalf("schema not pinned 2.1.0: %v", m["$schema"])
	}
	runs := m["runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("want 1 run, got %d", len(runs))
	}
	r0 := runs[0].(map[string]any)
	driver := r0["tool"].(map[string]any)["driver"].(map[string]any)
	if driver["name"] != toolName {
		t.Fatalf("driver name = %v, want %s", driver["name"], toolName)
	}

	results := r0["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	res := results[0].(map[string]any)
	if res["ruleId"] != "bug" {
		t.Fatalf("ruleId = %v, want bug", res["ruleId"])
	}
	if res["level"] != "error" {
		t.Fatalf("level = %v, want error (high→error)", res["level"])
	}
	if res["message"].(map[string]any)["text"] != "off-by-one" {
		t.Fatalf("message text mismatch: %v", res["message"])
	}
	loc := res["locations"].([]any)[0].(map[string]any)
	phys := loc["physicalLocation"].(map[string]any)
	if phys["artifactLocation"].(map[string]any)["uri"] != "src/app.go" {
		t.Fatalf("uri = %v, want src/app.go", phys["artifactLocation"])
	}
	reg := phys["region"].(map[string]any)
	if reg["startLine"].(float64) != 10 || reg["endLine"].(float64) != 12 {
		t.Fatalf("region lines = %v", reg)
	}
	if reg["snippet"].(map[string]any)["text"] != "for i := 0; i <= n; i++ {" {
		t.Fatalf("snippet mismatch: %v", reg["snippet"])
	}
	fixes := res["fixes"].([]any)
	if len(fixes) != 1 {
		t.Fatalf("want 1 fix, got %d", len(fixes))
	}
}

func TestLevelMapping(t *testing.T) {
	cases := map[string]string{
		"critical": "error", "high": "error", "medium": "warning",
		"low": "note", "info": "note", "": "note", "weird": "note",
	}
	for sev, want := range cases {
		if got := levelFor(sev); got != want {
			t.Errorf("levelFor(%q) = %q, want %q", sev, got, want)
		}
	}
}

func TestRelURINeverAbsolute(t *testing.T) {
	for _, in := range []string{"/abs/secret/path.go", "./rel/path.go", "rel/path.go", "C:\\win\\path.go"} {
		got := relURI(in)
		if strings.HasPrefix(got, "/") || strings.HasPrefix(got, "./") {
			t.Errorf("relURI(%q) = %q leaks absolute/dot prefix", in, got)
		}
	}
	if relURI("/abs/x.go") != "abs/x.go" {
		t.Fatalf("absolute not stripped: %q", relURI("/abs/x.go"))
	}
}

func TestEmitSARIFRuleSetDeduped(t *testing.T) {
	m := decode(t, []Finding{
		{File: "a.go", Line: 1, Severity: "low", Category: "style"},
		{File: "b.go", Line: 2, Severity: "low", Category: "style"},
		{File: "c.go", Line: 3, Severity: "high", Category: "bug"},
	})
	driver := m["runs"].([]any)[0].(map[string]any)["tool"].(map[string]any)["driver"].(map[string]any)
	rules := driver["rules"].([]any)
	if len(rules) != 2 {
		t.Fatalf("want 2 deduped rules, got %d: %v", len(rules), rules)
	}
}

func TestEmitSARIFEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := EmitSARIF(&buf, nil, ""); err != nil {
		t.Fatalf("EmitSARIF empty: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("empty emit invalid JSON: %v", err)
	}
	results := m["runs"].([]any)[0].(map[string]any)["results"].([]any)
	if len(results) != 0 {
		t.Fatalf("want 0 results for empty input, got %d", len(results))
	}
}
