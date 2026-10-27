package cli

import (
	stdctx "context"
	"errors"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/vanducng/miu-cr/internal/config"
)

// whoamiCommand reports the cached OAuth identity by WHITELIST: only the
// non-secret fields {Provider, AccountID, ExpiresAt}. The record's four secret
// fields (AccessToken/RefreshToken/IDToken/APIKey) are never read into the
// envelope, so no token can leak via whoami.
func whoamiCommand(_ *options) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the cached OAuth identity (provider/account/expiry — never the token)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWhoami(cmd.Context(), cmd.OutOrStdout())
		},
	}
}

func runWhoami(ctx stdctx.Context, stdout io.Writer) error {
	st, err := openOAuthStore(ctx)
	if err != nil {
		return err
	}
	rec, ok, err := st.Load(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return writeSuccess(stdout, "whoami", "whoami", oauthStoreFields(st, map[string]any{
			"logged_in": false,
		}), map[string]any{"logged_in": false})
	}
	expiresAt := ""
	if !rec.ExpiresAt.IsZero() {
		expiresAt = rec.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return writeSuccess(stdout, "whoami", "whoami", oauthStoreFields(st, map[string]any{
		"logged_in":  true,
		"provider":   rec.Provider,
		"account_id": rec.AccountID,
		"expires_at": expiresAt,
		"expired":    rec.Expired(time.Now()),
	}), map[string]any{"provider": rec.Provider})
}

// logoutCommand deletes the cached OAuth record. Idempotent: a missing record
// reports already-logged-out rather than erroring.
func logoutCommand(_ *options) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Delete the cached OAuth record",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogout(cmd.Context(), cmd.OutOrStdout())
		},
	}
}

func runLogout(ctx stdctx.Context, stdout io.Writer) error {
	st, err := openOAuthStore(ctx)
	if err != nil {
		return err
	}
	removed, err := st.Delete(ctx)
	if err != nil {
		var ce *CLIError
		if errors.As(err, &ce) {
			return err
		}
		return &CLIError{Code: "logout.remove_failed", Message: config.RedactString(err.Error()), Exit: 1}
	}
	return writeSuccess(stdout, "logout", "logout", oauthStoreFields(st, map[string]any{
		"removed": removed,
	}), map[string]any{"removed": removed})
}
