package github

import (
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine"
)

func TestCommentBodyCategoryLinkWhenMapped(t *testing.T) {
	f := engine.Finding{Severity: "high", Category: "Security", Rationale: "x"}
	urls := map[string]string{"security": "https://docs.example/sec"}

	linked, _ := commentBody(f, "", PostReviewOptions{CategoryURLs: urls}, false)
	if !strings.Contains(linked, "**HIGH** ([Security](https://docs.example/sec))") {
		t.Fatalf("mapped category must render as a link:\n%s", linked)
	}

	plain, _ := commentBody(f, "", PostReviewOptions{}, false)
	if !strings.Contains(plain, "**HIGH** (Security)") || strings.Contains(plain, "](http") {
		t.Fatalf("unmapped category must render plain (byte-for-byte today):\n%s", plain)
	}
}

func TestSummaryOverflowCategoryLink(t *testing.T) {
	info := &PRInfo{HeadSHA: "h"}
	omitted := []engine.Finding{{File: "a.go", Line: 4, Severity: "high", Category: "Security", Rationale: "x"}}
	urls := map[string]string{"security": "https://docs.example/sec"}

	linked := RenderSummaryWithOverflow(info, nil, nil, 1, omitted, urls)
	if !strings.Contains(linked, "([Security](https://docs.example/sec))") {
		t.Fatalf("summary overflow mapped category must link:\n%s", linked)
	}

	plain := RenderSummaryWithOverflow(info, nil, nil, 1, omitted, nil)
	if !strings.Contains(plain, "(Security)") || strings.Contains(plain, "](http") {
		t.Fatalf("summary overflow unmapped category must render plain:\n%s", plain)
	}
}

// Annotations stay plain text — no markdown link in the title.
func TestChecksAnnotationCategoryPlain(t *testing.T) {
	a := annotationFor(engine.Finding{File: "a.go", Line: 4, Severity: "high", Category: "Security", Rationale: "x"})
	if a.GetTitle() != "HIGH (Security)" {
		t.Fatalf("annotation title must stay plain, got %q", a.GetTitle())
	}
}
