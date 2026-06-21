package store_test

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/store/postgres"
	"github.com/vanducng/miu-cr/internal/store/sqlite"
)

// TestSchemaParity asserts both backends define the same tables with the same
// column names in the same order. Column TYPES are intentionally NOT compared
// (INTEGER vs BIGINT is a justified dialect difference); the contract is "same
// tables, same columns" so the two SQL literals can't silently drift apart.
func TestSchemaParity(t *testing.T) {
	sq := parseSchema(sqlite.SchemaSQL)
	pg := parseSchema(postgres.SchemaSQL)

	if !reflect.DeepEqual(tableNames(sq), tableNames(pg)) {
		t.Fatalf("table set differs:\n sqlite=%v\n postgres=%v", tableNames(sq), tableNames(pg))
	}
	for tbl, sqCols := range sq {
		pgCols := pg[tbl]
		if !reflect.DeepEqual(sqCols, pgCols) {
			t.Fatalf("table %q column mismatch:\n sqlite=%v\n postgres=%v", tbl, sqCols, pgCols)
		}
	}
}

var (
	tableRe = regexp.MustCompile(`(?is)CREATE TABLE IF NOT EXISTS\s+(\w+)\s*\((.*?)\);`)
	// a column line starts with an identifier; PRIMARY KEY/CHECK table constraints
	// and standalone constraint clauses are skipped.
	colRe = regexp.MustCompile(`^(\w+)\s+`)
)

// parseSchema returns table -> ordered column names from a schema literal.
func parseSchema(sql string) map[string][]string {
	out := map[string][]string{}
	for _, m := range tableRe.FindAllStringSubmatch(sql, -1) {
		table := m[1]
		var cols []string
		for _, raw := range splitTopLevel(m[2]) {
			line := strings.TrimSpace(raw)
			if line == "" {
				continue
			}
			upper := strings.ToUpper(line)
			if strings.HasPrefix(upper, "PRIMARY KEY") || strings.HasPrefix(upper, "CHECK") ||
				strings.HasPrefix(upper, "UNIQUE") || strings.HasPrefix(upper, "FOREIGN KEY") ||
				strings.HasPrefix(upper, "CONSTRAINT") {
				continue
			}
			if cm := colRe.FindStringSubmatch(line); cm != nil {
				cols = append(cols, cm[1])
			}
		}
		out[table] = cols
	}
	return out
}

// splitTopLevel splits a table body on commas not nested inside parentheses
// (so a CHECK(status IN ('a','b')) clause isn't split mid-list).
func splitTopLevel(body string) []string {
	var parts []string
	depth, start := 0, 0
	for i, r := range body {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, body[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, body[start:])
	return parts
}

func tableNames(m map[string][]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	// deterministic order for comparison
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
