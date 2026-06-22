package cli

import (
	stdctx "context"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/vanducng/miu-cr/internal/config"
)

// loginCallbackTimeout bounds the wait for the browser callback. cmd.Context() is
// context.Background() for cobra, so without this the flow could hang forever.
const loginCallbackTimeout = 3 * time.Minute

func loginCommand(_ *options) *cobra.Command {
	var (
		provider  string
		baseURL   string
		port      int
		noBrowser bool
	)
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Run an OAuth loopback flow and cache a token for reviews on your plan",
		RunE: func(cmd *cobra.Command, args []string) error {
			prov, err := lookupOAuthProvider(strings.TrimSpace(provider))
			if err != nil {
				return err
			}
			return runLogin(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), prov, loginOpts{
				baseURL:   strings.TrimSpace(baseURL),
				port:      port,
				noBrowser: noBrowser,
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&provider, "provider", "openai", "OAuth provider ("+availableProviders()+")")
	f.StringVar(&baseURL, "base-url", "", "Override the authorize/token host (self-hosted gateway)")
	f.IntVar(&port, "port", 0, "Loopback port (0 = auto-try the provider's allow-listed ports)")
	f.BoolVar(&noBrowser, "no-browser", false, "Print the authorize URL instead of opening a browser")
	return cmd
}

type loginOpts struct {
	baseURL   string
	port      int
	noBrowser bool
}

// runLogin wraps the shared OAuth flow with the login.result envelope. The flow
// itself (runOAuthLogin) is shared with `miucr init`'s browser-login path.
func runLogin(ctx stdctx.Context, stdout, stderr io.Writer, prov oauthProvider, opts loginOpts) error {
	rec, err := runOAuthLogin(ctx, stderr, prov, opts.baseURL, opts.port, opts.noBrowser)
	if err != nil {
		return err
	}
	oauthPath, _ := config.OAuthPath()
	expiresAt := ""
	if !rec.ExpiresAt.IsZero() {
		expiresAt = rec.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return writeSuccess(stdout, "login", "login.result", map[string]any{
		"provider":    rec.Provider,
		"oauth_path":  oauthPath,
		"expires_at":  expiresAt,
		"account_id":  rec.AccountID,
		"has_api_key": rec.APIKey != "",
	}, map[string]any{"provider": rec.Provider})
}
