package wire

import (
	"log/slog"
	"strings"

	mgithub "github.com/vanducng/miu-cr/internal/github"
)

// reRunComment resolves the literal PR comment shown as the "Re-run in CI" hint.
// Comment triggers only exist in the Action workflow (`miucr serve` does not
// read comment_trigger_regexes), so outside GitHub Actions it returns "".
// Precedence mirrors the workflow gate: MIUCR_COMMENT_TRIGGER_PATTERN, then
// review.pr_filter.comment_trigger_regexes, then the built-in `/miucr review`.
// A configured pattern with no literal example yields "" rather than the default,
// so a repo that overrode the trigger never sees instructions it does not honor.
func reRunComment(inActions bool, envPattern string, configured []string) string {
	if !inActions {
		return ""
	}
	source := "default"
	var patterns []string
	if p := strings.TrimSpace(envPattern); p != "" {
		source, patterns = "env", []string{p}
	} else if len(configured) > 0 {
		source, patterns = "config", configured
	}
	if patterns == nil {
		return mgithub.DefaultReRunComment
	}
	c := mgithub.ReRunCommentFromPatterns(patterns)
	if c == "" {
		slog.Debug("re-run comment hint omitted",
			"reason_code", "trigger_pattern_not_literal",
			"source", source,
			"patterns", len(patterns),
			"reason", "configured comment trigger pattern yields no literal example")
	}
	return c
}
