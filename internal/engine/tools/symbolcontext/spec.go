package symbolcontext

type Spec struct {
	Name        string
	Description string
	Properties  map[string]any
	Required    []string
}

const relationGuide = "Relation guide: document_symbols maps the symbols in one file; definition finds where a symbol is declared; references finds textual usages of a symbol, constant, config, model, or table; incoming_calls finds likely callers of a function; outgoing_calls lists likely callees from a function body; implementations finds concrete definition candidates for a type/interface/component; dependencies traces dbt/SQL ref/source dependencies."

func ToolSpec() Spec {
	return Spec{
		Name:        "symbol_context",
		Description: "Fetch bounded read-only code-intelligence context from the reviewed revision. Use before file_read when locating symbols, callers, callees, implementations, or dbt/SQL dependencies. " + relationGuide,
		Properties: map[string]any{
			"relation": map[string]any{
				"type":        "string",
				"description": "code-intelligence relation. " + relationGuide,
				"enum":        []string{"document_symbols", "definition", "references", "incoming_calls", "outgoing_calls", "implementations", "dependencies"},
			},
			"symbol": map[string]any{"type": "string", "description": "symbol, model, table, dependency, class, function, or component name; required except document_symbols and file-scoped dependencies"},
			"file":   map[string]any{"type": "string", "description": "repo-relative file path; required for document_symbols, optional scope for other relations"},
			"line":   map[string]any{"type": "integer", "description": "optional 1-based line number"},
			"limit":  map[string]any{"type": "integer", "description": "optional result limit"},
		},
		Required: []string{"relation"},
	}
}
