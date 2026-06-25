package cli

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"

	"github.com/vanducng/miu-cr/internal/config"
)

// configCommand groups config inspection (show) and a single-key writer (set). The
// writer refuses secret-bearing keys: tokens and DSNs are read from env at runtime,
// never persisted, so set only touches non-secret config.
func configCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and update miu-cr configuration",
	}
	cmd.AddCommand(configShowCommand(opts))
	cmd.AddCommand(configSetCommand(opts))
	cmd.AddCommand(configEditCommand(opts))
	return cmd
}

// configEditCommand opens the config file in $VISUAL/$EDITOR (falling back to vi) for
// free-form edits, then reloads it so a syntax/enum error surfaces immediately. It
// requires an interactive terminal; in CI use `config set`.
func configEditCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "edit",
		Short: "Open the config file in $VISUAL/$EDITOR (interactive)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if fi, _ := os.Stdin.Stat(); fi == nil || fi.Mode()&os.ModeCharDevice == 0 {
				return &CLIError{Code: "config.no_tty", Message: "config edit needs an interactive terminal", Hint: "use `miucr config set <key> <value>` in non-interactive contexts", Exit: 2}
			}
			path, err := config.FilePath()
			if err != nil {
				return &CLIError{Code: "config.unavailable", Message: "resolve config path: " + config.RedactString(err.Error()), Exit: 1}
			}
			if err := ensureConfigFile(path); err != nil {
				return err
			}
			editor := firstNonEmpty(os.Getenv("VISUAL"), os.Getenv("EDITOR"), "vi")
			// Run via the shell so a multi-word $EDITOR ("code -w") and a path with
			// spaces both work; the path is passed as $1, never interpolated into the
			// command string (no quoting/injection footgun, no strings.Fields panic).
			ed := exec.Command("sh", "-c", editor+` "$1"`, "sh", path) //nolint:gosec // user's own $EDITOR
			ed.Stdin, ed.Stdout, ed.Stderr = os.Stdin, os.Stderr, os.Stderr
			if err := ed.Run(); err != nil {
				return &CLIError{Code: "config.edit_failed", Message: "editor exited with error: " + config.RedactString(err.Error()), Hint: "check your $EDITOR/$VISUAL", Exit: 1}
			}
			// Reload to validate; report (do not block) if the edited file is invalid.
			valid := true
			var loadErr string
			if _, lerr := config.Load(); lerr != nil {
				valid = false
				loadErr = config.RedactString(lerr.Error())
			}
			return writeSuccess(cmd.OutOrStdout(), "config edit", "config.edit",
				map[string]any{"path": path, "valid": valid, "load_error": loadErr},
				map[string]any{"path": path, "valid": valid})
		},
	}
}

// ensureConfigFile creates the config dir and an empty file if absent, so the editor
// always opens something.
func ensureConfigFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil { // 0o700: matches Save (the dir also holds oauth.json)
		return &CLIError{Code: "config.write_failed", Message: "create config dir: " + config.RedactString(err.Error()), Exit: 1}
	}
	if err := os.WriteFile(path, []byte("# miu-cr config. See config.example.toml for all keys.\n"), 0o600); err != nil {
		return &CLIError{Code: "config.write_failed", Message: "create config file: " + config.RedactString(err.Error()), Exit: 1}
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// configSetCommand sets ONE dotted, non-secret key (e.g. default_provider, review.gate,
// providers.zai.model) and merges it into the existing config file, so the user updates
// config without re-running init. Secret keys (auth_token, store.dsn) are rejected.
func configSetCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set one non-secret config key, merged into the existing config",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value := args[0], args[1]
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if err := config.SetKey(&cfg, key, value); err != nil {
				return err
			}
			if err := config.Save(cfg); err != nil {
				return err
			}
			path := config.FilePathOrEmpty()
			return writeSuccess(cmd.OutOrStdout(), "config set", "config.set",
				map[string]any{"key": key, "path": path},
				map[string]any{"key": key})
		},
	}
}

// configShowCommand prints the EFFECTIVE configuration with every token/DSN
// masked by config.RedactConfig. Default shows only the user-set deltas (what
// `init` would write); --all includes the built-in defaults save.go strips. The
// redaction is STRUCTURAL: no token/DSN can ever reach stdout.
func configShowCommand(opts *options) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print the effective configuration (secrets redacted)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			safe := config.RedactConfig(cfg)
			view := configView(safe, all)
			data, err := configData(view)
			if err != nil {
				return err
			}
			summary := map[string]any{
				"all":  all,
				"path": config.FilePathOrEmpty(),
			}
			if prettyOutput {
				return renderConfigPretty(cmd.OutOrStdout(), view)
			}
			return writeSuccess(cmd.OutOrStdout(), "config show", "config.show", data, summary)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Include built-in defaults (the full effective config), not just user-set values")
	return cmd
}

// configView returns the config to render: the full effective config under --all,
// else only the non-default deltas (the user-set values `init` persists).
func configView(safe config.Config, all bool) any {
	if all {
		return safe
	}
	return config.Delta(safe)
}

// configData renders the (already-redacted) view to a generic JSON-friendly map
// via TOML round-trip, so the envelope uses the documented snake_case keys rather
// than the struct's Go field names (the struct carries only toml tags).
func configData(view any) (map[string]any, error) {
	raw, err := toml.Marshal(view)
	if err != nil {
		return nil, &CLIError{Code: "config.render_failed", Message: "render config: " + config.RedactString(err.Error()), Exit: 1}
	}
	out := map[string]any{}
	if err := toml.Unmarshal(raw, &out); err != nil {
		return nil, &CLIError{Code: "config.render_failed", Message: "render config: " + config.RedactString(err.Error()), Exit: 1}
	}
	return out, nil
}

// renderConfigPretty prints the redacted view as TOML for human reading. Input is
// already redacted by RedactConfig, so no secret can appear here either.
func renderConfigPretty(w io.Writer, view any) error {
	raw, err := toml.Marshal(view)
	if err != nil {
		return &CLIError{Code: "config.render_failed", Message: "render config: " + config.RedactString(err.Error()), Exit: 1}
	}
	if _, err := w.Write(raw); err != nil {
		return &CLIError{Code: "config.write_failed", Message: "write config: " + config.RedactString(err.Error()), Exit: 1}
	}
	return nil
}
