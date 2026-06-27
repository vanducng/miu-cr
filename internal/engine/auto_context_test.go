package engine

import (
	"testing"

	"github.com/vanducng/miu-cr/internal/engine/diff"
)

func TestAutoContextHops(t *testing.T) {
	tests := []struct {
		name string
		in   []diff.Diff
		want int
	}{
		{name: "small", in: []diff.Diff{{Insertions: 10, Deletions: 5}}, want: 2},
		{name: "medium files", in: make([]diff.Diff, 10), want: 3},
		{name: "medium churn", in: []diff.Diff{{Insertions: 300}}, want: 3},
		{name: "large files", in: make([]diff.Diff, 25), want: 3},
		{name: "large churn", in: []diff.Diff{{Insertions: 800}}, want: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := autoContextHops(tt.in); got != tt.want {
				t.Fatalf("autoContextHops()=%d, want %d", got, tt.want)
			}
		})
	}
}
