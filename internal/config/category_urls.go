package config

import (
	"log/slog"
	"net/url"
	"strings"
)

// maxCategoryURLLen caps a category docs URL; anything longer is dropped.
const maxCategoryURLLen = 2048

// CategoryURLMap returns the validated, lowercased-keyed category->URL map from
// the TRUSTED [review].category_urls table. Even though the source is trusted,
// each value is scheme-validated (http/https only) and length-capped as defense;
// invalid entries are dropped with a logged warning, never aborting. Returns nil
// when nothing is configured/valid, so the default render path is unchanged.
func (r Review) CategoryURLMap() map[string]string {
	if len(r.CategoryURLs) == 0 {
		return nil
	}
	out := make(map[string]string, len(r.CategoryURLs))
	for k, v := range r.CategoryURLs {
		key := strings.ToLower(strings.TrimSpace(k))
		val := strings.TrimSpace(v)
		if key == "" {
			continue
		}
		if !validCategoryURL(val) {
			slog.Warn("dropping invalid category_urls entry", "category", key)
			continue
		}
		out[key] = val
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// validCategoryURL accepts only an absolute http:// or https:// URL within the
// length cap. It rejects javascript:, data:, file:, mailto:, scheme-relative
// "//host", and any other scheme so a non-navigable/dangerous href can't render.
// It also rejects chars that break a Markdown link destination so the rendered
// [text](<url>) can't be escaped.
func validCategoryURL(raw string) bool {
	if raw == "" || len(raw) > maxCategoryURLLen {
		return false
	}
	if !markdownSafeURL(raw) {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return u.Host != ""
}

// markdownSafeURL rejects any char that breaks a Markdown link destination even
// in the angle-bracket form [text](<url>): ASCII whitespace and control chars,
// '<', '>', and backslash. A real doc URL percent-encodes these, so dropping them
// is loss-free; parens stay allowed since the <> form tolerates them.
func markdownSafeURL(raw string) bool {
	for i := 0; i < len(raw); i++ {
		switch c := raw[i]; {
		case c <= 0x20, c == 0x7f, c == '<', c == '>', c == '\\':
			return false
		}
	}
	return true
}
