// Package rules loads miucr markdown rule files (YAML frontmatter + prose body)
// from three trust layers and selects them against changed files. Rules are
// review CONTEXT only; they never gate findings or exit codes.
package rules

// Provenance tags where a rule came from and whether it is trusted.
type Provenance int

const (
	BuiltinDefault Provenance = iota // embedded baseline ruleset (Trusted)
	UserTrusted                      // ~/.config/miu/cr/rules (Trusted)
	RepoUntrusted                    // .miucr/rules (Untrusted; attacker-authored on fork PRs)
)

func (p Provenance) String() string {
	switch p {
	case BuiltinDefault:
		return "builtin-default"
	case UserTrusted:
		return "user"
	case RepoUntrusted:
		return "repo"
	default:
		return "unknown"
	}
}

// Trusted reports whether the rule's source is trusted (defaults + user).
func (p Provenance) Trusted() bool { return p != RepoUntrusted }

// Frontmatter is the YAML block at the head of a rule file. Trimmed to v1 keys.
type Frontmatter struct {
	Description  string   `yaml:"description"`
	Globs        []string `yaml:"globs"`
	AlwaysApply  bool     `yaml:"alwaysApply"`
	ContextFiles []string `yaml:"context_files"`
}

// Rule is a parsed rule file: selector frontmatter + prose body + provenance.
type Rule struct {
	Path       string
	Stem       string
	FM         Frontmatter
	Body       string
	Provenance Provenance
}
