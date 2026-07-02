package config

import (
	"net/url"
	"regexp"
	"strings"
)

// secretTokenChars bounds a credential to the chars real tokens use (base64url,
// hex, JWT, sk-, gh*_) so the match can't greedily eat trailing quotes/brackets
// (which corrupted traces) or, via a quote sitting before `bearer`, swallow the
// keyword and leave the actual token unredacted.
const secretTokenChars = `[A-Za-z0-9._~+/=-]+`

// optQuote optionally consumes an opening quote/backtick before a bearer/token so
// a quoted value like `'Bearer <tok>'` redacts the token, not the quote+keyword.
const optQuote = "[\"'\x60]?"

var (
	sensitiveAssignments = regexp.MustCompile(`(?i)(password|passwd|pwd|secret|token|api[_-]?key|auth[_-]?token|private[_-]?key|client[_-]?secret)=([^\s&]+)`)
	// header form: `Authorization: Bearer sk-...`, `x-api-key: sk-...` (also `=` delimiter, optional quote).
	sensitiveHeaders = regexp.MustCompile(`(?i)(authorization|x-api-key)(\s*[:=]\s*` + optQuote + `(?:bearer\s+)?)` + secretTokenChars)
	// bare bearer token anywhere in prose (must run before headers to avoid double work).
	bearerToken = regexp.MustCompile(`(?i)bearer\s+` + secretTokenChars)
	// provider-key shape (sk-..., sk-ant-...) as a last-resort net for delimiter-less prose.
	providerKey = regexp.MustCompile(`sk-[A-Za-z0-9_-]{8,}`)
	// GitHub tokens in delimiter-less prose: classic/app PATs (ghp_, gho_, ghu_,
	// ghs_, ghr_) and fine-grained PATs (github_pat_, which embed underscores).
	githubToken = regexp.MustCompile(`(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})`)
	// gateway tokens of shape `<hex>.<base64url>`, e.g. a Bearer value that
	// leaked into prose without a header/bearer prefix.
	gatewayToken = regexp.MustCompile(`\b[0-9a-fA-F]{16,}\.[A-Za-z0-9_-]{8,}\b`)
)

// redactedMask is the placeholder substituted for every secret-bearing config
// field by RedactConfig. A non-empty secret becomes this; an empty field stays
// empty so a viewer can tell "unset" from "set".
const redactedMask = "[redacted]"

// RedactConfig returns a deep copy of cfg with every secret-bearing field masked
// by construction (not by free-text regex): each provider's AuthToken and the
// store DSN. Non-secret fields (kind, base_url, model, auth_env name, backend,
// review/github/embedding/history) are preserved verbatim. An empty secret stays
// empty so `config show` can distinguish unset from set. This is the structural
// guarantee `config show` relies on so no token/DSN can ever print.
func RedactConfig(cfg Config) Config {
	out := cfg
	out.Providers = make(map[string]Provider, len(cfg.Providers))
	for name, p := range cfg.Providers {
		if p.AuthToken != "" {
			p.AuthToken = redactedMask
		}
		if p.AuthCommand != nil {
			p.AuthCommand = append([]string(nil), p.AuthCommand...)
			for i := range p.AuthCommand {
				p.AuthCommand[i] = RedactString(p.AuthCommand[i])
			}
		}
		out.Providers[name] = p
	}
	// Clone the only other nested reference field so the returned config never shares
	// a backing map with the input (out := cfg is a shallow struct copy).
	if cfg.Review.CategoryURLs != nil {
		out.Review.CategoryURLs = make(map[string]string, len(cfg.Review.CategoryURLs))
		for k, v := range cfg.Review.CategoryURLs {
			out.Review.CategoryURLs[k] = v
		}
	}
	if out.Store.DSN != "" {
		out.Store.DSN = redactedMask
	}
	return out
}

func RedactHostConfig(cfg HostConfig) HostConfig {
	out := cfg
	out.Providers = make(map[string]HostProvider, len(cfg.Providers))
	for name, p := range cfg.Providers {
		if p.AuthToken != "" {
			p.AuthToken = redactedMask
		}
		if len(p.AuthCommand) > 0 {
			p.AuthCommand = []string{redactedMask}
		}
		out.Providers[name] = p
	}
	if out.Store.DSN != "" {
		out.Store.DSN = redactedMask
	}
	out.Agent = redactHostAgent(cfg.Agent)
	if cfg.Github.Accounts != nil {
		out.Github.Accounts = make(map[string]HostGithubAccount, len(cfg.Github.Accounts))
		for name, acct := range cfg.Github.Accounts {
			if acct.AuthFile != "" {
				acct.AuthFile = redactedMask
			}
			if len(acct.AuthCommand) > 0 {
				acct.AuthCommand = []string{redactedMask}
			}
			if acct.AppID != "" {
				acct.AppID = redactedMask
			}
			if acct.ClientID != "" {
				acct.ClientID = redactedMask
			}
			if acct.InstallationID != "" {
				acct.InstallationID = redactedMask
			}
			if acct.PrivateKeyPath != "" {
				acct.PrivateKeyPath = redactedMask
			}
			if len(acct.PrivateKeyCommand) > 0 {
				acct.PrivateKeyCommand = []string{redactedMask}
			}
			out.Github.Accounts[name] = acct
		}
	}
	if cfg.Repos != nil {
		out.Repos = make([]HostRepo, len(cfg.Repos))
		for i, repo := range cfg.Repos {
			repo.Agent = redactHostAgent(repo.Agent)
			if len(repo.Rules) > 0 {
				repo.Rules = []string{redactedMask}
			}
			out.Repos[i] = repo
		}
	}
	return out
}

func redactHostAgent(agent HostAgent) HostAgent {
	if agent.SystemPrompt != "" {
		agent.SystemPrompt = redactedMask
	}
	if agent.SystemPromptFile != "" {
		agent.SystemPromptFile = redactedMask
	}
	return agent
}

// RedactString masks credentials in an arbitrary string: URL userinfo passwords,
// key=value secret assignments, Authorization/x-api-key header values, bare Bearer
// tokens, and delimiter-less provider tokens (sk-, GitHub gh*_, and gateway
// <hex>.<token> shapes). It is the last-resort net for free-text error/log
// output, so the "tokens are never logged" invariant rests on it.
func RedactString(value string) string {
	if value == "" {
		return value
	}
	value = redactURLPasswords(value)
	value = sensitiveAssignments.ReplaceAllString(value, "$1=[redacted]")
	value = sensitiveHeaders.ReplaceAllString(value, "$1$2[redacted]")
	value = bearerToken.ReplaceAllString(value, "bearer [redacted]")
	value = providerKey.ReplaceAllString(value, "[redacted]")
	value = githubToken.ReplaceAllString(value, "[redacted]")
	value = gatewayToken.ReplaceAllString(value, "[redacted]")
	return value
}

func redactURLPasswords(value string) string {
	result := value
	for _, field := range strings.Fields(value) {
		if !strings.Contains(field, "://") {
			continue
		}
		trimmed := strings.Trim(field, "`'\"")
		parsed, err := url.Parse(trimmed)
		if err != nil || parsed.User == nil {
			continue
		}
		if _, ok := parsed.User.Password(); !ok {
			continue
		}
		username := parsed.User.Username()
		parsed.User = url.UserPassword(username, "[redacted]")
		result = strings.Replace(result, trimmed, parsed.String(), 1)
	}
	return result
}
