package rules

import (
	"embed"
	"fmt"
	"sort"
)

//go:embed defaults/*.md
var defaultsFS embed.FS

// loadDefaults parses the embedded baseline ruleset. Every parsed default is
// tagged BuiltinDefault (Trusted). A malformed embedded file is a build-time
// authoring bug, so it surfaces as a warning but never aborts.
func loadDefaults() (rules []Rule, warnings []string) {
	entries, err := defaultsFS.ReadDir("defaults")
	if err != nil {
		return nil, []string{fmt.Sprintf("rules: read embedded defaults: %v", err)}
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := "defaults/" + e.Name()
		data, err := defaultsFS.ReadFile(path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("rules: read embedded %s: %v", path, err))
			continue
		}
		r, err := ParseRule(path, data)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("rules: skip embedded %s: %v", path, err))
			continue
		}
		r.Provenance = BuiltinDefault
		rules = append(rules, r)
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].Stem < rules[j].Stem })
	return rules, warnings
}
