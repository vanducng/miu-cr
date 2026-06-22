package cli

import (
	stdctx "context"
	"fmt"
	"io"
	"strings"

	"github.com/vanducng/miu-cr/internal/config"
)

// authMethod records how the credential is supplied, for the payoff + envelope.
type authMethod string

const (
	authMethodOAuth authMethod = "oauth"
	authMethodEnv   authMethod = "env"
	authMethodPaste authMethod = "paste"
)

const plaintextWarning = "  miu-cr: warning: provider auth_token is stored in plaintext on disk; prefer auth_env (the NAME of an env var holding the token)"

// initOAuthLogin is a seam so init tests can drive the browser-OAuth flow offline
// (point it at an httptest auth server + a fake browser-open seam).
var initOAuthLogin = runOAuthLogin

// authInput carries the non-interactive flag drivers into chooseAuth.
type authInput struct {
	nonInteractive bool
	flagAuth       string
	flagAuthEnv    string
}

// chooseAuth renders a provider-aware authentication menu and records the chosen
// method. openai offers browser OAuth (default), env var, or paste; anthropic and
// custom offer env var (default) or paste — anthropic never offers OAuth (ToS).
func chooseAuth(ctx stdctx.Context, ask func(string, string) string, out io.Writer, in authInput, provider string, prof *config.Provider) (authMethod, error) {
	if in.nonInteractive {
		return nonInteractiveAuth(in, provider, prof)
	}
	if provider == "openai" {
		printMenu(out, "Authenticate with openai:", []menuItem{
			{"1", "", "Browser login (OAuth) — use your ChatGPT/Codex plan, no API key"},
			{"2", "", "Environment variable — OPENAI_API_KEY"},
			{"3", "", "Paste API key — stored in config (plaintext)"},
		})
		switch ask("  Auth method", "1") {
		case "2":
			return authEnv(ask, provider, prof)
		case "3":
			return authPaste(ask, out, prof)
		default:
			return authOAuth(ctx, out, prof)
		}
	}

	defEnv := defaultAuthEnv(provider, *prof)
	printMenu(out, "Authenticate with "+provider+":", []menuItem{
		{"1", "", "Environment variable — " + defEnv},
		{"2", "", "Paste API key — stored in config (plaintext)"},
	})
	if ask("  Auth method", "1") == "2" {
		return authPaste(ask, out, prof)
	}
	return authEnv(ask, provider, prof)
}

// nonInteractiveAuth drives auth from flags. Only env is fully headless; oauth
// needs a browser and paste needs a TTY, so both error toward the right command.
func nonInteractiveAuth(in authInput, provider string, prof *config.Provider) (authMethod, error) {
	switch strings.ToLower(strings.TrimSpace(in.flagAuth)) {
	case "", "env":
		env := strings.TrimSpace(in.flagAuthEnv)
		if env == "" {
			env = defaultAuthEnv(provider, *prof)
		}
		prof.AuthEnv = env
		return authMethodEnv, nil
	case "oauth":
		return "", &CLIError{Code: "init.aborted", Message: "browser login (OAuth) requires an interactive terminal", Hint: "run `miucr login` to authenticate, then re-run init", Exit: 2}
	case "paste":
		return "", &CLIError{Code: "init.aborted", Message: "paste requires an interactive terminal", Hint: "use --auth env with --auth-env, or run init interactively", Exit: 2}
	default:
		return "", &CLIError{Code: "init.aborted", Message: "unknown --auth " + in.flagAuth, Hint: "use oauth | env | paste", Exit: 2}
	}
}

// authOAuth runs the shared browser-OAuth flow; the token lands in oauth.json and
// the profile stays the default openai profile so reviews resolve via oauth.json.
func authOAuth(ctx stdctx.Context, out io.Writer, prof *config.Provider) (authMethod, error) {
	prov, err := lookupOAuthProvider("openai")
	if err != nil {
		return "", err
	}
	if _, err := initOAuthLogin(ctx, out, prov, "", 0, false); err != nil {
		return "", err
	}
	prof.AuthEnv = ""
	prof.AuthToken = ""
	return authMethodOAuth, nil
}

// authEnv records the NAME of an env var holding the key — no secret on disk.
func authEnv(ask func(string, string) string, provider string, prof *config.Provider) (authMethod, error) {
	defEnv := defaultAuthEnv(provider, *prof)
	env := strings.TrimSpace(ask("  Env var name", defEnv))
	if env == "" {
		env = defEnv
	}
	prof.AuthEnv = env
	return authMethodEnv, nil
}

// authPaste writes a literal auth_token, but only after the plaintext warning and
// an explicit confirm.
func authPaste(ask func(string, string) string, out io.Writer, prof *config.Provider) (authMethod, error) {
	fmt.Fprintln(out, "  miu-cr: note: the key will be visible as you type (no terminal masking)")
	token := strings.TrimSpace(ask("  Paste API key", ""))
	if token == "" {
		return "", &CLIError{Code: "init.aborted", Message: "no key pasted", Hint: "re-run and choose env var, or paste a key", Exit: 2}
	}
	fmt.Fprintln(out, plaintextWarning)
	if !confirm(ask, "  Write the key to "+config.FilePathOrEmpty()+" in plaintext?", false) {
		return "", &CLIError{Code: "init.aborted", Message: "paste-now declined", Hint: "choose env var instead", Exit: 2}
	}
	prof.AuthToken = token
	return authMethodPaste, nil
}

// defaultAuthEnv is the conventional key env var for a provider kind.
func defaultAuthEnv(provider string, prof config.Provider) string {
	if prof.Kind == config.KindOpenAI || provider == "openai" {
		return "OPENAI_API_KEY"
	}
	return "ANTHROPIC_API_KEY"
}
