package config

import (
	"errors"
	"testing"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
)

func boolPtr(b bool) *bool { return &b }

// stubReviewValidators wires permissive/closed enum predicates for the test and
// restores them after.
func stubReviewValidators(t *testing.T) {
	t.Helper()
	prevG, prevF, prevM := gateValidator, filterModeValidator, minSeverityValidator
	t.Cleanup(func() { gateValidator, filterModeValidator, minSeverityValidator = prevG, prevF, prevM })
	inSet := func(set ...string) func(string) bool {
		return func(s string) bool {
			for _, v := range set {
				if v == s {
					return true
				}
			}
			return false
		}
	}
	gateValidator = inSet("none", "info", "low", "medium", "high", "critical")
	filterModeValidator = inSet("added", "diff_context", "file", "nofilter")
	minSeverityValidator = inSet("none", "info", "low", "medium", "high", "critical")
}

func TestValidateReview(t *testing.T) {
	stubReviewValidators(t)
	tests := []struct {
		name    string
		r       Review
		wantErr bool
	}{
		{"empty ok", Review{}, false},
		{"all valid", Review{Gate: "high", FilterMode: "file", MinSeverity: "low", Timeout: "5m", Suggest: boolPtr(true)}, false},
		{"bad gate", Review{Gate: "huge"}, true},
		{"bad filter_mode", Review{FilterMode: "bogus"}, true},
		{"bad min_severity", Review{MinSeverity: "meh"}, true},
		{"bad timeout", Review{Timeout: "5 fortnights"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateReview(tt.r)
			if tt.wantErr != (err != nil) {
				t.Fatalf("ValidateReview err=%v wantErr=%v", err, tt.wantErr)
			}
			if err != nil {
				var ce *clierr.CLIError
				if !errors.As(err, &ce) || ce.Code != "config.invalid" {
					t.Fatalf("want config.invalid CLIError, got %v", err)
				}
			}
		})
	}
}

func TestMergeReviewMergesNewFields(t *testing.T) {
	base := Review{}
	file := Review{Gate: "low", FilterMode: "file", MinSeverity: "medium", Timeout: "120s", Suggest: boolPtr(false)}
	out := mergeReview(base, file)
	if out.Gate != "low" || out.FilterMode != "file" || out.MinSeverity != "medium" || out.Timeout != "120s" {
		t.Fatalf("scalar review fields not merged: %+v", out)
	}
	if out.Suggest == nil || *out.Suggest != false {
		t.Fatalf("Suggest not merged: %+v", out.Suggest)
	}
	// An empty file inherits base.
	keep := mergeReview(Review{Gate: "high"}, Review{})
	if keep.Gate != "high" {
		t.Fatalf("empty file should inherit base gate, got %q", keep.Gate)
	}
}
