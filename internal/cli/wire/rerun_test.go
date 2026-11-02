package wire

import (
	"testing"

	mgithub "github.com/vanducng/miu-cr/internal/github"
)

func TestReRunComment(t *testing.T) {
	for _, tt := range []struct {
		name       string
		inActions  bool
		env        string
		configured []string
		want       string
	}{
		{"outside actions", false, "", nil, ""},
		{"outside actions ignores config", false, `/miucr review`, []string{`/miucr review`}, ""},
		{"actions default", true, "", nil, mgithub.DefaultReRunComment},
		{"config pattern", true, "", []string{`(^|\s)@review-bot\s+review\b`}, "@review-bot review"},
		{"env overrides config", true, `(^|\s)/recheck\b`, []string{`@review-bot`}, "/recheck"},
		{"blank env falls back to config", true, "  ", []string{`/rerun`}, "/rerun"},
		{"non-literal config omits, no default", true, "", []string{`.+`}, ""},
		{"non-literal env omits, no default", true, `.+`, nil, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := reRunComment(tt.inActions, tt.env, tt.configured); got != tt.want {
				t.Fatalf("reRunComment = %q, want %q", got, tt.want)
			}
		})
	}
}
