package symbolcontext

type Spec struct {
	Name        string
	Description string
	Properties  map[string]any
	Required    []string
}

func ToolSpec() Spec {
	return Spec{
		Name:        "symbol_context",
		Description: "Fetch bounded read-only code-intelligence context from the reviewed revision. Use before file_read when locating symbols, incoming/outgoing calls, implementations, or dbt/SQL dependencies.",
		Properties: map[string]any{
			"relation": map[string]any{
				"type":        "string",
				"description": "code-intelligence relation: document_symbols, definition, references, incoming_calls, outgoing_calls, implementations, dependencies",
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
