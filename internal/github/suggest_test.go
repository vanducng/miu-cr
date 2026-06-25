package github

import (
	"testing"

	"github.com/vanducng/miu-cr/internal/engine"
)

func TestClassifyReplacement(t *testing.T) {
	const content = "line one\n  anchor here\nline three\nfour\nfive"
	tests := []struct {
		name       string
		f          engine.Finding
		content    string
		wantPatch  string
		wantReason repairReason
		wantRepair bool
	}{
		{
			name:       "clean single-line OK",
			f:          engine.Finding{Line: 2, QuotedCode: "anchor here", SuggestedPatch: "  fixed line"},
			content:    content,
			wantPatch:  "fixed line",
			wantReason: reasonOK,
		},
		{
			name:       "no anchor (line<=0)",
			f:          engine.Finding{Line: 0, SuggestedPatch: "x"},
			content:    content,
			wantReason: reasonNoAnchor,
		},
		{
			name:       "garbled span (EndLine<Line)",
			f:          engine.Finding{Line: 3, EndLine: 2, SuggestedPatch: "x"},
			content:    content,
			wantReason: reasonGarbledSpan,
			wantRepair: true,
		},
		{
			name:       "empty patch",
			f:          engine.Finding{Line: 2, QuotedCode: "anchor here", SuggestedPatch: "   "},
			content:    content,
			wantReason: reasonEmpty,
			wantRepair: true,
		},
		{
			name:       "out of range",
			f:          engine.Finding{Line: 99, QuotedCode: "anchor here", SuggestedPatch: "x"},
			content:    content,
			wantReason: reasonOutOfRange,
		},
		{
			name:       "anchor mismatch single",
			f:          engine.Finding{Line: 2, QuotedCode: "totally different", SuggestedPatch: "x"},
			content:    content,
			wantReason: reasonAnchorMismatch,
		},
		{
			name:       "no-op single",
			f:          engine.Finding{Line: 2, QuotedCode: "anchor here", SuggestedPatch: "anchor here"},
			content:    content,
			wantReason: reasonNoOp,
			wantRepair: true,
		},
		{
			name:       "clean multi-line OK",
			f:          engine.Finding{Line: 2, EndLine: 3, QuotedCode: "anchor here\nline three", SuggestedPatch: "new a\nnew b"},
			content:    content,
			wantPatch:  "new a\nnew b",
			wantReason: reasonOK,
		},
		{
			name:       "multi-line out of range",
			f:          engine.Finding{Line: 4, EndLine: 99, QuotedCode: "four\nfive", SuggestedPatch: "x\ny"},
			content:    content,
			wantReason: reasonOutOfRange,
		},
		{
			name:       "multi-line length mismatch",
			f:          engine.Finding{Line: 2, EndLine: 3, QuotedCode: "anchor here", SuggestedPatch: "x\ny"},
			content:    content,
			wantReason: reasonLengthMismatch,
			wantRepair: true,
		},
		{
			name:       "multi-line anchor mismatch",
			f:          engine.Finding{Line: 2, EndLine: 3, QuotedCode: "anchor here\nwrong", SuggestedPatch: "x\ny"},
			content:    content,
			wantReason: reasonAnchorMismatch,
		},
		{
			name:       "multi-line no-op",
			f:          engine.Finding{Line: 2, EndLine: 3, QuotedCode: "anchor here\nline three", SuggestedPatch: "anchor here\nline three"},
			content:    content,
			wantReason: reasonNoOp,
			wantRepair: true,
		},
		{
			name:       "multi-line empty patch",
			f:          engine.Finding{Line: 2, EndLine: 3, QuotedCode: "anchor here\nline three", SuggestedPatch: ""},
			content:    content,
			wantReason: reasonEmpty,
			wantRepair: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			patch, reason := classifyReplacement(tc.f, tc.content)
			if reason != tc.wantReason {
				t.Fatalf("reason = %v, want %v", reason, tc.wantReason)
			}
			if patch != tc.wantPatch {
				t.Fatalf("patch = %q, want %q", patch, tc.wantPatch)
			}
			if reason.repairable() != tc.wantRepair {
				t.Fatalf("repairable = %v, want %v", reason.repairable(), tc.wantRepair)
			}

			// Regression guard: isCleanReplacement is byte-identical to the
			// pre-refactor accept/reject + returned patch.
			wantOK := tc.wantReason == reasonOK
			gotPatch, gotOK := isCleanReplacement(tc.f, tc.content)
			if gotOK != wantOK {
				t.Fatalf("isCleanReplacement ok = %v, want %v", gotOK, wantOK)
			}
			if gotPatch != tc.wantPatch {
				t.Fatalf("isCleanReplacement patch = %q, want %q", gotPatch, tc.wantPatch)
			}

			// Exported seam mirrors the same verdict + stable string.
			xPatch, xReason, xRepair := ClassifyReplacement(tc.f, tc.content)
			if xPatch != tc.wantPatch || xReason != tc.wantReason.String() || xRepair != tc.wantRepair {
				t.Fatalf("ClassifyReplacement = (%q, %q, %v), want (%q, %q, %v)",
					xPatch, xReason, xRepair, tc.wantPatch, tc.wantReason.String(), tc.wantRepair)
			}
		})
	}
}

func TestRepairReasonString(t *testing.T) {
	for r, want := range map[repairReason]string{
		reasonOK:             "ok",
		reasonNoAnchor:       "no_anchor",
		reasonOutOfRange:     "out_of_range",
		reasonEmpty:          "empty",
		reasonNoOp:           "no_op",
		reasonAnchorMismatch: "anchor_mismatch",
		reasonGarbledSpan:    "garbled_span",
		reasonLengthMismatch: "length_mismatch",
	} {
		if got := r.String(); got != want {
			t.Errorf("repairReason(%d).String() = %q, want %q", r, got, want)
		}
	}
}
