package github

import "sort"

// presentation is the section-flag set a review output format resolves to. Each
// flag gates one human-facing block of the summary/inline comment; the hidden
// upsert markers, the plain Result line, inline findings, and the footer render
// in every format and are NOT flagged. Adding a format = one entry in modes
// plus a golden test; a new block adds a flag here and a gate at its render site.
type presentation struct {
	Heading       bool // "## Code Review Summary" H2
	Walkthrough   bool // "What changed:" narrative
	Diagram       bool // opt-in Mermaid change diagram (narrative chrome, rides --walkthrough-diagram)
	ResultBadges  bool // shields severity chips on the Result line (else a plain count)
	ChangesTable  bool // "Important Files Changed" per-file table
	ReviewRef     bool // collapsed "Review reference" (priority legend + effort/context)
	PriorityBadge bool // per-finding shields priority badge on inline comments
}

// modes is the closed registry of named output formats. The empty/unset format
// resolves to "full" so existing output stays byte-identical.
var modes = map[string]presentation{
	"full":    {Heading: true, Walkthrough: true, Diagram: true, ResultBadges: true, ChangesTable: true, ReviewRef: true, PriorityBadge: true},
	"minimal": {},
}

// presentationFor resolves a format name to its preset. Empty and unknown names
// fall back to "full"; unknown values are rejected earlier by ValidFormat at the
// config/flag boundary, so the fallback only guards a direct zero-value render.
func presentationFor(name string) presentation {
	if p, ok := modes[name]; ok {
		return p
	}
	return modes["full"]
}

// ValidFormat reports whether s is a recognized --format / [review].format value.
// Empty is valid (the documented default, "full").
func ValidFormat(s string) bool {
	if s == "" {
		return true
	}
	_, ok := modes[s]
	return ok
}

// ModeNames returns the registered format names sorted, the single source of
// truth for the validator's "want" hint and --help.
func ModeNames() []string {
	names := make([]string, 0, len(modes))
	for name := range modes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
