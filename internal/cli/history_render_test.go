package cli

import (
	"testing"
	"time"
)

func TestTargetLabel(t *testing.T) {
	cases := []struct {
		name          string
		owner, repo   string
		number        int
		repoDir, want string
	}{
		{"pr with number", "acme", "widgets", 7, "/tmp/repo", "acme/widgets#7"},
		{"owner+repo no number falls back to owner/repo", "acme", "widgets", 0, "/tmp/repo", "acme/widgets"},
		{"owner+repo negative number falls back to owner/repo", "acme", "widgets", -1, "", "acme/widgets"},
		{"local repo dir", "", "", 0, "/tmp/repo", "/tmp/repo"},
		{"bare repo", "", "widgets", 0, "", "widgets"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := target(tc.owner, tc.repo, tc.number, tc.repoDir); got != tc.want {
				t.Fatalf("target(%q,%q,%d,%q)=%q, want %q", tc.owner, tc.repo, tc.number, tc.repoDir, got, tc.want)
			}
		})
	}
}

func TestRelativeSpan(t *testing.T) {
	cases := []struct {
		in      string
		wantDur time.Duration
		wantOK  bool
	}{
		{"7d", 7 * 24 * time.Hour, true},
		{"24h", 24 * time.Hour, true},
		{"0d", 0, true},
		{"x", 0, false},
		{"-3d", 0, false},
		// large values must be rejected, never overflow into a negative duration.
		{"100000000000000d", 0, false},
		{"100000000000000h", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := relativeSpan(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("relativeSpan(%q) ok=%v, want %v", tc.in, ok, tc.wantOK)
			}
			if ok && got != tc.wantDur {
				t.Fatalf("relativeSpan(%q)=%v, want %v", tc.in, got, tc.wantDur)
			}
			if got < 0 {
				t.Fatalf("relativeSpan(%q)=%v overflowed to negative", tc.in, got)
			}
		})
	}
}
