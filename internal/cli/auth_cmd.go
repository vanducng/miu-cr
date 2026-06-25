package cli

import (
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/vanducng/miu-cr/internal/config"
)

// whoamiCommand reports the cached OAuth identity by WHITELIST — only the
// non-secret fields {Provider, AccountID, ExpiresAt}. The record's four secret
// fields (AccessToken/RefreshToken/IDToken/APIKey) are never read into the
// envelope, so no token can leak via whoami.
func whoamiCommand(_ *options) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the cached OAuth identity (provider/account/expiry — never the token)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWhoami(cmd.OutOrStdout())
		},
	}
}

func runWhoami(stdout io.Writer) error {
	rec, ok, err := config.LoadOAuth()
	if err != nil {
		return err
	}
	if !ok {
		return writeSuccess(stdout, "whoami", "whoami", map[string]any{
			"logged_in": false,
		}, map[string]any{"logged_in": false})
	}
	expiresAt := ""
	if !rec.ExpiresAt.IsZero() {
		expiresAt = rec.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return writeSuccess(stdout, "whoami", "whoami", map[string]any{
		"logged_in":  true,
		"provider":   rec.Provider,
		"account_id": rec.AccountID,
		"expires_at": expiresAt,
		"expired":    rec.Expired(time.Now()),
	}, map[string]any{"provider": rec.Provider})
}

// logoutCommand deletes the cached OAuth record. Idempotent: a missing record
// reports already-logged-out rather than erroring.
func logoutCommand(_ *options) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Delete the cached OAuth record",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogout(cmd.OutOrStdout())
		},
	}
}

func runLogout(stdout io.Writer) error {
	path, err := config.OAuthPath()
	if err != nil {
		return err
	}
	removed := true
	if err := os.Remove(path); err != nil {
		if !os.IsNotExist(err) {
			return &CLIError{Code: "logout.remove_failed", Message: config.RedactString(err.Error()), Exit: 1}
		}
		removed = false
	}
	return writeSuccess(stdout, "logout", "logout", map[string]any{
		"removed": removed,
	}, map[string]any{"removed": removed})
}
