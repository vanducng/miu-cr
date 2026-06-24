package engine

import (
	"errors"
	"testing"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
)

func TestValidGate(t *testing.T) {
	for _, g := range []string{"none", "info", "low", "medium", "high", "critical"} {
		if !ValidGate(g) {
			t.Errorf("ValidGate(%q) = false, want true", g)
		}
	}
	for _, g := range []string{"", "HIGH", "warn", "blocker"} {
		if ValidGate(g) {
			t.Errorf("ValidGate(%q) = true, want false", g)
		}
	}
}

func TestValidateInvocation(t *testing.T) {
	tests := []struct {
		name                   string
		staged                 bool
		from, to, commit, gate string
		wantCode               string // "" means no error
	}{
		{name: "staged ok", staged: true, gate: "high"},
		{name: "range ok", from: "main", to: "HEAD", gate: "high"},
		{name: "commit ok", commit: "HEAD~1", gate: "low"},
		{name: "gate none ok", staged: true, gate: "none"},
		{name: "bad gate", staged: true, gate: "warn", wantCode: "review.bad_gate"},
		{name: "empty gate rejected", staged: true, gate: "", wantCode: "review.bad_gate"},
		{name: "no mode", gate: "high", wantCode: "review.no_mode"},
		{name: "half range from", from: "main", gate: "high", wantCode: "review.bad_flags"},
		{name: "half range to", to: "HEAD", gate: "high", wantCode: "review.bad_flags"},
		{name: "staged plus commit", staged: true, commit: "HEAD", gate: "high", wantCode: "review.bad_flags"},
		{name: "range plus commit", from: "a", to: "b", commit: "c", gate: "high", wantCode: "review.bad_flags"},
		{name: "staged plus range", staged: true, from: "a", to: "b", gate: "high", wantCode: "review.bad_flags"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateInvocation(tt.staged, tt.from, tt.to, tt.commit, tt.gate)
			if tt.wantCode == "" {
				if err != nil {
					t.Fatalf("want nil error, got %v", err)
				}
				return
			}
			var ce *clierr.CLIError
			if !errors.As(err, &ce) {
				t.Fatalf("want *clierr.CLIError, got %T: %v", err, err)
			}
			if ce.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", ce.Code, tt.wantCode)
			}
			if ce.Exit != 2 {
				t.Errorf("exit = %d, want 2", ce.Exit)
			}
			if tt.wantCode == "review.bad_gate" && ce.Hint == "" {
				t.Error("review.bad_gate must carry an actionable hint")
			}
		})
	}
}
