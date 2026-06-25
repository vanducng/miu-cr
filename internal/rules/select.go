package rules

import (
	"sort"

	"github.com/bmatcuk/doublestar/v4"
)

// SelectRules returns the rules that apply to the given changed paths: a rule
// applies if AlwaysApply is true OR any of its globs doublestar-matches any
// changed path. A rule with no globs and AlwaysApply==false is never
// auto-selected. changedPaths must be forward-slash relative paths.
//
// Output order is deterministic and doubles as the truncation order: applicable
// rules first by descending priority (alwaysApply, then trust), then
// alphabetically by stem, so the least-important rules sort last and get
// dropped first under a token cap.
func SelectRules(rules []Rule, changedPaths []string) []Rule {
	out := make([]Rule, 0, len(rules))
	for _, r := range rules {
		if r.applies(changedPaths) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.FM.AlwaysApply != b.FM.AlwaysApply {
			return a.FM.AlwaysApply
		}
		if a.Provenance.Trusted() != b.Provenance.Trusted() {
			return a.Provenance.Trusted()
		}
		return a.Stem < b.Stem
	})
	return out
}

func (r Rule) applies(changedPaths []string) bool {
	if r.FM.AlwaysApply {
		return true
	}
	for _, g := range r.FM.Globs {
		if g == "" {
			continue
		}
		for _, p := range changedPaths {
			// validateGlobs (loader.go) rejects malformed patterns at load time, so
			// a match error here is unexpected; treat it as a non-match rather than
			// matching spuriously.
			if ok, err := doublestar.Match(g, p); err == nil && ok {
				return true
			}
		}
	}
	return false
}
