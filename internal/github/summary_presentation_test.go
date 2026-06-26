package github

import "testing"

func TestCapBullets(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"empty", "", 5, ""},
		{"whitespace", "   \n  ", 5, ""},
		{"under cap unchanged", "- a\n- b", 5, "- a\n- b"},
		{"drops surplus bullets", "- a\n- b\n- c\n- d\n- e\n- f", 5, "- a\n- b\n- c\n- d\n- e"},
		{"keeps leading prose + first bullet", "intro\n- a\n- b", 1, "intro\n- a"},
		{"drops orphaned continuation of a dropped bullet", "- a\n  detail a\n- b\n  detail b", 1, "- a\n  detail a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := capBullets(tt.in, tt.n); got != tt.want {
				t.Fatalf("capBullets(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
			}
		})
	}
}
