package cli

import (
	stdctx "context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2"

	"github.com/vanducng/miu-cr/internal/config"
)

// browserOpen is a seam so tests can drive the loopback callback without
// launching a real browser. It returns an error only when the OS open command
// fails; login continues either way (the URL is always printed).
var browserOpen = openBrowser

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
	f.StringVar(&provider, "provider", "openai", "OAuth provider (available: openai)")
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

func runLogin(ctx stdctx.Context, stdout, stderr io.Writer, prov oauthProvider, opts loginOpts) error {
	ln, port, err := bindLoopback(prov, opts.port)
	if err != nil {
		return err
	}
	defer ln.Close()
	redirectURI := fmt.Sprintf("http://localhost:%d/auth/callback", port)

	verifier := oauth2.GenerateVerifier()
	state, err := randomState()
	if err != nil {
		return &CLIError{Code: "login.exchange_failed", Message: "generate state: " + config.RedactString(err.Error()), Exit: 1}
	}

	conf := oauthConfig(prov, opts.baseURL, redirectURI)
	authURL := authorizeURL(conf, prov, verifier, state)

	fmt.Fprintf(stderr, "miu-cr: open this URL to authorize:\n%s\n", authURL)
	if !opts.noBrowser {
		if err := browserOpen(authURL); err != nil {
			fmt.Fprintf(stderr, "miu-cr: could not open a browser automatically (%s); use the URL above\n", config.RedactString(err.Error()))
		}
	}

	code, err := serveCallback(ctx, ln, state)
	if err != nil {
		return err
	}

	tok, err := conf.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return &CLIError{Code: "login.exchange_failed", Message: "token exchange failed: " + config.RedactString(err.Error()), Exit: 1}
	}

	idToken, _ := tok.Extra("id_token").(string)
	rec := config.OAuthRecord{
		Provider:     prov.Name,
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		IDToken:      idToken,
		AccountID:    accountIDFromIDToken(idToken),
		ExpiresAt:    tok.Expiry,
	}
	rec.APIKey = bestEffortAPIKey(ctx, conf, prov, tok)

	if err := config.SaveOAuth(rec); err != nil {
		return &CLIError{Code: "login.write_failed", Message: config.RedactString(err.Error()), Exit: 1}
	}

	oauthPath, _ := config.OAuthPath()
	expiresAt := ""
	if !rec.ExpiresAt.IsZero() {
		expiresAt = rec.ExpiresAt.UTC().Format(time.RFC3339)
	}
	fmt.Fprintf(stderr, "miu-cr: logged in (%s); token cached at %s\n", prov.Name, oauthPath)
	return writeSuccess(stdout, "login", "login.result", map[string]any{
		"provider":    prov.Name,
		"oauth_path":  oauthPath,
		"expires_at":  expiresAt,
		"account_id":  rec.AccountID,
		"has_api_key": rec.APIKey != "",
	}, map[string]any{"provider": prov.Name})
}

func oauthConfig(prov oauthProvider, baseURL, redirectURI string) *oauth2.Config {
	authURL, tokenURL := prov.AuthURL, prov.TokenURL
	if baseURL != "" {
		authURL = strings.TrimRight(baseURL, "/") + "/oauth/authorize"
		tokenURL = strings.TrimRight(baseURL, "/") + "/oauth/token"
	}
	return &oauth2.Config{
		ClientID:    prov.ClientID,
		RedirectURL: redirectURI,
		Scopes:      prov.Scopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:   authURL,
			TokenURL:  tokenURL,
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
}

func authorizeURL(conf *oauth2.Config, prov oauthProvider, verifier, state string) string {
	opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier)}
	for k, v := range prov.ExtraAuthParams {
		opts = append(opts, oauth2.SetAuthURLParam(k, v))
	}
	return conf.AuthCodeURL(state, opts...)
}

func randomState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// accountIDFromIDToken extracts chatgpt_account_id from the id_token's JWT
// payload (middle segment, base64url JSON). Returns "" on any decode failure;
// the account id is non-secret routing metadata, not a credential.
func accountIDFromIDToken(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Auth struct {
			ChatGPTAccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
		ChatGPTAccountID string `json:"chatgpt_account_id"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	if claims.Auth.ChatGPTAccountID != "" {
		return claims.Auth.ChatGPTAccountID
	}
	return claims.ChatGPTAccountID
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

// bindLoopback binds 127.0.0.1 on the requested port, or the first free provider
// port when port==0. Only the provider's allow-listed ports are tried.
func bindLoopback(prov oauthProvider, port int) (net.Listener, int, error) {
	ports := prov.Ports
	if port != 0 {
		ports = []int{port}
	}
	var lastErr error
	for _, p := range ports {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			return ln, p, nil
		}
		lastErr = err
	}
	hint := fmt.Sprintf("ports %v are reserved for this flow; free one or pass --no-browser to authorize manually", prov.Ports)
	msg := "could not bind a loopback port"
	if lastErr != nil {
		msg += ": " + config.RedactString(lastErr.Error())
	}
	return nil, 0, &CLIError{Code: "login.browser_open_failed", Message: msg, Hint: hint, Exit: 1}
}
