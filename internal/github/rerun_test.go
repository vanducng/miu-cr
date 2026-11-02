package github

import (
	"strings"
	"testing"
)

func TestReRunCommentFromPatterns(t *testing.T) {
	for _, tt := range []struct {
		name     string
		patterns []string
		want     string
	}{
		{"example default", []string{`(^|\s)/miucr review\b`}, "/miucr review"},
		{"slash or mention picks first", []string{`(^|\s)(/miucr review\b|@review-bot\b)`}, "/miucr review"},
		{"mention with whitespace class", []string{`(^|\s)@review-bot\s+review\b`}, "@review-bot review"},
		{"case-insensitive flag", []string{`(?i)/Bot\s+check`}, "/bot check"},
		{"char class literal", []string{`/[Rr]erun`}, "/rerun"},
		{"any-char skipped for next", []string{`.*`, `/review-now`}, "/review-now"},
		{"no literal", []string{`.+`}, ""},
		{"invalid regex", []string{`(`}, ""},
		{"anchors only", []string{`^$`}, ""},
		{"backtick rejected", []string{"/run`x"}, ""},
		{"empty", nil, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ReRunCommentFromPatterns(tt.patterns); got != tt.want {
				t.Fatalf("ReRunCommentFromPatterns(%q) = %q, want %q", tt.patterns, got, tt.want)
			}
		})
	}
}

func TestRenderSummaryFullCIReRunHint(t *testing.T) {
	info, diffs, _ := presentationFixture()
	info.ReRunComment = "/miucr review"
	out := RenderSummaryFull(info, nil, nil, 0, nil, nil, SummaryOptions{Diffs: diffs})
	ci := "- Re-run in CI: comment `/miucr review` on this pull request\n"
	local := "- Run locally: `miucr review --pr https://github.com/o/r/pull/7 -o pretty`"
	ciAt, localAt := strings.Index(out, ci), strings.Index(out, local)
	if ciAt < 0 || localAt < 0 {
		t.Fatalf("want both CI and local re-run lines:\n%s", out)
	}
	if ciAt > localAt {
		t.Fatalf("CI comment hint should come before the local command:\n%s", out)
	}
}

func TestRenderSummaryFullNoCIReRunHintWithoutTrigger(t *testing.T) {
	info, diffs, _ := presentationFixture()
	out := RenderSummaryFull(info, nil, nil, 0, nil, nil, SummaryOptions{Diffs: diffs})
	if strings.Contains(out, "Re-run in CI") {
		t.Fatalf("no trigger known: CI hint must be omitted:\n%s", out)
	}
	if !strings.Contains(out, "Run locally: `miucr review --pr") {
		t.Fatalf("local command must stay:\n%s", out)
	}
}

func TestRenderErrorNoticeCIReRunHint(t *testing.T) {
	for _, level := range []string{"warning", "caution"} {
		t.Run(level, func(t *testing.T) {
			info := &PRInfo{Owner: "o", Repo: "r", Number: 3, ReviewCount: 1, ReRunComment: "@review-bot review"}
			out := RenderErrorNotice(info, ErrorNotice{Level: level, Message: "boom"}, "v1.0.0")
			want := "To re-run the review, comment `@review-bot review` on this pull request."
			if !strings.Contains(out, want) {
				t.Fatalf("want %q in error notice:\n%s", want, out)
			}
			if strings.Index(out, want) > strings.Index(out, "<sub>miucr v1.0.0</sub>") {
				t.Fatalf("re-run hint must precede the version footer:\n%s", out)
			}

			info.ReRunComment = ""
			if out := RenderErrorNotice(info, ErrorNotice{Level: level}, ""); strings.Contains(out, "To re-run the review") {
				t.Fatalf("no trigger known: hint must be omitted:\n%s", out)
			}
		})
	}
}

func TestReRunCommentInstructionRejectsUnsafe(t *testing.T) {
	if got := reRunCommentInstruction(&PRInfo{ReRunComment: "x` <b>"}); got != "" {
		t.Fatalf("backtick must not break the code span, got %q", got)
	}
	if got := reRunCommentInstruction(&PRInfo{ReRunComment: "a\nb"}); got != "" {
		t.Fatalf("newline must be rejected, got %q", got)
	}
}
