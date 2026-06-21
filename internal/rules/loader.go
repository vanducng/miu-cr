package rules

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	yaml "go.yaml.in/yaml/v4"
)

// ErrNotARule signals a file with no leading frontmatter fence: it is not a
// rule and must be skipped (callers may still report it as a body-only file).
var ErrNotARule = errors.New("not a rule: missing frontmatter fence")

const fence = "---"

// ParseRule splits the leading `---` fence by hand, unmarshals the YAML block,
// and returns the remainder as the body. A file without a fence returns
// ErrNotARule so the loader can skip it.
func ParseRule(path string, data []byte) (Rule, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")

	// First non-empty line must be the opening fence.
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	if start >= len(lines) || strings.TrimSpace(lines[start]) != fence {
		return Rule{}, ErrNotARule
	}

	closeAt := -1
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == fence {
			closeAt = i
			break
		}
	}
	if closeAt == -1 {
		return Rule{}, fmt.Errorf("unterminated frontmatter fence")
	}

	block := strings.Join(lines[start+1:closeAt], "\n")
	body := strings.TrimSpace(strings.Join(lines[closeAt+1:], "\n"))

	var fm Frontmatter
	if err := yaml.Unmarshal([]byte(block), &fm); err != nil {
		return Rule{}, fmt.Errorf("parse frontmatter: %w", err)
	}

	return Rule{
		Path: path,
		Stem: stemOf(path),
		FM:   fm,
		Body: body,
	}, nil
}

func stemOf(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// LoadRules layers embedded defaults (Trusted), user rules (Trusted), and repo
// rules (Untrusted, only when allowRepo). Later layers override earlier ones by
// stem (repo > user > defaults). Per-file parse errors are isolated as warnings
// and the file is skipped; a missing directory is not an error. The returned
// slice is sorted deterministically by stem.
func LoadRules(userDir, repoDir string, allowRepo bool) (rules []Rule, warnings []string) {
	byStem := map[string]Rule{}

	defaults, dw := loadDefaults()
	warnings = append(warnings, dw...)
	for _, r := range defaults {
		byStem[r.Stem] = r
	}

	userRules, uw := loadDir(userDir, UserTrusted)
	warnings = append(warnings, uw...)
	for _, r := range userRules {
		byStem[r.Stem] = r
	}

	if allowRepo {
		repoRules, rw := loadDir(repoDir, RepoUntrusted)
		warnings = append(warnings, rw...)
		for _, r := range repoRules {
			byStem[r.Stem] = r
		}
	}

	rules = make([]Rule, 0, len(byStem))
	for _, r := range byStem {
		rules = append(rules, r)
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].Stem < rules[j].Stem })
	return rules, warnings
}

func loadDir(dir string, prov Provenance) (rules []Rule, warnings []string) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []string{fmt.Sprintf("rules: read dir %s: %v", dir, err)}
	}
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("rules: read %s: %v", path, err))
			continue
		}
		r, err := ParseRule(path, data)
		if err != nil {
			if errors.Is(err, ErrNotARule) {
				warnings = append(warnings, fmt.Sprintf("rules: skip %s (no frontmatter fence)", path))
				continue
			}
			warnings = append(warnings, fmt.Sprintf("rules: skip %s: %v", path, err))
			continue
		}
		r.Provenance = prov
		rules = append(rules, r)
	}
	return rules, warnings
}
