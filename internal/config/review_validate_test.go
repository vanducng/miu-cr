package config

import (
	"errors"
	"testing"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
)

func boolPtr(b bool) *bool { return &b }
func intPtr(n int) *int    { return &n }

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
		{"all valid", Review{Gate: "high", FilterMode: "file", MinSeverity: "low", Timeout: "5m", Expand: intPtr(20), TokenBudget: intPtr(0), DeepContext: boolPtr(true), ContextHops: intPtr(5), Conversation: boolPtr(true), Suggest: boolPtr(true)}, false},
		{"bad gate", Review{Gate: "huge"}, true},
		{"bad filter_mode", Review{FilterMode: "bogus"}, true},
		{"bad min_severity", Review{MinSeverity: "meh"}, true},
		{"bad timeout", Review{Timeout: "5 fortnights"}, true},
		{"bad expand", Review{Expand: intPtr(-1)}, true},
		{"bad token budget", Review{TokenBudget: intPtr(-1)}, true},
		{"context hops zero ok", Review{ContextHops: intPtr(0)}, false},
		{"context hops negative", Review{ContextHops: intPtr(-1)}, true},
		{"bad context hops", Review{ContextHops: intPtr(6)}, true},
		{"subagents valid", Review{Subagents: ReviewSubagents{Mode: "auto", MaxParallel: 2, MinFiles: 4, Agents: []ReviewSubagent{{Name: "go", Include: []string{"**/*.go"}}}}}, false},
		{"subagents bad mode", Review{Subagents: ReviewSubagents{Mode: "sometimes", Agents: []ReviewSubagent{{Name: "go", Include: []string{"**/*.go"}}}}}, true},
		{"subagents empty include", Review{Subagents: ReviewSubagents{Mode: "always", Agents: []ReviewSubagent{{Name: "go"}}}}, true},
		{"subagents duplicate name", Review{Subagents: ReviewSubagents{Mode: "always", Agents: []ReviewSubagent{{Name: "go", Include: []string{"**/*.go"}}, {Name: "go", Include: []string{"**/*.ts"}}}}}, true},
		{"comment trigger regex valid", Review{PRFilter: HostPRFilter{CommentTriggerRegexes: []string{`(^|\s)(/miucr review\b|@vanducng\b)`}}}, false},
		{"comment trigger regex invalid", Review{PRFilter: HostPRFilter{CommentTriggerRegexes: []string{"["}}}, true},
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
	file := Review{Gate: "low", FilterMode: "file", MinSeverity: "medium", Timeout: "120s", Expand: intPtr(12), TokenBudget: intPtr(0), DeepContext: boolPtr(true), ContextHops: intPtr(3), Conversation: boolPtr(true), Suggest: boolPtr(false), Subagents: ReviewSubagents{Mode: "auto", Agents: []ReviewSubagent{{Name: "go", Include: []string{"**/*.go"}}}}}
	out := mergeReview(base, file)
	if out.Gate != "low" || out.FilterMode != "file" || out.MinSeverity != "medium" || out.Timeout != "120s" {
		t.Fatalf("scalar review fields not merged: %+v", out)
	}
	if out.Expand == nil || *out.Expand != 12 || out.TokenBudget == nil || *out.TokenBudget != 0 || out.DeepContext == nil || !*out.DeepContext || out.ContextHops == nil || *out.ContextHops != 3 || out.Conversation == nil || !*out.Conversation {
		t.Fatalf("context review fields not merged: %+v", out)
	}
	if out.Suggest == nil || *out.Suggest != false {
		t.Fatalf("Suggest not merged: %+v", out.Suggest)
	}
	if out.Subagents.Mode != "auto" || len(out.Subagents.Agents) != 1 {
		t.Fatalf("Subagents not merged: %+v", out.Subagents)
	}
	pr := mergeReview(Review{}, Review{PatchRepair: boolPtr(true)})
	if pr.PatchRepair == nil || *pr.PatchRepair != true {
		t.Fatalf("PatchRepair not merged: %+v", pr.PatchRepair)
	}
	// An empty file inherits base.
	keep := mergeReview(Review{Gate: "high"}, Review{})
	if keep.Gate != "high" {
		t.Fatalf("empty file should inherit base gate, got %q", keep.Gate)
	}
}
