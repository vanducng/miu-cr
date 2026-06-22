package cli

import (
	stdctx "context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/vanducng/miu-cr/internal/config"
)

const callbackHTML = `<!doctype html><meta charset=utf-8><title>miu-cr</title>` +
	`<body style="font-family:system-ui;padding:3rem"><h2>Login complete</h2>` +
	`<p>You can close this tab and return to the terminal.</p></body>`

// serveCallback serves /auth/callback on ln until it receives a valid code or the
// context is canceled. A state mismatch is a typed error (login.state_mismatch).
func serveCallback(ctx stdctx.Context, ln net.Listener, wantState string) (string, error) {
	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/auth/callback" {
				http.NotFound(w, r)
				return
			}
			q := r.URL.Query()
			writeCallbackPage(w)
			if e := q.Get("error"); e != "" {
				done <- result{err: &CLIError{Code: "login.exchange_failed", Message: "authorization error: " + sanitizeQuery(e), Exit: 1}}
				return
			}
			if q.Get("state") != wantState {
				done <- result{err: &CLIError{Code: "login.state_mismatch", Message: "OAuth state mismatch (possible CSRF); aborting", Exit: 1}}
				return
			}
			code := q.Get("code")
			if code == "" {
				done <- result{err: &CLIError{Code: "login.exchange_failed", Message: "callback missing authorization code", Exit: 1}}
				return
			}
			done <- result{code: code}
		}),
	}

	go func() { _ = srv.Serve(ln) }()
	defer shutdown(srv)

	select {
	case <-ctx.Done():
		return "", &CLIError{Code: "login.exchange_failed", Message: "login canceled before callback", Exit: 1}
	case res := <-done:
		return res.code, res.err
	}
}

func writeCallbackPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(callbackHTML))
}

func shutdown(srv *http.Server) {
	ctx, cancel := stdctx.WithTimeout(stdctx.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// sanitizeQuery keeps an OAuth error string short and redacted for the envelope.
func sanitizeQuery(s string) string {
	s = config.RedactString(s)
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// bestEffortAPIKey attempts the token-exchange→openai-api-key grant. It NEVER
// hard-fails login: any error yields "" and the codex-backend path (which uses
// the access token + the ChatGPT plan) remains the primary review path.
func bestEffortAPIKey(ctx stdctx.Context, conf *oauth2.Config, prov oauthProvider, tok *oauth2.Token) string {
	form := url.Values{
		"grant_type":           {"urn:ietf:params:oauth:grant-type:token-exchange"},
		"client_id":            {prov.ClientID},
		"requested_token":      {"openai-api-key"},
		"subject_token":        {tok.AccessToken},
		"subject_token_type":   {"urn:ietf:params:oauth:token-type:access_token"},
		"requested_token_type": {"urn:ietf:params:oauth:token-type:access_token"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, conf.Endpoint.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var out struct {
		AccessToken string `json:"access_token"`
		APIKey      string `json:"api_key"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return ""
	}
	if out.APIKey != "" {
		return out.APIKey
	}
	return out.AccessToken
}
