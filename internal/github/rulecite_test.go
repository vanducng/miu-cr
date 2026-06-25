package github

import (
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine"
)

func citeInfo() *PRInfo {
	return &PRInfo{Owner: "o", Repo: "r", Number: 1, HeadSHA: "abc123", HTMLBase: "https://github.com/o/r"}
}

// A repo (Linkable) citation renders "(per [stem](<blobURL of repo-relative path>))";
// the link path is the wire-supplied repo-relative path, never an absolute one.
func TestRuleCitationRepoLinks(t *testing.T) {
	cites := map[string]RuleCitation{"go": {RepoRelPath: ".miu/cr/rules/go.md", Linkable: true}}
	f := engine.Finding{Severity: "high", Category: "bug", Rule: "go", Rationale: "x"}
	body, _ := commentBody(citeInfo(), f, "", PostReviewOptions{RuleCitations: cites}, false)
	if !strings.Contains(body, "(per [go](<https://github.com/o/r/blob/abc123/.miu/cr/rules/go.md>))") {
		t.Fatalf("repo rule must render a repo-relative link:\n%s", body)
	}
}

// A user (cite-only) citation renders "(per <stem>)" as TEXT — no link, and the
// absolute home path the wire layer withheld never appears.
func TestRuleCitationUserTextOnly(t *testing.T) {
	cites := map[string]RuleCitation{"team": {Linkable: false}}
	f := engine.Finding{Severity: "low", Category: "style", Rule: "team", Rationale: "x"}
	body, _ := commentBody(citeInfo(), f, "", PostReviewOptions{RuleCitations: cites}, false)
	if !strings.Contains(body, "(per team)") {
		t.Fatalf("user rule must be cited as text:\n%s", body)
	}
	if strings.Contains(body, "(per [") || strings.Contains(body, "blob/") {
		t.Fatalf("user rule must NOT be linked:\n%s", body)
	}
}

// A built-in stem is cite-only too (the wire layer never sets a path for it), so
// it renders as text and never a defaults/* link.
func TestRuleCitationBuiltinTextOnly(t *testing.T) {
	cites := map[string]RuleCitation{"security": {Linkable: false}}
	f := engine.Finding{Severity: "high", Category: "security", Rule: "security", Rationale: "x"}
	body, _ := commentBody(citeInfo(), f, "", PostReviewOptions{RuleCitations: cites}, false)
	if !strings.Contains(body, "(per security)") {
		t.Fatalf("builtin rule must be cited as text:\n%s", body)
	}
	if strings.Contains(body, "defaults/") || strings.Contains(body, "blob/") {
		t.Fatalf("builtin rule must NOT be linked:\n%s", body)
	}
}

// A stem matching NO loaded rule is dropped entirely (anti-hallucination): no
// "(per …)" text, no link.
func TestRuleCitationUnmatchedDropped(t *testing.T) {
	cites := map[string]RuleCitation{"go": {RepoRelPath: ".miu/cr/rules/go.md", Linkable: true}}
	f := engine.Finding{Severity: "low", Category: "bug", Rule: "hallucinated", Rationale: "x"}
	body, _ := commentBody(citeInfo(), f, "", PostReviewOptions{RuleCitations: cites}, false)
	if strings.Contains(body, "per ") {
		t.Fatalf("unmatched rule citation must be dropped:\n%s", body)
	}
}

// An empty Rule or a nil citation map yields no citation, and the body is the
// pre-grounding body byte-for-byte.
func TestRuleCitationAbsentNoChange(t *testing.T) {
	f := engine.Finding{Severity: "low", Category: "bug", Rationale: "x"}
	withMap, _ := commentBody(citeInfo(), f, "", PostReviewOptions{RuleCitations: map[string]RuleCitation{"go": {Linkable: true, RepoRelPath: ".miu/cr/rules/go.md"}}}, false)
	noMap, _ := commentBody(citeInfo(), f, "", PostReviewOptions{}, false)
	if withMap != noMap {
		t.Fatalf("absent Rule must not change the body:\nwith=%q\nno=%q", withMap, noMap)
	}
	if strings.Contains(noMap, "per ") {
		t.Fatalf("no Rule must render no citation:\n%s", noMap)
	}
}

// An untrusted stem is mdInline-escaped at render so a crafted stem can't break
// out of the markdown link/text.
func TestRuleCitationEscapesStem(t *testing.T) {
	cites := map[string]RuleCitation{"pwn](http://evil)": {Linkable: false}}
	f := engine.Finding{Severity: "low", Category: "bug", Rule: "pwn](http://evil)", Rationale: "x"}
	body, _ := commentBody(citeInfo(), f, "", PostReviewOptions{RuleCitations: cites}, false)
	if strings.Contains(body, "pwn](http://evil)") {
		t.Fatalf("untrusted stem must be mdInline-escaped:\n%s", body)
	}
}

// The overflow list grounds an omitted finding's cited rule the same way the
// inline comment does.
func TestRuleCitationInOverflow(t *testing.T) {
	info := citeInfo()
	omitted := []engine.Finding{{File: "a.go", Line: 5, Severity: "high", Category: "bug", Rule: "go", Rationale: "leak"}}
	out := RenderSummaryFull(info, nil, nil, 1, omitted, nil, SummaryOptions{
		RuleCitations: map[string]RuleCitation{"go": {RepoRelPath: ".miu/cr/rules/go.md", Linkable: true}},
	})
	if !strings.Contains(out, "(per [go](<https://github.com/o/r/blob/abc123/.miu/cr/rules/go.md>))") {
		t.Fatalf("overflow entry must carry the linked citation:\n%s", out)
	}
}
