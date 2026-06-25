package cli

import (
	"io"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"

	"github.com/vanducng/miu-cr/internal/config"
)

// configCommand groups read-only config inspection. The write path (get/set) is
// deliberately deferred — a plaintext-secret footgun.
func configCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect miu-cr configuration",
	}
	cmd.AddCommand(configShowCommand(opts))
	return cmd
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
