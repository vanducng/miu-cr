package agent

import (
	stdctx "context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	openai "github.com/openai/openai-go/v3"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
)

// fakeReqResp populates the Request/Response an SDK *Error dereferences in its
// Error() method, so a constructed SDK error renders without panicking.
func fakeReqResp(status int) (*http.Request, *http.Response) {
	req := &http.Request{Method: http.MethodPost, URL: &url.URL{Scheme: "https", Host: "api.example", Path: "/v1"}}
	return req, &http.Response{StatusCode: status}
}

func asCLIErr(t *testing.T, err error) *clierr.CLIError {
	t.Helper()
	ce, ok := err.(*clierr.CLIError)
	if !ok {
		t.Fatalf("want *clierr.CLIError, got %T: %v", err, err)
	}
	return ce
}

func TestClassifyStatusTaxonomy(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		authCode  string
		wantCode  string
		wantRetry bool
		wantHint  string
	}{
		{"401 api key", 401, codeAuthFailed, "agent.auth_failed", false, hintLoginAnthropic},
		{"403 api key", 403, codeAuthFailed, "agent.auth_failed", false, hintLoginAnthropic},
		{"401 oauth", 401, codeAuthExpired, "agent.auth_expired", false, hintLoginOpenAI},
		{"429", 429, codeAuthFailed, "provider.rate_limited", true, ""},
		{"500", 500, codeAuthFailed, "agent.unavailable", true, ""},
		{"503", 503, codeAuthFailed, "agent.unavailable", true, ""},
		{"529 overloaded", 529, codeAuthFailed, "agent.unavailable", true, ""},
		{"200 unclassified", 200, codeAuthFailed, "", false, ""},
		{"418 unclassified", 418, codeAuthFailed, "", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lh := hintLoginAnthropic
			if tt.authCode == codeAuthExpired {
				lh = hintLoginOpenAI
			}
			got := classifyStatus(tt.status, "boom", lh, tt.authCode)
			if tt.wantCode == "" {
				if got != nil {
					t.Fatalf("want nil (unclassified), got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("want code %q, got nil", tt.wantCode)
			}
			if got.Code != tt.wantCode {
				t.Fatalf("code = %q, want %q", got.Code, tt.wantCode)
			}
			if got.Retry != tt.wantRetry {
				t.Fatalf("retry = %v, want %v", got.Retry, tt.wantRetry)
			}
			if got.Hint == "" {
				t.Fatalf("expected a non-empty hint for %s", tt.wantCode)
			}
			if tt.wantHint != "" && got.Hint != tt.wantHint {
				t.Fatalf("hint = %q, want %q", got.Hint, tt.wantHint)
			}
		})
	}
}

func TestClassifyStatusRedactsSecret(t *testing.T) {
	const secret = "sk-ant-synthetic-secret-token-xyz"
	got := classifyStatus(401, "401 unauthorized: api_key="+secret, hintLoginAnthropic, codeAuthFailed)
	if got == nil {
		t.Fatal("expected a classified error")
	}
	if strings.Contains(got.Message, secret) {
		t.Fatalf("secret leaked into classified message: %q", got.Message)
	}
}

func TestClassifyAnthropicErr(t *testing.T) {
	req, resp := fakeReqResp(429)
	apiErr := &anthropic.Error{StatusCode: 429, Request: req, Response: resp}
	ce := asCLIErr(t, classifyAnthropicErr(apiErr))
	if ce.Code != "provider.rate_limited" || !ce.Retry {
		t.Fatalf("got %+v, want rate_limited+retry", ce)
	}

	req, resp = fakeReqResp(401)
	apiErr = &anthropic.Error{StatusCode: 401, Request: req, Response: resp}
	ce = asCLIErr(t, classifyAnthropicErr(apiErr))
	if ce.Code != "agent.auth_failed" {
		t.Fatalf("got %+v, want auth_failed", ce)
	}
}

func TestClassifyOpenAIErr(t *testing.T) {
	req, resp := fakeReqResp(500)
	apiErr := &openai.Error{StatusCode: 500, Request: req, Response: resp}
	ce := asCLIErr(t, classifyOpenAIErr(apiErr))
	if ce.Code != "agent.unavailable" || !ce.Retry {
		t.Fatalf("got %+v, want unavailable+retry", ce)
	}
}

// An unrecognized (non-SDK, non-status) error must keep the bare %w wrap so the
// ctx error chain survives to the review layer's errors.Is.
func TestClassifyPreservesCtxChain(t *testing.T) {
	for _, fn := range []func(error) error{classifyAnthropicErr, classifyOpenAIErr} {
		for _, ctxErr := range []error{stdctx.DeadlineExceeded, stdctx.Canceled} {
			got := fn(ctxErr)
			var ce *clierr.CLIError
			if errors.As(got, &ce) {
				t.Fatalf("ctx error was wrongly converted to a CLIError: %v", got)
			}
			if !errors.Is(got, ctxErr) {
				t.Fatalf("ctx chain broken: %v does not wrap %v", got, ctxErr)
			}
		}
	}
}
