package agent

import (
	stdctx "context"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/anchor"
	"github.com/vanducng/miu-cr/internal/engine/diff"
)

const secretToken = "sk-ant-do-not-leak-0123456789"

// fakeAgent drives the pipeline with fixed findings and zero network. It also
// records a bounded internal "tool loop" to exercise bail-out behavior.
type fakeAgent struct {
	findings  []engine.Finding
	maxEmpty  int
	emptySeen int
	bailedOut bool
}

func (f *fakeAgent) Review(ctx stdctx.Context, rc Context) ([]engine.Finding, error) {
	for f.emptySeen < f.maxEmpty {
		f.emptySeen++
	}
	f.bailedOut = f.emptySeen >= f.maxEmpty
	return f.findings, nil
}

var _ Agent = (*fakeAgent)(nil)

func TestFakeAgentZeroNetwork(t *testing.T) {
	want := []engine.Finding{{
		Severity:   "high",
		Category:   "bug",
		Rationale:  "off-by-one, example token " + secretToken + " in prose",
		QuotedCode: "for i := 0; i <= len(s); i++ {",
	}}
	fa := &fakeAgent{findings: want, maxEmpty: maxEmptyRounds}
	got, err := fa.Review(stdctx.Background(), Context{Text: "ctx"})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 finding, got %d", len(got))
	}
	if got[0].Line != 0 || got[0].EndLine != 0 {
		t.Fatalf("agent must emit no line numbers: line=%d end=%d", got[0].Line, got[0].EndLine)
	}
	if got[0].QuotedCode == "" {
		t.Fatal("quoted code must be carried for anchoring")
	}
	if !fa.bailedOut {
		t.Fatal("fake tool loop did not reach the empty-round bail")
	}
}

func TestParseFindingsFenceStripping(t *testing.T) {
	model := "```json\n" +
		`{"findings":[{"existing_code":"x := y / 0","severity":"critical","category":"bug","rationale":"division by zero","suggested_patch":"if y != 0 { x = y }"}]}` +
		"\n```"
	findings, ok := parseFindings(model)
	if !ok {
		t.Fatal("parse failed")
	}
	if len(findings) != 1 {
		t.Fatalf("want 1, got %d", len(findings))
	}
	f := findings[0]
	if f.Severity != "critical" || f.Category != "bug" {
		t.Fatalf("severity/category not parsed: %+v", f)
	}
	if f.QuotedCode != "x := y / 0" {
		t.Fatalf("existing_code not mapped to QuotedCode: %q", f.QuotedCode)
	}
	if f.Line != 0 || f.EndLine != 0 {
		t.Fatalf("parsed findings must have no line numbers: %+v", f)
	}
}

// Regression: the model must report "file" per finding and parseFindings must
// carry it onto engine.Finding.File, otherwise the anchor resolver keys on ""
// and silently drops every finding. This also proves the agent->anchor handoff.
func TestParseFindingsPopulatesFileAndAnchors(t *testing.T) {
	const handlerDiff = `diff --git a/pkg/example/handler.go b/pkg/example/handler.go
--- a/pkg/example/handler.go
+++ b/pkg/example/handler.go
@@ -10,7 +10,7 @@ func HandleRequest(w http.ResponseWriter, r *http.Request) {
     ctx := r.Context()
-    log.Print("handling request")
+    log.Printf("handling request: %s", r.URL.Path)
     err := process(ctx)`

	model := `{"findings":[{"file":"pkg/example/handler.go","existing_code":"log.Printf(\"handling request: %s\", r.URL.Path)","severity":"low","category":"maintainability","rationale":"r"}]}`

	findings, ok := parseFindings(model)
	if !ok || len(findings) != 1 {
		t.Fatalf("parse: ok=%v len=%d", ok, len(findings))
	}
	if findings[0].File != "pkg/example/handler.go" {
		t.Fatalf("File not populated: %q", findings[0].File)
	}

	got := anchor.ResolveLineNumbers(findings, []diff.Diff{
		{NewPath: "pkg/example/handler.go", Diff: handlerDiff},
	})
	if got[0].Line != 11 || got[0].EndLine != 11 {
		t.Fatalf("agent->anchor handoff: got %d..%d, want 11..11", got[0].Line, got[0].EndLine)
	}
}

func TestParseFindingsPlainAndEmpty(t *testing.T) {
	if findings, ok := parseFindings(`{"findings":[]}`); !ok || len(findings) != 0 {
		t.Fatalf("empty findings: ok=%v len=%d", ok, len(findings))
	}
	if _, ok := parseFindings("not json"); ok {
		t.Fatal("non-JSON must not parse")
	}
	if _, ok := parseFindings(""); ok {
		t.Fatal("empty must not parse")
	}
}

func TestStripMarkdownFencesLanguageTag(t *testing.T) {
	in := "```json\n{\"a\":1}\n```"
	if got := stripMarkdownFences(in); got != `{"a":1}` {
		t.Fatalf("fence strip with lang tag: %q", got)
	}
	bare := "```\n{\"a\":1}\n```"
	if got := stripMarkdownFences(bare); got != `{"a":1}` {
		t.Fatalf("bare fence strip: %q", got)
	}
	none := `{"a":1}`
	if got := stripMarkdownFences(none); got != none {
		t.Fatalf("no-fence passthrough: %q", got)
	}
}

func TestTokenNeverInFindings(t *testing.T) {
	creds := Credentials{APIKey: secretToken, Model: defaultModel}
	a := New(creds, 0)
	if a == nil {
		t.Fatal("New returned nil")
	}
	want := []engine.Finding{{Severity: "low", Category: "bug", QuotedCode: "z := 1"}}
	fa := &fakeAgent{findings: want, maxEmpty: 1}
	got, err := fa.Review(stdctx.Background(), Context{Text: "ctx"})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	for _, f := range got {
		if strings.Contains(f.Rationale, secretToken) ||
			strings.Contains(f.QuotedCode, secretToken) ||
			strings.Contains(f.SuggestedPatch, secretToken) ||
			strings.Contains(f.Category, secretToken) ||
			strings.Contains(f.Severity, secretToken) {
			t.Fatal("API token leaked into a returned Finding")
		}
	}
}
