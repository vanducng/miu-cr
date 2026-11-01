package agent

import (
	stdctx "context"
	"encoding/json"
	"testing"
)

// Asserts on the JSON the SDK would send, per model family x thinking setting,
// at all three anthropic request sites (review loop, repair, relocate).
func TestAnthropicRequestShape(t *testing.T) {
	const (
		adaptive = `{"type":"adaptive"}`
		enabled  = `{"budget_tokens":10000,"type":"enabled"}`
	)
	tests := []struct {
		model string
		// medium
		wantThinking string // "" => no thinking block
		wantEffort   string // "" => no output_config
		wantTemp     bool   // temperature present on the review request (thinking=medium)
		// repair/relocate never think; only temperature varies by model
		wantAuxTemp bool
	}{
		{"claude-opus-5-5", adaptive, "medium", false, false},
		{"claude-sonnet-5-5", adaptive, "medium", false, false},
		{"claude-haiku-5-5", adaptive, "medium", false, false},
		{"claude-opus-4-8", adaptive, "medium", false, false},
		{"claude-opus-4-7", adaptive, "medium", false, false},
		{"claude-sonnet-4-5", enabled, "", false, true},
		{"claude-sonnet-4-20250514", enabled, "", false, true},
		{"claude-opus-4-1", enabled, "", false, true},
		{"glm-5.3", enabled, "", false, true},
		{"grok-4.7", enabled, "", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			medium := reviewShape(t, tt.model, "medium")
			assertShape(t, "review/medium", medium, tt.wantThinking, tt.wantEffort, tt.wantTemp)

			off := reviewShape(t, tt.model, "off")
			assertShape(t, "review/off", off, "", "", tt.wantAuxTemp)

			repair, relocate := auxShapes(t, tt.model)
			assertShape(t, "repair", repair, "", "", tt.wantAuxTemp)
			assertShape(t, "relocate", relocate, "", "", tt.wantAuxTemp)
		})
	}
}

func reviewShape(t *testing.T, model, thinking string) map[string]json.RawMessage {
	t.Helper()
	fc := &fakeAnthropic{responses: []string{textMessage(`{"findings":[]}`)}}
	a := &anthropicAgent{client: fc, model: model, thinking: thinking}
	if _, err := a.Review(stdctx.Background(), Context{Text: "ctx", RepoDir: t.TempDir()}); err != nil {
		t.Fatalf("Review: %v", err)
	}
	return marshalShape(t, fc.seen[0])
}

func auxShapes(t *testing.T, model string) (repair, relocate map[string]json.RawMessage) {
	t.Helper()
	fc := &fakeAnthropic{responses: []string{textMessage("x"), textMessage("x")}}
	a := &anthropicAgent{client: fc, model: model, thinking: "medium"}
	if _, _, err := a.RepairPatch(stdctx.Background(), RepairRequest{Span: "s", Rationale: "r", Category: "bug", Severity: "high"}); err != nil {
		t.Fatalf("RepairPatch: %v", err)
	}
	if _, _, err := a.RelocateQuote(stdctx.Background(), RelocateRequest{File: "f.go", Rationale: "r", QuotedCode: "q", Excerpt: "e", Category: "bug", Severity: "high"}); err != nil {
		t.Fatalf("RelocateQuote: %v", err)
	}
	return marshalShape(t, fc.seen[0]), marshalShape(t, fc.seen[1])
}

func marshalShape(t *testing.T, params any) map[string]json.RawMessage {
	t.Helper()
	b, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	return m
}

func assertShape(t *testing.T, site string, m map[string]json.RawMessage, wantThinking, wantEffort string, wantTemp bool) {
	t.Helper()
	got := string(m["thinking"])
	if (got == "") != (wantThinking == "") || (got != "" && !sameJSON(got, wantThinking)) {
		t.Errorf("%s: thinking = %q, want %q", site, got, wantThinking)
	}
	var effort string
	if raw, ok := m["output_config"]; ok {
		var oc struct {
			Effort string `json:"effort"`
		}
		if err := json.Unmarshal(raw, &oc); err != nil {
			t.Fatalf("%s: output_config: %v", site, err)
		}
		effort = oc.Effort
	}
	if effort != wantEffort {
		t.Errorf("%s: output_config.effort = %q, want %q", site, effort, wantEffort)
	}
	if _, has := m["temperature"]; has != wantTemp {
		t.Errorf("%s: temperature present = %v, want %v", site, has, wantTemp)
	}
}

func sameJSON(a, b string) bool {
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return false
	}
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return string(xb) == string(yb)
}
