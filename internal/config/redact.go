package config

import (
	"net/url"
	"regexp"
	"strings"
)

var (
	sensitiveAssignments = regexp.MustCompile(`(?i)(password|passwd|pwd|secret|token|api[_-]?key|auth[_-]?token|private[_-]?key|client[_-]?secret)=([^\s&]+)`)
	// header form: `Authorization: Bearer sk-...`, `x-api-key: sk-...` (also `=` delimiter).
	sensitiveHeaders = regexp.MustCompile(`(?i)(authorization|x-api-key)(\s*[:=]\s*)(?:bearer\s+)?\S+`)
	// bare bearer token anywhere in prose (must run before headers to avoid double work).
	bearerToken = regexp.MustCompile(`(?i)bearer\s+\S+`)
	// provider-key shape (sk-..., sk-ant-...) as a last-resort net for delimiter-less prose.
	providerKey = regexp.MustCompile(`sk-[A-Za-z0-9_-]{8,}`)
	// GitHub-style PATs (ghp_, gho_, ghu_, ghs_, ghr_) in delimiter-less prose.
	githubToken = regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`)
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
		out.Providers[name] = p
	}
	if out.Store.DSN != "" {
		out.Store.DSN = redactedMask
	}
	return out
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
