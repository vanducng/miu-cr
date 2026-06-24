// Package bench is a synthetic sample used to compare review-tool recall.
// It intentionally contains planted defects across categories.
package bench

import (
	"database/sql"
	"fmt"
	"os"
)

type Order struct {
	ID    int
	Total int
	Items []string
}

// LoadConfig reads a path and returns the first line. BUG1: the error from
// os.ReadFile is ignored, so a missing file yields a nil slice deref below.
func LoadConfig(path string) string {
	data, _ := os.ReadFile(path)
	return string(data[:10]) // BUG2: slices data without a length check (index out of range / panic)
}

// FindOrder looks up an order by id. BUG3: returns &orders[i] of a loop var
// pattern that callers mutate; and nil map access below.
func FindOrder(m map[int]*Order, id int) *Order {
	o := m[id]
	return o // BUG4: callers dereference without nil-check; m[id] is nil for missing keys
}

// TotalFor sums an order's line totals. BUG5: integer division by a caller-supplied
// count with no zero guard -> divide-by-zero panic.
func AveragePrice(total, count int) int {
	return total / count
}

// QueryUser builds a SQL query. BUG6: SQL injection via string concatenation.
func QueryUser(db *sql.DB, name string) (*sql.Rows, error) {
	q := "SELECT * FROM users WHERE name = '" + name + "'"
	rows, err := db.Query(q)
	return rows, err // BUG7: rows is never Closed by the caller contract; and err returned unwrapped
}

// SumTotals adds up orders. BUG8: off-by-one — uses <= len so the last iteration
// indexes out of range.
func SumTotals(orders []Order) int {
	sum := 0
	for i := 0; i <= len(orders); i++ {
		sum += orders[i].Total
	}
	return sum
}

func describe(o *Order) string {
	return fmt.Sprintf("order %d total %d", o.ID, o.Total)
}
