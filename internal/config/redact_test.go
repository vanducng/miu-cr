package config

import (
	"strings"
	"testing"
)

func TestRedactStringAssignments(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		leaked  string // must NOT appear
		keepKey string // key name should survive
	}{
		{"password", "PGPASSWORD=hunter2 connecting", "hunter2", "PGPASSWORD"},
		{"passwd", "passwd=hunter2", "hunter2", "passwd"},
		{"pwd", "db_pwd=s3cr3t", "s3cr3t", "pwd"},
		{"secret", "client secret=topsecret here", "topsecret", "secret"},
		{"token", "auth_token=abc.def.ghi failed", "abc.def.ghi", "token"},
		{"anthropic key", "ANTHROPIC_API_KEY=sk-ant-abcdef123 invalid", "sk-ant-abcdef123", "API_KEY"},
		{"openai key", "OPENAI_API_KEY=sk-openaikey99 bad", "sk-openaikey99", "API_KEY"},
		{"apikey nounderscore", "apikey=rawvalue123", "rawvalue123", "apikey"},
		{"private_key", "private_key=PEMDATA", "PEMDATA", "private_key"},
		{"client_secret", "client_secret=zzz", "zzz", "client_secret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactString(tt.in)
			if strings.Contains(got, tt.leaked) {
				t.Fatalf("leaked %q in %q", tt.leaked, got)
			}
			if !strings.Contains(got, "[redacted]") {
				t.Fatalf("no [redacted] marker in %q", got)
			}
		})
	}
}

func TestRedactStringHeaderAndBearer(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		leaked string
	}{
		{"authorization bearer", "Authorization: Bearer sk-ant-abc123def456", "sk-ant-abc123def456"},
		{"x-api-key header", "x-api-key: sk-proj-deadbeef99", "sk-proj-deadbeef99"},
		{"json authorization", `{"authorization":"Bearer sk-livekey00"}`, "sk-livekey00"},
		{"bare bearer", "got 401 with bearer sk-anytoken1234 nope", "sk-anytoken1234"},
		{"provider key shape", "401 invalid api key sk-ant-12345678 rejected", "sk-ant-12345678"},
		{"x-api-key equals form", "x-api-key=sk-eqdelim123456", "sk-eqdelim123456"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactString(tt.in)
			if strings.Contains(got, tt.leaked) {
				t.Fatalf("leaked %q in %q", tt.leaked, got)
			}
		})
	}
}

// Delimiter-less provider tokens (no header/bearer/= prefix) must still be caught
// by the last-resort net: GitHub PATs and z.ai/GLM gateway <hex>.<token> shapes.
func TestRedactStringDelimiterlessProviderTokens(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		leaked string
	}{
		{"github pat", "error: bad credentials ghp_AbCdEf0123456789AbCdEf0123456789 rejected", "ghp_AbCdEf0123456789AbCdEf0123456789"},
		{"github oauth", "401 with gho_0123456789ABCDEFGHIJ0123456789abcd here", "gho_0123456789ABCDEFGHIJ0123456789abcd"},
		{"gateway token bare", "upstream said: token 1a2b3c4d5e6f7890.abc_DEF-123456 invalid", "1a2b3c4d5e6f7890.abc_DEF-123456"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactString(tt.in)
			if strings.Contains(got, tt.leaked) {
				t.Fatalf("leaked %q in %q", tt.leaked, got)
			}
		})
	}
}

func TestRedactStringURLPasswords(t *testing.T) {
	in := "fatal: cannot fetch https://user:s3cr3tpw@example.com/repo.git now"
	got := RedactString(in)
	if strings.Contains(got, "s3cr3tpw") {
		t.Fatalf("URL password leaked: %q", got)
	}
	if !strings.Contains(got, "user:") {
		t.Fatalf("username should survive: %q", got)
	}

	// No password in URL: unchanged.
	noPass := "see https://example.com/repo.git"
	if RedactString(noPass) != noPass {
		t.Fatalf("URL without password was altered: %q", RedactString(noPass))
	}

	// Multiple URLs in one string.
	multi := "https://a:apw@x.com and https://b:bpw@y.com"
	gm := RedactString(multi)
	if strings.Contains(gm, "apw") || strings.Contains(gm, "bpw") {
		t.Fatalf("multi-URL passwords leaked: %q", gm)
	}

	// Backtick/quote-wrapped URL.
	wrapped := "`https://u:wrapped@h.com`"
	if strings.Contains(RedactString(wrapped), "wrapped") {
		t.Fatalf("wrapped URL password leaked: %q", RedactString(wrapped))
	}
}

func TestRedactStringBenign(t *testing.T) {
	cases := []string{
		"",
		"this is a perfectly fine error message",
		"file not found: /tmp/x",
		"connection refused on port 5432",
	}
	for _, c := range cases {
		if got := RedactString(c); got != c {
			t.Fatalf("benign %q was altered to %q", c, got)
		}
	}
}
