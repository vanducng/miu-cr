package github

import (
	"regexp"
	"regexp/syntax"
	"strings"
	"unicode"
)

// DefaultReRunComment is the comment the example Action workflow matches when
// no comment_trigger_regexes / MIUCR_COMMENT_TRIGGER_PATTERN is configured
// (its built-in default pattern is `(^|\s)/miucr review\b`).
const DefaultReRunComment = "/miucr review"

// maxReRunCommentLen caps a derived trigger so a pathological pattern can't
// bloat the summary.
const maxReRunCommentLen = 80

// ReRunCommentFromPatterns derives a literal PR comment that re-triggers the
// Action review from the configured comment-trigger regexes. Patterns are tried
// in order; the first one that yields a short, single-line example the pattern
// itself matches (case-insensitively, like the workflow) wins. It returns ""
// when no pattern yields one, so callers omit the instruction instead of
// printing a trigger the repo does not honor.
func ReRunCommentFromPatterns(patterns []string) string {
	for _, p := range patterns {
		if c := reRunCommentFromPattern(p); c != "" {
			return c
		}
	}
	return ""
}

func reRunCommentFromPattern(pattern string) string {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return ""
	}
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return ""
	}
	var b strings.Builder
	if !exampleFor(re, &b) {
		return ""
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if out == "" || len(out) > maxReRunCommentLen || !safeCodeSpan(out) {
		return ""
	}
	// Confirm the example really triggers: the workflow tests with the 'i' flag.
	check, err := regexp.Compile("(?i:" + pattern + ")")
	if err != nil || !check.MatchString(out) {
		return ""
	}
	return out
}

// exampleFor appends one shortest-ish string matched by re. It reports false
// for constructs with no sensible literal (any-char, word/class negations).
func exampleFor(re *syntax.Regexp, b *strings.Builder) bool {
	switch re.Op {
	case syntax.OpLiteral:
		lit := string(re.Rune)
		if re.Flags&syntax.FoldCase != 0 {
			// The parser stores case-folded literals in upper case; a lower-case
			// command reads more naturally and still matches.
			lit = strings.ToLower(lit)
		}
		b.WriteString(lit)
		return true
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine,
		syntax.OpBeginText, syntax.OpEndText, syntax.OpWordBoundary,
		syntax.OpQuest, syntax.OpStar:
		return true
	case syntax.OpCharClass:
		r, ok := classRune(re.Rune)
		if ok {
			b.WriteRune(r)
		}
		return ok
	case syntax.OpCapture:
		return exampleFor(re.Sub[0], b)
	case syntax.OpPlus:
		return exampleFor(re.Sub[0], b)
	case syntax.OpRepeat:
		for i := 0; i < re.Min; i++ {
			if !exampleFor(re.Sub[0], b) {
				return false
			}
		}
		return true
	case syntax.OpConcat:
		for _, s := range re.Sub {
			if !exampleFor(s, b) {
				return false
			}
		}
		return true
	case syntax.OpAlternate:
		// Prefer the first alternative that names a visible trigger; a leading
		// `(^|\s)` alternative contributes nothing either way.
		for _, s := range re.Sub {
			var alt strings.Builder
			if exampleFor(s, &alt) && strings.TrimSpace(alt.String()) != "" {
				b.WriteString(alt.String())
				return true
			}
		}
		return exampleFor(re.Sub[0], b)
	default:
		return false
	}
}

// classRune picks a representative rune from a char class's range pairs,
// preferring a space for whitespace classes.
func classRune(ranges []rune) (rune, bool) {
	for i := 0; i+1 < len(ranges); i += 2 {
		if ranges[i] <= ' ' && ' ' <= ranges[i+1] {
			return ' ', true
		}
	}
	for i := 0; i+1 < len(ranges); i += 2 {
		for r := ranges[i]; r <= ranges[i+1] && r-ranges[i] < 256; r++ {
			if unicode.IsPrint(r) {
				return r, true
			}
		}
	}
	return 0, false
}

// safeCodeSpan reports whether s can sit inside a single-backtick Markdown code
// span without breaking out of it.
func safeCodeSpan(s string) bool {
	for _, r := range s {
		if r == '`' || !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// reRunCommentInstruction renders the CI re-trigger hint for info, or "" when no
// trigger is known for this run.
func reRunCommentInstruction(info *PRInfo) string {
	if info == nil {
		return ""
	}
	c := strings.TrimSpace(info.ReRunComment)
	if c == "" || !safeCodeSpan(c) {
		return ""
	}
	return "comment `" + c + "` on this pull request"
}
