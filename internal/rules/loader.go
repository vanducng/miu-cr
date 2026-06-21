package rules

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	yaml "go.yaml.in/yaml/v4"
)

// ErrNotARule signals a file with no leading frontmatter fence: it is not a
// rule and must be skipped (callers may still report it as a body-only file).
var ErrNotARule = errors.New("not a rule: missing frontmatter fence")

// errSymlink is the platform-agnostic sentinel openNoFollow returns when the
// target is a symlink, so callers don't depend on a platform errno.
var errSymlink = errors.New("symlink not allowed")

const fence = "---"

// maxRuleFileBytes caps a single rule file so an attacker-authored repo rule
// can't OOM the process by being read whole into Rule.Body before the prompt
// token cap ever applies.
const maxRuleFileBytes = 64 * 1024

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
// rules (Untrusted, only when allowRepo). A later layer may override a stem only
// when it is at least as trusted as the rule already present: defaults and user
// (both Trusted) may override each other, but the Untrusted repo layer may add
// NEW stems only — it may NOT replace a stem provided by a Trusted layer (doing
// so would let an attacker `.miu/cr/rules/security.md` gut the baseline). A
// blocked override is dropped with a warning. Per-file parse errors are isolated
// as warnings and the file is skipped; a missing directory is not an error. The
// returned slice is sorted deterministically by stem.
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
			if existing, ok := byStem[r.Stem]; ok {
				if existing.Provenance.Trusted() {
					warnings = append(warnings, fmt.Sprintf("rules: ignore repo rule %s (stem %q already provided by trusted layer %s)", r.Path, r.Stem, existing.Provenance))
					continue
				}
				// Intra-layer collision: two repo rules resolve to the same stem
				// (e.g. security.md and security.MD on a case-insensitive FS); the
				// later one silently shadows the earlier — warn before overwriting.
				warnings = append(warnings, fmt.Sprintf("rules: duplicate stem %q in repo layer (%s shadows %s)", r.Stem, r.Path, existing.Path))
			}
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
		// openNoFollow refuses a symlinked .miu/cr/rules/link.md -> /etc/passwd
		// (atomically on unix), then we fstat the handle (not the path) for the
		// size cap and read from the same handle — no lstat-then-read TOCTOU.
		f, err := openNoFollow(path)
		if err != nil {
			if errors.Is(err, errSymlink) {
				warnings = append(warnings, fmt.Sprintf("rules: skip %s (symlink not allowed)", path))
			} else {
				warnings = append(warnings, fmt.Sprintf("rules: skip %s: %v", path, err))
			}
			continue
		}
		fi, err := f.Stat()
		if err != nil {
			f.Close()
			warnings = append(warnings, fmt.Sprintf("rules: skip %s: %v", path, err))
			continue
		}
		if fi.Size() > maxRuleFileBytes {
			f.Close()
			warnings = append(warnings, fmt.Sprintf("rules: skip %s (file too large: %d bytes)", path, fi.Size()))
			continue
		}
		data, err := io.ReadAll(f)
		f.Close()
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
		warnings = append(warnings, validateGlobs(r)...)
		rules = append(rules, r)
	}
	return rules, warnings
}

// validateGlobs reports malformed doublestar glob patterns as load-time
// warnings so a user sees why a rule never matches, rather than the selector
// silently failing to match at review time.
func validateGlobs(r Rule) (warnings []string) {
	for _, g := range r.FM.Globs {
		if g == "" {
			continue
		}
		if !doublestar.ValidatePattern(g) {
			warnings = append(warnings, fmt.Sprintf("rules: invalid glob %q in %s (will never match)", g, r.Path))
		}
	}
	return warnings
}
