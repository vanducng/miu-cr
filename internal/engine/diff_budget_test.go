package engine

import "testing"

func TestDiffBudget(t *testing.T) {
	const rulesCapDefault = 4096
	tests := []struct {
		name     string
		total    int
		rulesCap int
		want     int
	}{
		{"small total, large rules cap keeps a usable diff share", 300, rulesCapDefault, 150},
		{"disabled total stays disabled", 0, rulesCapDefault, 0},
		{"negative total stays disabled", -5, rulesCapDefault, -5},
		{"total far above rules cap subtracts full cap", 100000, rulesCapDefault, 100000 - rulesCapDefault},
		{"rules cap eats at most half", 4096, 4096, 2048},
		{"non-positive rules cap gives diff the whole total", 300, 0, 300},
		{"tiny total never drops below 1", 1, rulesCapDefault, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := diffBudget(tt.total, tt.rulesCap)
			if got != tt.want {
				t.Fatalf("diffBudget(%d, %d) = %d, want %d", tt.total, tt.rulesCap, got, tt.want)
			}
			// Contract: for a positive total the result is always in [1, total].
			if tt.total > 0 && (got < 1 || got > tt.total) {
				t.Fatalf("diffBudget(%d, %d) = %d outside [1, %d]", tt.total, tt.rulesCap, got, tt.total)
			}
		})
	}
}
