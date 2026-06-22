package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/vanducng/miu-cr/internal/config"
)

// initCommand walks provider -> API-key source -> project rules -> config.Save,
// ending on the literal `miucr review --staged`. Hand-rolled bufio prompts (no
// TUI dep). Default writes only an env-var NAME; a literal auth_token is written
// only on explicit paste-now + confirm. --non-interactive drives it from flags
// with zero prompts (CI bootstrap). Registered first in commands.go so it tops
// --help.
func initCommand(_ *options) *cobra.Command {
	var (
		nonInteractive bool
		force          bool
		yes            bool
		noRules        bool
		flagProvider   string
		flagAuthEnv    string
		flagBaseURL    string
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up a provider, scaffold rules, and write ~/.config/miu/cr/config.toml",
		RunE: func(cmd *cobra.Command, args []string) error {
			in := bufio.NewScanner(cmd.InOrStdin())
			out := cmd.ErrOrStderr() // prompts/payoff on stderr; envelope on stdout
			ask := func(prompt, def string) string { return askLine(in, out, prompt, def) }

			path, err := config.FilePath()
			if err != nil {
				return &CLIError{Code: "config.write_failed", Message: err.Error(), Exit: 1}
			}

			if _, statErr := os.Stat(path); statErr == nil && !force {
				if nonInteractive && !yes {
					return &CLIError{Code: "init.aborted", Message: path + " already exists", Hint: "pass --force or --yes to overwrite", Exit: 2}
				}
				if !yes {
					if cur, lerr := config.Load(); lerr == nil {
						fmt.Fprintf(out, "Config exists at %s (provider: %s)\n", path, cur.DefaultProvider)
					}
					if !confirm(ask, "Overwrite?", false) {
						return &CLIError{Code: "init.aborted", Message: "init aborted: config exists", Hint: "re-run with --force to overwrite", Exit: 2}
					}
				}
			}

			provider, prov, err := chooseProvider(ask, nonInteractive, flagProvider, flagBaseURL)
			if err != nil {
				return err
			}
			if err := chooseAuth(ask, out, nonInteractive, yes, provider, flagAuthEnv, &prov); err != nil {
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

			printPayoff(out, path, provider, prov, rulePath)
			return writeSuccess(cmd.OutOrStdout(), "init", "init.result",
				map[string]any{
					"config_path":      path,
					"default_provider": provider,
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
	f.StringVar(&flagProvider, "provider", "", "Provider: anthropic | openai (non-interactive)")
	f.StringVar(&flagAuthEnv, "auth-env", "", "Name of the env var holding the API key (non-interactive)")
	f.StringVar(&flagBaseURL, "base-url", "", "Override the provider base URL (custom gateway)")
	return cmd
}

// chooseProvider resolves the provider name + a profile seeded from Defaults.
// Interactive: numbered select (anthropic default | openai | custom). Custom asks
// base_url; auth handled in chooseAuth. Non-interactive reads flagProvider.
func chooseProvider(ask func(string, string) string, nonInteractive bool, flagProvider, flagBaseURL string) (string, config.Provider, error) {
	base := config.Defaults()
	name := strings.TrimSpace(flagProvider)
	if !nonInteractive {
		name = ask("Provider [1] anthropic  [2] openai  [3] custom", "anthropic")
		switch name {
		case "1", "":
			name = "anthropic"
		case "2":
			name = "openai"
		case "3":
			name = "custom"
		}
	}
	if name == "" {
		name = "anthropic"
	}

	if prof, ok := base.Providers[name]; ok {
		if flagBaseURL != "" {
			prof.BaseURL = flagBaseURL
		}
		return name, prof, nil
	}
	if name != "custom" && nonInteractive {
		return "", config.Provider{}, &CLIError{Code: "init.aborted", Message: "unknown --provider " + name, Hint: "use anthropic or openai", Exit: 2}
	}
	// custom: a profile of an existing kind with a user base_url.
	prof := config.Provider{Kind: config.KindAnthropic, Model: config.DefaultAnthropicModel}
	if k := strings.ToLower(strings.TrimSpace(ask("Kind [1] anthropic  [2] openai", "anthropic"))); k == "2" || k == "openai" {
		prof = config.Provider{Kind: config.KindOpenAI, Model: config.DefaultOpenAIModel}
	}
	bu := flagBaseURL
	if bu == "" {
		bu = strings.TrimSpace(ask("Base URL (gateway endpoint)", ""))
	}
	prof.BaseURL = bu
	pname := strings.TrimSpace(ask("Profile name", "custom"))
	if pname == "" {
		pname = "custom"
	}
	return pname, prof, nil
}

// chooseAuth records how the credential is supplied. Default (and recommended)
// is an env-var NAME — no secret on disk. Paste-now writes a literal auth_token
// only after an explicit confirm and the plaintext-on-disk warning.
func chooseAuth(ask func(string, string) string, out io.Writer, nonInteractive, yes bool, provider, flagAuthEnv string, prof *config.Provider) error {
	defEnv := defaultAuthEnv(provider, *prof)
	if nonInteractive {
		env := strings.TrimSpace(flagAuthEnv)
		if env == "" {
			env = defEnv
		}
		prof.AuthEnv = env
		return nil
	}

	choice := ask("API key source [1] env var (recommended)  [2] paste now", "1")
	if choice == "2" || strings.EqualFold(choice, "paste") {
		fmt.Fprintln(out, "miu-cr: note: the key will be visible as you type (no terminal masking)")
		token := strings.TrimSpace(ask("Paste API key", ""))
		if token == "" {
			return &CLIError{Code: "init.aborted", Message: "no key pasted", Hint: "re-run and choose env var, or paste a key", Exit: 2}
		}
		fmt.Fprintln(out, plaintextWarning)
		if !confirm(ask, "Write the key to "+config.FilePathOrEmpty()+" in plaintext?", false) {
			return &CLIError{Code: "init.aborted", Message: "paste-now declined", Hint: "choose env var instead", Exit: 2}
		}
		prof.AuthToken = token
		return nil
	}

	env := strings.TrimSpace(ask("Env var name", defEnv))
	if env == "" {
		env = defEnv
	}
	prof.AuthEnv = env
	return nil
}

// defaultAuthEnv is the conventional key env var for a provider kind.
func defaultAuthEnv(provider string, prof config.Provider) string {
	if prof.Kind == config.KindOpenAI || provider == "openai" {
		return "OPENAI_API_KEY"
	}
	return "ANTHROPIC_API_KEY"
}

const plaintextWarning = "miu-cr: warning: provider auth_token is stored in plaintext on disk; prefer auth_env (the NAME of an env var holding the token)"
