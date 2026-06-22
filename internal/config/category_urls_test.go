package config

import "testing"

func TestCategoryURLMapValidatesAndLowercases(t *testing.T) {
	r := Review{CategoryURLs: map[string]string{
		"Security": "https://docs.example.com/security",
		" Bug ":    "http://x/bug",
		"xss":      "javascript:alert(1)", // dropped: bad scheme
		"leak":     "//evil.example",      // dropped: scheme-relative
		"file":     "file:///etc/passwd",  // dropped: file scheme
		"empty":    "",                    // dropped: empty
		"":         "https://x/skip",      // dropped: empty key
		"no-host":  "https://",            // dropped: no host
	}}
	m := r.CategoryURLMap()
	if got := m["security"]; got != "https://docs.example.com/security" {
		t.Fatalf("security: lowercased key + kept url, got %q", got)
	}
	if got := m["bug"]; got != "http://x/bug" {
		t.Fatalf("bug: trimmed key + http kept, got %q", got)
	}
	for _, bad := range []string{"xss", "leak", "file", "empty", "no-host", ""} {
		if _, ok := m[bad]; ok {
			t.Fatalf("invalid entry %q must be dropped", bad)
		}
	}
	if len(m) != 2 {
		t.Fatalf("want exactly 2 valid entries, got %d: %v", len(m), m)
	}
}

func TestCategoryURLMapNilWhenNoneConfigured(t *testing.T) {
	if (Review{}).CategoryURLMap() != nil {
		t.Fatal("empty config must yield nil map (default render unchanged)")
	}
	if (Review{CategoryURLs: map[string]string{"a": "javascript:1"}}).CategoryURLMap() != nil {
		t.Fatal("all-invalid config must yield nil map")
	}
}

func TestCategoryURLLengthCap(t *testing.T) {
	long := "https://x/"
	for len(long) <= maxCategoryURLLen {
		long += "aaaaaaaaaa"
	}
	if (Review{CategoryURLs: map[string]string{"x": long}}).CategoryURLMap() != nil {
		t.Fatal("over-cap URL must be dropped")
	}
}

func TestValidCategoryURLRejectsMarkdownBreakers(t *testing.T) {
	// Parens are legal in a URL and handled by the [text](<url>) render form.
	if !validCategoryURL("https://en.wikipedia.org/wiki/SQL_(lang)") {
		t.Fatal("a URL with parens must be kept (the <url> render form tolerates them)")
	}
	bad := map[string]string{
		"space":     "https://x/a b",
		"tab":       "https://x/a\tb",
		"newline":   "https://x/a\nb",
		"angle-lt":  "https://x/a<b",
		"angle-gt":  "https://x/a>b",
		"backslash": "https://x/a\\b",
		"control":   "https://x/a\x01b",
	}
	for name, u := range bad {
		if validCategoryURL(u) {
			t.Fatalf("%s: a markdown-breaking char must be rejected: %q", name, u)
		}
	}
}

func TestMergeReviewOverlay(t *testing.T) {
	base := Defaults()
	file := Config{Review: Review{CategoryURLs: map[string]string{"security": "https://x/s"}}}
	out := Merge(base, file)
	if out.Review.CategoryURLs["security"] != "https://x/s" {
		t.Fatalf("file [review] must overlay base, got %v", out.Review.CategoryURLs)
	}
}
