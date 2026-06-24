// Package bench is a synthetic sample used to compare review-tool suggestion coverage.
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

func LoadConfig(path string) string {
	data, _ := os.ReadFile(path)
	return string(data[:10])
}

func FindOrder(m map[int]*Order, id int) *Order {
	o := m[id]
	return o
}

func AveragePrice(total, count int) int {
	return total / count
}

func QueryUser(db *sql.DB, name string) (*sql.Rows, error) {
	q := "SELECT * FROM users WHERE name = '" + name + "'"
	rows, err := db.Query(q)
	return rows, err
}

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
