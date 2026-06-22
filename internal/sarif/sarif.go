// Package sarif emits SARIF 2.1.0 from review findings so code-scanning tools
// (GitHub Security tab via github/codeql-action/upload-sarif, IDEs) can ingest
// them. It is a leaf package: stdlib encoding/json only, its own Finding input
// shape (no engine/cli import), and repo-RELATIVE URIs only — never an absolute
// or secret path.
package sarif

import (
	"encoding/json"
	"io"
	"strings"
)

const (
	schemaURL = "https://raw.githubusercontent.com/oasis-tcs/sarif-spec/master/Schemata/sarif-schema-2.1.0.json"
	version   = "2.1.0"
	toolName  = "miucr"
	// defaultRuleID is the fallback rule id for a finding with no Category, so
	// every result keeps a valid rule association (strict SARIF 2.1.0 validators
	// reject a result with neither ruleId nor ruleIndex).
	defaultRuleID = "general"
)

// Finding is the minimal input shape EmitSARIF maps 1:1, decoupled from
// engine.Finding so this package stays a leaf. File MUST be a repo-relative path.
type Finding struct {
	File           string
	Line           int
	EndLine        int
	Severity       string
	Category       string
	Rationale      string
	SuggestedPatch string
	QuotedCode     string
}

type sarifLog struct {
	Schema  string `json:"$schema"`
	Version string `json:"version"`
	Runs    []run  `json:"runs"`
}

type run struct {
	Tool    tool     `json:"tool"`
	Results []result `json:"results"`
}

type tool struct {
	Driver driver `json:"driver"`
}

type driver struct {
	Name           string `json:"name"`
	InformationURI string `json:"informationUri,omitempty"`
	Version        string `json:"version,omitempty"`
	Rules          []rule `json:"rules,omitempty"`
}

type rule struct {
	ID      string `json:"id"`
	HelpURI string `json:"helpUri,omitempty"`
}

type result struct {
	RuleID    string     `json:"ruleId,omitempty"`
	Level     string     `json:"level"`
	Message   message    `json:"message"`
	Locations []location `json:"locations"`
	Fixes     []fix      `json:"fixes,omitempty"`
}

type message struct {
	Text string `json:"text"`
}

type location struct {
	PhysicalLocation physicalLocation `json:"physicalLocation"`
}

type physicalLocation struct {
	ArtifactLocation artifactLocation `json:"artifactLocation"`
	Region           *region          `json:"region,omitempty"`
}

type artifactLocation struct {
	URI string `json:"uri"`
}

type region struct {
	StartLine int      `json:"startLine,omitempty"`
	EndLine   int      `json:"endLine,omitempty"`
	Snippet   *snippet `json:"snippet,omitempty"`
}

type snippet struct {
	Text string `json:"text"`
}

type fix struct {
	Description     message          `json:"description"`
	ArtifactChanges []artifactChange `json:"artifactChanges,omitempty"`
}

type artifactChange struct {
	ArtifactLocation artifactLocation `json:"artifactLocation"`
	Replacements     []replacement    `json:"replacements"`
}

type replacement struct {
	DeletedRegion   region          `json:"deletedRegion"`
	InsertedContent insertedContent `json:"insertedContent"`
}

type insertedContent struct {
	Text string `json:"text"`
}

// EmitSARIF writes a schema-pinned SARIF 2.1.0 log mapping each finding 1:1: rule
// id = Category (defaultRuleID when empty), result.level from Severity, region from
// Line/EndLine, snippet = QuotedCode, fix description + artifactChanges replacement =
// SuggestedPatch. The driver rule set is the unique set of resolved rule ids.
// toolVersion is the miucr version (informational; omit if empty).
func EmitSARIF(w io.Writer, findings []Finding, toolVersion string) error {
	return EmitSARIFWithLinks(w, findings, toolVersion, nil)
}

// EmitSARIFWithLinks is EmitSARIF plus a category->URL map (TRUSTED config only)
// that sets each matching rule's helpUri. The category match is case-insensitive;
// a nil/empty map yields byte-for-byte EmitSARIF output (no helpUri keys).
func EmitSARIFWithLinks(w io.Writer, findings []Finding, toolVersion string, categoryURLs map[string]string) error {
	results := make([]result, 0, len(findings))
	ruleSet := map[string]bool{}
	var rules []rule
	for _, f := range findings {
		cat := strings.TrimSpace(f.Category)
		if cat == "" {
			cat = defaultRuleID
		}
		if !ruleSet[cat] {
			ruleSet[cat] = true
			rules = append(rules, rule{ID: cat, HelpURI: categoryURLs[strings.ToLower(cat)]})
		}
		results = append(results, toResult(f, cat))
	}

	log := sarifLog{
		Schema:  schemaURL,
		Version: version,
		Runs: []run{{
			Tool: tool{Driver: driver{
				Name:           toolName,
				InformationURI: "https://github.com/vanducng/miu-cr",
				Version:        toolVersion,
				Rules:          rules,
			}},
			Results: results,
		}},
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(log)
}

func toResult(f Finding, cat string) result {
	reg := &region{StartLine: f.Line}
	if f.EndLine > f.Line {
		reg.EndLine = f.EndLine
	}
	if snip := strings.TrimRight(f.QuotedCode, "\n"); snip != "" {
		reg.Snippet = &snippet{Text: snip}
	}
	if reg.StartLine <= 0 && reg.Snippet == nil {
		reg = nil // a drift finding (Line==0, no snippet) carries no region
	}

	r := result{
		RuleID:  cat,
		Level:   levelFor(f.Severity),
		Message: message{Text: messageText(f)},
		Locations: []location{{PhysicalLocation: physicalLocation{
			ArtifactLocation: artifactLocation{URI: relURI(f.File)},
			Region:           reg,
		}}},
	}
	if patch := strings.TrimSpace(f.SuggestedPatch); patch != "" {
		fx := fix{Description: message{Text: patch}}
		// Only emit a machine-applicable replacement when there's a concrete region
		// to delete; a region-less (drift, Line<=0) fix stays description-only.
		if f.Line > 0 {
			dr := region{StartLine: f.Line}
			if f.EndLine > f.Line {
				dr.EndLine = f.EndLine
			}
			fx.ArtifactChanges = []artifactChange{{
				ArtifactLocation: artifactLocation{URI: relURI(f.File)},
				Replacements:     []replacement{{DeletedRegion: dr, InsertedContent: insertedContent{Text: patch}}},
			}}
		}
		r.Fixes = []fix{fx}
	}
	return r
}

// levelFor maps a miucr severity to a SARIF result.level. SARIF defines only
// error|warning|note|none; critical/high→error, medium→warning, low/info→note.
func levelFor(sev string) string {
	switch strings.ToLower(strings.TrimSpace(sev)) {
	case "critical", "high":
		return "error"
	case "medium":
		return "warning"
	default:
		return "note"
	}
}

func messageText(f Finding) string {
	if r := strings.TrimSpace(f.Rationale); r != "" {
		return r
	}
	return "miucr finding"
}

// relURI normalizes a path into a forward-slash, leading-separator-free
// repo-relative SARIF URI. It strips any leading "/" or "./" so an accidental
// absolute path can never leak into the artifact location.
func relURI(path string) string {
	p := strings.TrimSpace(path)
	p = strings.ReplaceAll(p, "\\", "/")
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	p = strings.TrimPrefix(p, "/")
	return p
}
