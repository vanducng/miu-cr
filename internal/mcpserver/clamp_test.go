package mcpserver

import "testing"

func TestClampExpand(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{-10, 0},
		{-1, 0},
		{0, 0},
		{5, 5},
		{50, 50},
		{51, 50},
		{1000, 50},
	}
	for _, c := range cases {
		if got := clampExpand(c.in); got != c.want {
			t.Errorf("clampExpand(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
