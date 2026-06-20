package config

import (
	"net/url"
	"regexp"
	"strings"
)

var sensitiveAssignments = regexp.MustCompile(`(?i)(password|passwd|pwd|secret|token|private[_-]?key|client[_-]?secret)=([^\s&]+)`)

func RedactString(value string) string {
	if value == "" {
		return value
	}
	value = redactURLPasswords(value)
	value = sensitiveAssignments.ReplaceAllString(value, "$1=[redacted]")
	return value
}

func redactURLPasswords(value string) string {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return value
	}
	changed := false
	for i, field := range fields {
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
		fields[i] = strings.Replace(field, trimmed, parsed.String(), 1)
		changed = true
	}
	if !changed {
		return value
	}
	return strings.Join(fields, " ")
}
