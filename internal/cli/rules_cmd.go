package cli

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/rules"
)

// rulesCommand groups the rule scaffolding (`init`) and inspection (`check`)
// subcommands. `check` calls the same rules.SelectRules entry point the live
// review uses, so it never lies about what applies.
func rulesCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rules",
		Short: "Scaffold and inspect miucr project rules",
	}
	cmd.AddCommand(rulesInitCommand(opts))
	cmd.AddCommand(rulesCheckCommand(opts))
	return cmd
}

const exampleRulePath = ".miu/cr/rules/example.md"

func rulesInitCommand(_ *options) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Scaffold an annotated .miu/cr/rules/example.md",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := filepath.Dir(exampleRulePath)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return &CLIError{Code: "rules.init_mkdir", Message: "create rules dir: " + err.Error(), Hint: "check write permissions for " + dir, Exit: 1}
			}
			if _, err := os.Stat(exampleRulePath); err == nil && !force {
				return &CLIError{
					Code:    "rules.init_exists",
					Message: exampleRulePath + " already exists",
					Hint:    "pass --force to overwrite",
					Exit:    2,
					Details: map[string]any{"path": exampleRulePath},
				}
			}
			if err := os.WriteFile(exampleRulePath, []byte(rules.RuleTemplate()), 0o644); err != nil {
				return &CLIError{Code: "rules.init_write", Message: "write template: " + err.Error(), Hint: "check write permissions for " + exampleRulePath, Exit: 1}
			}
			return writeSuccess(cmd.OutOrStdout(), "rules init", "rules.init",
				map[string]any{"path": exampleRulePath, "forced": force},
				map[string]any{"path": exampleRulePath})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite an existing example.md")
	return cmd
}

func rulesCheckCommand(_ *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check <path>",
		Short: "Report which loaded rules apply to a changed-file path",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			changed := filepath.ToSlash(strings.TrimSpace(args[0]))
			if changed == "" {
				return &CLIError{Code: "rules.check_path_required", Message: "a non-empty path is required", Hint: "miucr rules check <path>", Exit: 2}
			}

			// Mirror engineReviewer: a local `rules check` always allows the
			// repo layer (the fork-PR demotion only applies to --pr reviews).
			const includeRepoRules = true
			loaded, warnings := rules.LoadRules(config.RulesDir(), filepath.Join(".miu", "cr", "rules"), includeRepoRules)
			selected := rules.SelectRules(loaded, []string{changed})

			applicable := make([]map[string]any, 0, len(selected))
			for _, r := range selected {
				applicable = append(applicable, map[string]any{
					"stem":         r.Stem,
					"description":  r.FM.Description,
					"provenance":   r.Provenance.String(),
					"trusted":      r.Provenance.Trusted(),
					"always_apply": r.FM.AlwaysApply,
					"globs":        r.FM.Globs,
					"path":         r.Path,
				})
			}

			bodyOnly := bodyOnlyWarnings(warnings)
			data := map[string]any{
				"path":         changed,
				"applicable":   applicable,
				"loaded_count": len(loaded),
				"body_only":    bodyOnly,
			}
			summary := map[string]any{
				"path":            changed,
				"applicable":      len(applicable),
				"loaded":          len(loaded),
				"body_only_files": len(bodyOnly),
			}
			return writeSuccess(cmd.OutOrStdout(), "rules check", "rules.check", data, summary)
		},
	}
	return cmd
}

const noFenceMarker = "(no frontmatter fence)"

// bodyOnlyWarnings surfaces fence-less files the loader skipped, loudly, so a
// stray .md author sees why their file is not a rule.
func bodyOnlyWarnings(warnings []string) []string {
	out := make([]string, 0, len(warnings))
	for _, w := range warnings {
		if strings.Contains(w, noFenceMarker) {
			out = append(out, w)
		}
	}
	return out
}
