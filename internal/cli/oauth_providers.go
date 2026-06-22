package cli

import "fmt"

// oauthProvider describes one OAuth login target. The registry is the extension
// point: `openai` is the only entry today; codex is NOT hardcoded into the flow.
// Adding a future compliant provider is a new entry, not a rewrite. Anthropic is
// intentionally absent (third-party OAuth is ToS-prohibited).
type oauthProvider struct {
	Name            string
	AuthURL         string
	TokenURL        string
	ClientID        string
	Scopes          []string
	BackendBaseURL  string
	Ports           []int
	ExtraAuthParams map[string]string
}

var oauthProviders = map[string]oauthProvider{
	"openai": {
		Name:     "openai",
		AuthURL:  "https://auth.openai.com/oauth/authorize",
		TokenURL: "https://auth.openai.com/oauth/token",
		ClientID: "app_EMoamEEZ73f0CkXaXp7hrann",
		Scopes: []string{
			"openid", "profile", "email", "offline_access",
			"api.connectors.read", "api.connectors.invoke",
		},
		BackendBaseURL: "https://chatgpt.com/backend-api/codex",
		Ports:          []int{1455, 1457},
		ExtraAuthParams: map[string]string{
			"id_token_add_organizations": "true",
			"codex_cli_simplified_flow":  "true",
		},
	},
}

// OAuthBackendMeta is the non-secret routing the review-time credential resolver
// needs for one provider: the token endpoint, OAuth client id, and codex backend
// host. It is exported for the wire layer (engine stays FS-free).
type OAuthBackendMeta struct {
	Provider       string
	TokenURL       string
	ClientID       string
	BackendBaseURL string
}

// OAuthBackend returns the routing for a logged-in provider, or ok=false if the
// name is not a registered OAuth provider.
func OAuthBackend(name string) (OAuthBackendMeta, bool) {
	p, ok := oauthProviders[name]
	if !ok {
		return OAuthBackendMeta{}, false
	}
	return OAuthBackendMeta{
		Provider:       p.Name,
		TokenURL:       p.TokenURL,
		ClientID:       p.ClientID,
		BackendBaseURL: p.BackendBaseURL,
	}, true
}

// lookupOAuthProvider returns the registered provider or a typed error listing
// what is available. `anthropic`/unknown are rejected here (ToS).
func lookupOAuthProvider(name string) (oauthProvider, error) {
	if p, ok := oauthProviders[name]; ok {
		return p, nil
	}
	return oauthProvider{}, &CLIError{
		Code:    "login.provider_unsupported",
		Message: fmt.Sprintf("unsupported provider %q", name),
		Hint:    "available: openai",
		Exit:    2,
	}
}
