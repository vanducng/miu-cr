package wire

import "testing"

func TestReviewErrorIsOperational(t *testing.T) {
	tests := []struct {
		name      string
		code      string
		retryable bool
		want      bool
	}{
		{"provider unavailable", "agent.unavailable", false, true},
		{"rate limited", "provider.rate_limited", false, true},
		{"quota", "quota.exceeded", false, true},
		{"timeout", "review.timeout", false, true},
		{"github unavailable", "github.unavailable", false, true},
		{"store unavailable", "store.unavailable", false, true},
		{"retryable provider family", "provider.busy", true, true},
		{"retryable unknown family", "internal.retryable", true, false},
		{"internal", "internal.error", false, false},
		{"config", "config.invalid", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reviewErrorIsOperational(tt.code, tt.retryable); got != tt.want {
				t.Fatalf("reviewErrorIsOperational(%q, %v)=%v want %v", tt.code, tt.retryable, got, tt.want)
			}
		})
	}
}

func TestReviewErrorTitle(t *testing.T) {
	tests := []struct {
		code string
		want string
	}{
		{"agent.auth_failed", "Provider authentication issue"},
		{"agent.unavailable", "Provider unavailable"},
		{"provider.rate_limited", "Provider rate limit"},
		{"quota.exceeded", "Provider quota reached"},
		{"review.timeout", "Review timed out"},
		{"review.stalled", "Review stalled"},
		{"github.rate_limited", "GitHub rate limit"},
		{"github.unavailable", "GitHub unavailable"},
		{"store.unavailable", "Review store unavailable"},
		{"internal.error", "Operational review issue"},
	}
	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			if got := reviewErrorTitle(tt.code); got != tt.want {
				t.Fatalf("reviewErrorTitle(%q)=%q want %q", tt.code, got, tt.want)
			}
		})
	}
}

func TestReviewErrorTextLooksOperational(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want bool
	}{
		{"timeout", "context deadline exceeded", true},
		{"rate", "provider rate limit reached", true},
		{"quota", "quota exhausted", true},
		{"overload", "service overloaded", true},
		{"529", "upstream returned 529", true},
		{"network", "network connection reset", true},
		{"internal panic", "panic in renderer", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reviewErrorTextLooksOperational(tt.msg); got != tt.want {
				t.Fatalf("reviewErrorTextLooksOperational(%q)=%v want %v", tt.msg, got, tt.want)
			}
		})
	}
}
