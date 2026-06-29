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
	prevG, prevF, prevM, prevFmt, prevPF := gateValidator, filterModeValidator, minSeverityValidator, formatValidator, promptFormatValidator
	t.Cleanup(func() {
		gateValidator, filterModeValidator, minSeverityValidator, formatValidator, promptFormatValidator = prevG, prevF, prevM, prevFmt, prevPF
	})
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
	formatValidator = inSet("full", "minimal")
	promptFormatValidator = inSet("markdown", "xml")
}

func TestValidateReview(t *testing.T) {
	stubReviewValidators(t)
	tests := []struct {
		name    string
		r       Review
		wantErr bool
	}{
		{"empty ok", Review{}, false},
		{"all valid", Review{Gate: "high", FilterMode: "file", MinSeverity: "low", Timeout: "5m", StalledTimeout: "30s", ProviderRetry: ProviderRetry{MaxRetries: intPtr(10), InitialBackoff: "5s", MaxBackoff: "2m", MaxElapsed: "10m"}, Expand: intPtr(20), TokenBudget: intPtr(0), DeepContext: boolPtr(true), ContextHops: intPtr(5), Conversation: boolPtr(true), Suggest: boolPtr(true), Tools: ReviewTools{MaxRetries: intPtr(2), MaxTurns: intPtr(12), RetryBackoff: "250ms", SymbolContext: SymbolContext{MaxBytes: 12000, MaxFiles: 500, MaxParallel: 6}}, Approval: ApprovalPolicy{Mode: "threshold", MaxPriority: "P3", Note: "on_findings"}}, false},
		{"bad gate", Review{Gate: "huge"}, true},
		{"bad filter_mode", Review{FilterMode: "bogus"}, true},
		{"bad min_severity", Review{MinSeverity: "meh"}, true},
		{"format valid", Review{Format: "minimal"}, false},
		{"bad format", Review{Format: "fancy"}, true},
		{"prompt_format markdown ok", Review{PromptFormat: "markdown"}, false},
		{"prompt_format xml ok", Review{PromptFormat: "xml"}, false},
		{"bad prompt_format", Review{PromptFormat: "yaml"}, true},
		{"bad timeout", Review{Timeout: "5 fortnights"}, true},
		{"stalled timeout zero ok", Review{StalledTimeout: "0s"}, false},
		{"bad stalled timeout", Review{StalledTimeout: "-1s"}, true},
		{"provider retry zero ok", Review{ProviderRetry: ProviderRetry{MaxRetries: intPtr(0), InitialBackoff: "0s", MaxBackoff: "0s", MaxElapsed: "0s"}}, false},
		{"bad provider retry max retries negative", Review{ProviderRetry: ProviderRetry{MaxRetries: intPtr(-1)}}, true},
		{"bad provider retry max retries too high", Review{ProviderRetry: ProviderRetry{MaxRetries: intPtr(21)}}, true},
		{"bad provider retry initial backoff", Review{ProviderRetry: ProviderRetry{InitialBackoff: "soon"}}, true},
		{"bad provider retry max backoff", Review{ProviderRetry: ProviderRetry{MaxBackoff: "-1s"}}, true},
		{"bad provider retry max elapsed", Review{ProviderRetry: ProviderRetry{MaxElapsed: "later"}}, true},
		{"bad expand", Review{Expand: intPtr(-1)}, true},
		{"bad token budget", Review{TokenBudget: intPtr(-1)}, true},
		{"context hops zero ok", Review{ContextHops: intPtr(0)}, false},
		{"context hops negative", Review{ContextHops: intPtr(-1)}, true},
		{"bad context hops", Review{ContextHops: intPtr(6)}, true},
		{"bad approval mode", Review{Approval: ApprovalPolicy{Mode: "maybe"}}, true},
		{"bad approval priority", Review{Approval: ApprovalPolicy{Mode: "threshold", MaxPriority: "P9"}}, true},
		{"approval priority outside threshold", Review{Approval: ApprovalPolicy{Mode: "clean", MaxPriority: "P3"}}, true},
		{"bad approval note", Review{Approval: ApprovalPolicy{Mode: "clean", Note: "verbose"}}, true},
		{"bad symbol max bytes", Review{Tools: ReviewTools{SymbolContext: SymbolContext{MaxBytes: -1}}}, true},
		{"bad symbol max files", Review{Tools: ReviewTools{SymbolContext: SymbolContext{MaxFiles: -1}}}, true},
		{"bad symbol max parallel", Review{Tools: ReviewTools{SymbolContext: SymbolContext{MaxParallel: -1}}}, true},
		{"bad tool max retries negative", Review{Tools: ReviewTools{MaxRetries: intPtr(-1)}}, true},
		{"bad tool max retries too high", Review{Tools: ReviewTools{MaxRetries: intPtr(6)}}, true},
		{"tool max turns zero ok", Review{Tools: ReviewTools{MaxTurns: intPtr(0)}}, false},
		{"bad tool max turns negative", Review{Tools: ReviewTools{MaxTurns: intPtr(-1)}}, true},
		{"bad tool max turns too high", Review{Tools: ReviewTools{MaxTurns: intPtr(65)}}, true},
		{"bad tool retry backoff", Review{Tools: ReviewTools{RetryBackoff: "soon"}}, true},
		{"subagents valid", Review{Subagents: ReviewSubagents{Mode: "auto", MaxParallel: 2, MinFiles: 4, Agents: []ReviewSubagent{{Name: "go", Include: []string{"**/*.go"}}}}}, false},
		{"subagents bad mode", Review{Subagents: ReviewSubagents{Mode: "sometimes", Agents: []ReviewSubagent{{Name: "go", Include: []string{"**/*.go"}}}}}, true},
		{"subagents empty include", Review{Subagents: ReviewSubagents{Mode: "always", Agents: []ReviewSubagent{{Name: "go"}}}}, true},
		{"subagents duplicate name", Review{Subagents: ReviewSubagents{Mode: "always", Agents: []ReviewSubagent{{Name: "go", Include: []string{"**/*.go"}}, {Name: "go", Include: []string{"**/*.ts"}}}}}, true},
		{"comment trigger regex valid", Review{PRFilter: HostPRFilter{CommentTriggerRegexes: []string{`(^|\s)(/miucr review\b|@vanducng\b)`}}}, false},
		{"comment trigger regex invalid", Review{PRFilter: HostPRFilter{CommentTriggerRegexes: []string{"["}}}, true},
		{"pr filter rule invalid action", Review{PRFilter: HostPRFilter{Rules: []HostPRFilterRule{{Action: "block", Authors: []string{"vanducng"}}}}}, true},
		{"pr filter rule missing matcher", Review{PRFilter: HostPRFilter{Rules: []HostPRFilterRule{{Action: "include"}}}}, true},
		{"pr filter rule invalid title regex", Review{PRFilter: HostPRFilter{Rules: []HostPRFilterRule{{Action: "exclude", TitleRegexes: []string{"["}}}}}, true},
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
	base := Review{Approval: ApprovalPolicy{Mode: "clean"}}
	file := Review{Gate: "low", FilterMode: "file", MinSeverity: "medium", PromptFormat: "markdown", Timeout: "120s", StalledTimeout: "45s", ProviderRetry: ProviderRetry{MaxRetries: intPtr(7), InitialBackoff: "3s", MaxBackoff: "45s", MaxElapsed: "8m"}, Expand: intPtr(12), TokenBudget: intPtr(0), DeepContext: boolPtr(true), ContextHops: intPtr(3), Conversation: boolPtr(true), Suggest: boolPtr(false), Tools: ReviewTools{MaxRetries: intPtr(3), MaxTurns: intPtr(9), RetryBackoff: "500ms", SymbolContext: SymbolContext{MaxBytes: 12000}}, Approval: ApprovalPolicy{Mode: "threshold", MaxPriority: "P3"}, Subagents: ReviewSubagents{Mode: "auto", Agents: []ReviewSubagent{{Name: "go", Include: []string{"**/*.go"}}}}}
	out := mergeReview(base, file)
	if out.Gate != "low" || out.FilterMode != "file" || out.MinSeverity != "medium" || out.PromptFormat != "markdown" || out.Timeout != "120s" || out.StalledTimeout != "45s" {
		t.Fatalf("scalar review fields not merged: %+v", out)
	}
	if out.ProviderRetry.MaxRetries == nil || *out.ProviderRetry.MaxRetries != 7 || out.ProviderRetry.InitialBackoff != "3s" || out.ProviderRetry.MaxBackoff != "45s" || out.ProviderRetry.MaxElapsed != "8m" {
		t.Fatalf("ProviderRetry not merged: %+v", out.ProviderRetry)
	}
	if out.Expand == nil || *out.Expand != 12 || out.TokenBudget == nil || *out.TokenBudget != 0 || out.DeepContext == nil || !*out.DeepContext || out.ContextHops == nil || *out.ContextHops != 3 || out.Conversation == nil || !*out.Conversation {
		t.Fatalf("context review fields not merged: %+v", out)
	}
	if out.Suggest == nil || *out.Suggest != false {
		t.Fatalf("Suggest not merged: %+v", out.Suggest)
	}
	if out.Tools.MaxRetries == nil || *out.Tools.MaxRetries != 3 || out.Tools.MaxTurns == nil || *out.Tools.MaxTurns != 9 || out.Tools.RetryBackoff != "500ms" || out.Tools.SymbolContext.MaxBytes != 12000 {
		t.Fatalf("Tools not merged: %+v", out.Tools)
	}
	if out.Approval.Mode != "threshold" || out.Approval.MaxPriority != "P3" || out.Approval.Note != "" {
		t.Fatalf("Approval not merged: %+v", out.Approval)
	}
	if out.Subagents.Mode != "auto" || len(out.Subagents.Agents) != 1 {
		t.Fatalf("Subagents not merged: %+v", out.Subagents)
	}
	pr := mergeReview(Review{}, Review{PatchRepair: boolPtr(true)})
	if pr.PatchRepair == nil || *pr.PatchRepair != true {
		t.Fatalf("PatchRepair not merged: %+v", pr.PatchRepair)
	}
	keep := mergeReview(Review{Gate: "high"}, Review{})
	if keep.Gate != "high" {
		t.Fatalf("empty file should inherit base gate, got %q", keep.Gate)
	}
}

func TestMergeApprovalPolicy(t *testing.T) {
	base := ApprovalPolicy{Mode: "threshold", MaxPriority: "P1", Note: "always"}
	inherit := MergeApprovalPolicy(base, ApprovalPolicy{Mode: "threshold"})
	if inherit != base {
		t.Fatalf("threshold mode should inherit base subfields: %+v", inherit)
	}
	clean := MergeApprovalPolicy(base, ApprovalPolicy{Mode: "clean"})
	if clean != (ApprovalPolicy{Mode: "clean"}) {
		t.Fatalf("clean mode should drop threshold subfields: %+v", clean)
	}
	off := MergeApprovalPolicy(base, ApprovalPolicy{Mode: "off"})
	if off != (ApprovalPolicy{Mode: "off"}) {
		t.Fatalf("off mode should drop approval subfields: %+v", off)
	}
	override := MergeApprovalPolicy(base, ApprovalPolicy{MaxPriority: "P3", Note: "on_findings"})
	if override != (ApprovalPolicy{Mode: "threshold", MaxPriority: "P3", Note: "on_findings"}) {
		t.Fatalf("subfields should override inherited threshold policy: %+v", override)
	}
}
