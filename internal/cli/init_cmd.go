package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/vanducng/miu-cr/internal/config"
)

// initCommand walks a clean, sectioned setup: provider -> provider-aware auth
// (browser OAuth / env var / paste) -> project rules -> config.Save, ending on
// the literal `miucr review --staged`. Hand-rolled bufio prompts (no TUI dep).
// Default writes only an env-var NAME; a literal auth_token lands only on explicit
// paste + confirm; OAuth caches a token in oauth.json (never in config).
// --non-interactive drives it from flags with zero prompts (CI bootstrap).
// Registered first in commands.go so it tops --help.
func initCommand(_ *options) *cobra.Command {
	var (
		nonInteractive bool
		force          bool
		yes            bool
		noRules        bool
		flagProvider   string
		flagAuth       string
		flagAuthEnv    string
		flagBaseURL    string
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up a provider, scaffold rules, and write ~/.config/miu/cr/config.toml",
		RunE: func(cmd *cobra.Command, args []string) error {
			in := bufio.NewScanner(cmd.InOrStdin())
			out := cmd.ErrOrStderr() // prompts/payoff on stderr; envelope on stdout
			ask := func(prompt, def string) string { return askLine(cmd.Context(), in, out, prompt, def) }

			path, err := config.FilePath()
			if err != nil {
				return &CLIError{Code: "config.write_failed", Message: err.Error(), Exit: 1}
			}

			if !nonInteractive {
				fmt.Fprintln(out, "\n  miu-cr setup")
			}

			if _, statErr := os.Stat(path); statErr == nil && !force {
				if nonInteractive && !yes {
					return &CLIError{Code: "init.aborted", Message: path + " already exists", Hint: "pass --force or --yes to overwrite", Exit: 2}
				}
				if !yes {
					if cur, lerr := config.Load(); lerr == nil {
						fmt.Fprintf(out, "\n  Config exists at %s (provider: %s)\n", path, cur.DefaultProvider)
					}
					if !confirm(ask, "  Overwrite?", false) {
						return &CLIError{Code: "init.aborted", Message: "init aborted: config exists", Hint: "re-run with --force to overwrite", Exit: 2}
					}
				}
			}

			provider, prov, err := chooseProvider(ask, out, nonInteractive, flagProvider, flagBaseURL)
			if err != nil {
				return err
			}
			method, err := chooseAuth(cmd.Context(), ask, out, authInput{
				nonInteractive: nonInteractive,
				flagAuth:       flagAuth,
				flagAuthEnv:    flagAuthEnv,
			}, provider, &prov)
			if err != nil {
				return err
			}

			cfg := config.Defaults()
			cfg.DefaultProvider = provider
			cfg.Providers[provider] = prov

			if err := config.Save(cfg); err != nil {
				return &CLIError{Code: "config.write_failed", Message: err.Error(), Hint: "check write permissions for " + filepath.Dir(path), Exit: 1}
			}

			rulePath := ""
			if !noRules {
				rulePath, err = scaffoldDetectedRules(ask, nonInteractive, force)
				if err != nil {
					return err
				}
			}

			printPayoff(out, path, provider, prov, rulePath, method)
			return writeSuccess(cmd.OutOrStdout(), "init", "init.result",
				map[string]any{
					"config_path":      path,
					"default_provider": provider,
					"auth_method":      string(method),
					"auth_env":         prov.AuthEnv,
					"auth_inline":      prov.AuthToken != "",
					"rules_path":       rulePath,
					"next":             "miucr review --staged",
				},
				map[string]any{"provider": provider, "config_path": path, "rules_path": rulePath})
		},
	}
	f := cmd.Flags()
	f.BoolVar(&nonInteractive, "non-interactive", false, "Write config from flags with zero prompts (CI bootstrap)")
	f.BoolVar(&force, "force", false, "Overwrite an existing config without prompting")
	f.BoolVar(&yes, "yes", false, "Assume yes for all prompts")
	f.BoolVar(&noRules, "no-rules", false, "Skip scaffolding a project rules file")
	f.StringVar(&flagProvider, "provider", "", "Provider: anthropic | openai | custom (non-interactive)")
	f.StringVar(&flagAuth, "auth", "", "Auth method (non-interactive): oauth | env | paste")
	f.StringVar(&flagAuthEnv, "auth-env", "", "Name of the env var holding the API key (non-interactive)")
	f.StringVar(&flagBaseURL, "base-url", "", "Override the provider base URL (custom gateway)")
	return cmd
}
