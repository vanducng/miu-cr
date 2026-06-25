package agent

import (
	stdctx "context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/config"
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

func (f *fakeAgent) Review(ctx stdctx.Context, rc Context) (engine.ReviewOutput, error) {
	for f.emptySeen < f.maxEmpty {
		f.emptySeen++
	}
	f.bailedOut = f.emptySeen >= f.maxEmpty
	return engine.ReviewOutput{
		Findings:      f.findings,
		Walkthrough:   "Sample walkthrough: bounds-check the loop.",
		FileSummaries: map[string]string{"app.go": "Tightens the loop bound."},
	}, nil
}

var _ Agent = (*fakeAgent)(nil)

func TestFakeAgentZeroNetwork(t *testing.T) {
	want := []engine.Finding{{
		Title:      "Off-by-one loop bound",
		Rule:       "go",
		Severity:   "high",
		Category:   "bug",
		Rationale:  "off-by-one, example token " + secretToken + " in prose",
		QuotedCode: "for i := 0; i <= len(s); i++ {",
	}}
	fa := &fakeAgent{findings: want, maxEmpty: maxEmptyRounds}
	out, err := fa.Review(stdctx.Background(), Context{Text: "ctx"})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	got := out.Findings
	if len(got) != 1 {
		t.Fatalf("want 1 finding, got %d", len(got))
	}
	if got[0].Line != 0 || got[0].EndLine != 0 {
		t.Fatalf("agent must emit no line numbers: line=%d end=%d", got[0].Line, got[0].EndLine)
	}
	if got[0].QuotedCode == "" {
		t.Fatal("quoted code must be carried for anchoring")
	}
	if got[0].Title != "Off-by-one loop bound" {
		t.Fatalf("title must be carried, got %q", got[0].Title)
	}
	if got[0].Rule != "go" {
		t.Fatalf("rule must be carried, got %q", got[0].Rule)
	}
	if !fa.bailedOut {
		t.Fatal("fake tool loop did not reach the empty-round bail")
	}
}

func TestParseFindingsFenceStripping(t *testing.T) {
	model := "```json\n" +
		`{"findings":[{"existing_code":"x := y / 0","severity":"critical","category":"bug","rationale":"division by zero","suggested_patch":"if y != 0 { x = y }"}]}` +
		"\n```"
	out, ok := parseFindings(model)
	if !ok {
		t.Fatal("parse failed")
	}
	findings := out.Findings
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

	out, ok := parseFindings(model)
	findings := out.Findings
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

// parseFindings maps the optional title onto engine.Finding.Title (the shared
// path covers all 3 backends), caps it, and leaves it empty on a response that
// omits it (back-compat).
func TestParseFindingsTitle(t *testing.T) {
	out, ok := parseFindings(`{"findings":[{"file":"a.go","existing_code":"x","severity":"low","category":"bug","title":"Unchecked nil deref","rationale":"r"}]}`)
	if !ok || len(out.Findings) != 1 {
		t.Fatalf("parse: ok=%v len=%d", ok, len(out.Findings))
	}
	if out.Findings[0].Title != "Unchecked nil deref" {
		t.Fatalf("title not mapped: %q", out.Findings[0].Title)
	}

	// Absent title => empty (back-compat).
	out2, _ := parseFindings(`{"findings":[{"file":"a.go","existing_code":"x","severity":"low","category":"bug","rationale":"r"}]}`)
	if out2.Findings[0].Title != "" {
		t.Fatalf("absent title must be empty, got %q", out2.Findings[0].Title)
	}

	// Over-long title is rune-capped, not rejected.
	long := strings.Repeat("é", maxTitleLen+50)
	out3, _ := parseFindings(`{"findings":[{"file":"a.go","existing_code":"x","severity":"low","category":"bug","title":"` + long + `","rationale":"r"}]}`)
	if n := len([]rune(out3.Findings[0].Title)); n != maxTitleLen {
		t.Fatalf("title cap: got %d runes, want %d", n, maxTitleLen)
	}
}

// parseFindings maps the optional rule stem onto engine.Finding.Rule (the shared
// path covers all 3 backends), trims + caps it, and leaves it empty when omitted.
func TestParseFindingsRule(t *testing.T) {
	out, ok := parseFindings(`{"findings":[{"file":"a.go","existing_code":"x","severity":"low","category":"bug","rule":"  go  ","rationale":"r"}]}`)
	if !ok || len(out.Findings) != 1 {
		t.Fatalf("parse: ok=%v len=%d", ok, len(out.Findings))
	}
	if out.Findings[0].Rule != "go" {
		t.Fatalf("rule not trimmed/mapped: %q", out.Findings[0].Rule)
	}

	// Absent rule => empty (back-compat).
	out2, _ := parseFindings(`{"findings":[{"file":"a.go","existing_code":"x","severity":"low","category":"bug","rationale":"r"}]}`)
	if out2.Findings[0].Rule != "" {
		t.Fatalf("absent rule must be empty, got %q", out2.Findings[0].Rule)
	}

	// Over-long rule is rune-capped, not rejected.
	long := strings.Repeat("é", maxRuleLen+50)
	out3, _ := parseFindings(`{"findings":[{"file":"a.go","existing_code":"x","severity":"low","category":"bug","rule":"` + long + `","rationale":"r"}]}`)
	if n := len([]rune(out3.Findings[0].Rule)); n != maxRuleLen {
		t.Fatalf("rule cap: got %d runes, want %d", n, maxRuleLen)
	}
}

func TestParseFindingsPlainAndEmpty(t *testing.T) {
	if out, ok := parseFindings(`{"findings":[]}`); !ok || len(out.Findings) != 0 {
		t.Fatalf("empty findings: ok=%v len=%d", ok, len(out.Findings))
	}
	if _, ok := parseFindings("not json"); ok {
		t.Fatal("non-JSON must not parse")
	}
	if _, ok := parseFindings(""); ok {
		t.Fatal("empty must not parse")
	}
}

// The same review pass may carry an optional walkthrough + per-file digest; both
// round-trip into ReviewOutput verbatim (escaping happens at render, not parse).
func TestParseFindingsWalkthroughRoundTrips(t *testing.T) {
	model := `{"findings":[],"walkthrough":"Adds bounds checks to the loop.","file_summaries":{"app.go":"Tightens the loop bound.","util.go":"Adds a helper."}}`
	out, ok := parseFindings(model)
	if !ok {
		t.Fatal("parse failed")
	}
	if out.Walkthrough != "Adds bounds checks to the loop." {
		t.Fatalf("walkthrough not parsed: %q", out.Walkthrough)
	}
	if out.FileSummaries["app.go"] != "Tightens the loop bound." || out.FileSummaries["util.go"] != "Adds a helper." {
		t.Fatalf("file_summaries not parsed: %+v", out.FileSummaries)
	}
}

// A response WITHOUT the new fields yields empty walkthrough/digest, the legacy
// shape stays back-compatible.
func TestParseFindingsWalkthroughBackCompat(t *testing.T) {
	out, ok := parseFindings(`{"findings":[{"file":"a.go","existing_code":"x","severity":"low","category":"bug","rationale":"r"}]}`)
	if !ok {
		t.Fatal("parse failed")
	}
	if out.Walkthrough != "" || out.FileSummaries != nil {
		t.Fatalf("absent fields must degrade to empty: walkthrough=%q summaries=%+v", out.Walkthrough, out.FileSummaries)
	}
}

// Over-long walkthrough/summary text is truncated (rune-safe) to bound the extra
// output tokens the additive fields add to every review.
func TestParseFindingsWalkthroughLengthCaps(t *testing.T) {
	longWalk := strings.Repeat("é", maxWalkthroughLen+50)
	longSummary := strings.Repeat("x", maxFileSummaryLen+50)
	model := `{"findings":[],"walkthrough":"` + longWalk + `","file_summaries":{"a.go":"` + longSummary + `"}}`
	out, ok := parseFindings(model)
	if !ok {
		t.Fatal("parse failed")
	}
	if n := len([]rune(out.Walkthrough)); n != maxWalkthroughLen {
		t.Fatalf("walkthrough not capped: got %d runes, want %d", n, maxWalkthroughLen)
	}
	if n := len([]rune(out.FileSummaries["a.go"])); n != maxFileSummaryLen {
		t.Fatalf("file summary not capped: got %d runes, want %d", n, maxFileSummaryLen)
	}
}

// Over the maxFileSummaryKeys cap the kept subset is the alphabetically-first N
// keys, deterministically, Go map iteration order must not leak into the output,
// or re-reviews of the same diff would render a different per-file digest.
func TestParseFindingsFileSummariesDeterministicTruncation(t *testing.T) {
	summaries := make(map[string]string, maxFileSummaryKeys+50)
	for i := 0; i < maxFileSummaryKeys+50; i++ {
		summaries[fmt.Sprintf("file-%04d.go", i)] = "note"
	}
	body, err := json.Marshal(rawFindings{FileSummaries: summaries})
	if err != nil {
		t.Fatal(err)
	}
	out, ok := parseFindings(string(body))
	if !ok {
		t.Fatal("parse failed")
	}
	if len(out.FileSummaries) != maxFileSummaryKeys {
		t.Fatalf("want %d kept summaries, got %d", maxFileSummaryKeys, len(out.FileSummaries))
	}
	if _, ok := out.FileSummaries["file-0000.go"]; !ok {
		t.Fatal("the alphabetically-first key must be kept")
	}
	if _, ok := out.FileSummaries[fmt.Sprintf("file-%04d.go", maxFileSummaryKeys+49)]; ok {
		t.Fatal("a key past the sorted cap must be dropped")
	}
	again, _ := parseFindings(string(body))
	if !reflect.DeepEqual(out.FileSummaries, again.FileSummaries) {
		t.Fatal("truncation must be deterministic across runs")
	}
}

// The optional diagram field round-trips verbatim and is rune-capped; an absent
// field degrades to empty (back-compatible).
func TestParseFindingsDiagram(t *testing.T) {
	out, ok := parseFindings(`{"findings":[],"diagram":"flowchart TD\n A-->B"}`)
	if !ok {
		t.Fatal("parse failed")
	}
	if out.Diagram != "flowchart TD\n A-->B" {
		t.Fatalf("diagram not parsed: %q", out.Diagram)
	}
	// Absent → empty.
	out2, _ := parseFindings(`{"findings":[]}`)
	if out2.Diagram != "" {
		t.Fatalf("absent diagram must be empty, got %q", out2.Diagram)
	}
	// Over-long → capped.
	long := strings.Repeat("x", maxDiagramLen+50)
	out3, _ := parseFindings(`{"findings":[],"diagram":"` + long + `"}`)
	if n := len([]rune(out3.Diagram)); n != maxDiagramLen {
		t.Fatalf("diagram not capped: got %d runes, want %d", n, maxDiagramLen)
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
	creds := Credentials{Kind: config.KindAnthropic, APIKey: secretToken, Model: config.DefaultAnthropicModel}
	a, err := New(creds, 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a == nil {
		t.Fatal("New returned nil")
	}
	want := []engine.Finding{{Severity: "low", Category: "bug", QuotedCode: "z := 1"}}
	fa := &fakeAgent{findings: want, maxEmpty: 1}
	out, err := fa.Review(stdctx.Background(), Context{Text: "ctx"})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	for _, f := range out.Findings {
		if strings.Contains(f.Rationale, secretToken) ||
			strings.Contains(f.QuotedCode, secretToken) ||
			strings.Contains(f.SuggestedPatch, secretToken) ||
			strings.Contains(f.Category, secretToken) ||
			strings.Contains(f.Severity, secretToken) {
			t.Fatal("API token leaked into a returned Finding")
		}
	}
}
