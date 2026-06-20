package agent

import (
	stdctx "context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// captureRT records the outgoing request and returns a canned 200 so no network
// is touched. It lets us assert the auth headers and base URL anthropicOptions
// produces, applied through a real SDK request.
type captureRT struct{ req *http.Request }

func (c *captureRT) RoundTrip(r *http.Request) (*http.Response, error) {
	c.req = r
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"msg_1","type":"message","role":"assistant","model":"m",` +
				`"content":[{"type":"text","text":"{\"findings\":[]}"}],` +
				`"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)),
		Request: r,
	}, nil
}

// anthropicOptions is the load-bearing gateway wiring for z.ai/glm support.
// Assert the headers/URL of the real request for the bearer, api-key, and
// base-URL paths. No network, no real key.
func TestAnthropicOptionsRequestWiring(t *testing.T) {
	tests := []struct {
		name          string
		creds         Credentials
		wantAuth      string // Authorization header value ("" => absent)
		wantAPIKey    string // x-api-key header value ("" => absent)
		wantHostMatch string // substring required in request URL host
	}{
		{
			name:          "bearer path drops x-api-key",
			creds:         Credentials{AuthToken: "zai-secret", BaseURL: "https://api.z.ai/api/anthropic"},
			wantAuth:      "Bearer zai-secret",
			wantAPIKey:    "",
			wantHostMatch: "api.z.ai",
		},
		{
			name:          "api-key path uses x-api-key",
			creds:         Credentials{APIKey: "sk-ant-xyz"},
			wantAuth:      "",
			wantAPIKey:    "sk-ant-xyz",
			wantHostMatch: "api.anthropic.com",
		},
		{
			name:          "base url applied with api key",
			creds:         Credentials{APIKey: "sk-ant-xyz", BaseURL: "https://gateway.example/anthropic"},
			wantAuth:      "",
			wantAPIKey:    "sk-ant-xyz",
			wantHostMatch: "gateway.example",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := &captureRT{}
			opts := append(anthropicOptions(tt.creds), option.WithHTTPClient(&http.Client{Transport: rt}))
			client := anthropic.NewClient(opts...)

			_, err := client.Messages.New(stdctx.Background(), anthropic.MessageNewParams{
				MaxTokens: 1,
				Model:     anthropic.Model("m"),
				Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))},
			})
			if err != nil {
				t.Fatalf("Messages.New: %v", err)
			}
			if rt.req == nil {
				t.Fatal("no request captured")
			}

			if got := rt.req.Header.Get("Authorization"); got != tt.wantAuth {
				t.Fatalf("Authorization = %q, want %q", got, tt.wantAuth)
			}
			if got := rt.req.Header.Get("X-Api-Key"); got != tt.wantAPIKey {
				t.Fatalf("X-Api-Key = %q, want %q", got, tt.wantAPIKey)
			}
			if !strings.Contains(rt.req.URL.Host, tt.wantHostMatch) {
				t.Fatalf("request host %q does not contain %q", rt.req.URL.Host, tt.wantHostMatch)
			}
			// Bearer path must NOT also send x-api-key (the regression guarded).
			if tt.creds.AuthToken != "" && rt.req.Header.Get("X-Api-Key") != "" {
				t.Fatal("bearer path leaked x-api-key alongside Authorization")
			}
		})
	}
}
