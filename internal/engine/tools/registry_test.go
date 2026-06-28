package tools

import (
	"testing"
)

func TestSpecsAlwaysIncludeSymbolContext(t *testing.T) {
	specs := Specs()
	if len(specs) != 3 || specs[2].Name != "symbol_context" {
		t.Fatalf("specs = %+v", specs)
	}
}
