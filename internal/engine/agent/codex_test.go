package agent

import (
	stdctx "context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	"github.com/vanducng/miu-cr/internal/config"
)

// codexMessageResp builds a Responses payload whose single message output_text
// is the given content (e.g. the findings JSON).
// codexSSE wraps a Responses output object as the terminal SSE event the codex
// backend streams (stream:true). post() parses this.
func codexSSE(output []map[string]any) string {
	b, _ := json.Marshal(map[string]any{
		"type":     "response.completed",
		"response": map[string]any{"output": output},
	})
	return "event: response.completed\ndata: " + string(b) + "\n\n"
}

func codexMessageResp(content string) string {
	return codexSSE([]map[string]any{{
		"type":    "message",
		"content": []map[string]any{{"type": "output_text", "text": content}},
	}})
}

func codexFunctionCallResp(callID, name, args string) string {
	return codexSSE([]map[string]any{{
		"type":      "function_call",
		"name":      name,
		"call_id":   callID,
		"arguments": args,
	}})
}

// codexFailedSSE emits a response.failed event carrying the given error
// type/code/message (post() inspects type/code to decide retry vs terminal).
func codexFailedSSE(errType, code, message string) string {
	b, _ := json.Marshal(map[string]any{
		"type": "response.failed",
		"response": map[string]any{
			"error": map[string]any{"type": errType, "code": code, "message": message},
		},
	})
	return "event: response.failed\ndata: " + string(b) + "\n\n"
}

const codexFindingsJSON = `{"findings":[{"file":"pkg/sample.go","severity":"high","category":"bug","existing_code":"return nil","rationale":"swallows the error","suggested_patch":"return err"}]}`

func newTestCodexAgent(t *testing.T, srv *httptest.Server) *codexAgent {
	t.Helper()
	creds := Credentials{
		Kind:           config.KindOpenAI,
		Backend:        "codex",
		OAuthToken:     "access-tok-1",
		OAuthAccountID: "acct-123",
		BaseURL:        srv.URL,
		Model:          "gpt-test",
		HTTPClient:     srv.Client(),
	}
	a, ok := newCodexAgentFromNew(t, creds)
	if !ok {
		t.Fatal("New did not build a codexAgent")
	}
	return a
}

// newCodexAgentFromNew exercises the registry/factory path (Backend=="codex").
func newCodexAgentFromNew(t *testing.T, creds Credentials) (*codexAgent, bool) {
	t.Helper()
	llm, err := New(creds, 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ca, ok := llm.(*codexAgent)
	return ca, ok
}

func TestCodexAgentPostsResponsesAndParses(t *testing.T) {
	var gotPath, gotAuth, gotAccount, gotBeta string
	var gotBody codexReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccount = r.Header.Get("ChatGPT-Account-ID")
		gotBeta = r.Header.Get("OpenAI-Beta")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		io.WriteString(w, codexMessageResp(codexFindingsJSON))
	}))
	defer srv.Close()

	a := newTestCodexAgent(t, srv)
	out, err := a.Review(stdctx.Background(), Context{Text: "diff --git a/x b/x"})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	findings := out.Findings
	if len(findings) != 1 || findings[0].File != "pkg/sample.go" || findings[0].QuotedCode != "return nil" {
		t.Fatalf("unexpected findings: %+v", findings)
	}
	if gotPath != "/responses" {
		t.Errorf("path = %q, want /responses", gotPath)
	}
	if gotAuth != "Bearer access-tok-1" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotAccount != "acct-123" {
		t.Errorf("ChatGPT-Account-ID = %q", gotAccount)
	}
	if gotBeta == "" {
		t.Errorf("OpenAI-Beta header missing")
	}
	if gotBody.Model != "gpt-test" || gotBody.Store {
		t.Errorf("body model/store = %q/%v", gotBody.Model, gotBody.Store)
	}
	if gotBody.Instructions == "" || len(gotBody.Input) == 0 {
		t.Errorf("body missing instructions/input: %+v", gotBody)
	}
	if !strings.Contains(gotBody.Input[0].Content[0].Text, "diff --git") {
		t.Errorf("user input did not carry the diff: %+v", gotBody.Input[0])
	}
}

// RepairPatch must use the SAME SSE/stream path as Review (stream:true, Accept:
// text/event-stream, no tools, repairSystemPrompt instructions) and return the
// fence-stripped reply.
func TestCodexAgentRepairPatch(t *testing.T) {
	var gotBody codexReq
	var gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		io.WriteString(w, codexMessageResp("```go\nval, ok := m[key]\n```"))
	}))
	defer srv.Close()

	a := newTestCodexAgent(t, srv)
	out, err := a.RepairPatch(stdctx.Background(), RepairRequest{Span: "val := m[key]", Rationale: "missing check", Category: "bug", Severity: "high"})
	if err != nil {
		t.Fatalf("RepairPatch: %v", err)
	}
	if out != "val, ok := m[key]" {
		t.Fatalf("reply not fence-stripped: %q", out)
	}
	if !gotBody.Stream {
		t.Error("repair must use stream:true (SSE path)")
	}
	if gotAccept != "text/event-stream" {
		t.Errorf("Accept = %q, want text/event-stream", gotAccept)
	}
	if len(gotBody.Tools) != 0 {
		t.Errorf("repair must offer no tools, got %d", len(gotBody.Tools))
	}
	if gotBody.Instructions != repairSystemPrompt {
		t.Errorf("repair must use repairSystemPrompt instructions")
	}
	if len(gotBody.Input) == 0 || !strings.Contains(gotBody.Input[0].Content[0].Text, "val := m[key]") {
		t.Errorf("span missing from repair input: %+v", gotBody.Input)
	}
}

func TestCodexAgentToolLoopThenFindings(t *testing.T) {
	var calls int
	var sawToolOutput bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body codexReq
		_ = json.Unmarshal(raw, &body)
		for _, it := range body.Input {
			if it.Type == "function_call_output" {
				sawToolOutput = true
			}
		}
		if calls == 0 {
			calls++
			io.WriteString(w, codexFunctionCallResp("call-1", "grep", `{"pattern":"TODO"}`))
			return
		}
		calls++
		io.WriteString(w, codexMessageResp(codexFindingsJSON))
	}))
	defer srv.Close()

	a := newTestCodexAgent(t, srv)
	out, err := a.Review(stdctx.Background(), Context{Text: "diff", RepoDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(out.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(out.Findings))
	}
	if !sawToolOutput {
		t.Error("second request did not echo a function_call_output item")
	}
}

func TestCodexAgentRefreshesOn401(t *testing.T) {
	var calls int
	var lastAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastAuth = r.Header.Get("Authorization")
		if calls == 0 {
			calls++
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		calls++
		io.WriteString(w, codexMessageResp(codexFindingsJSON))
	}))
	defer srv.Close()

	refreshed := false
	creds := Credentials{
		Kind: config.KindOpenAI, Backend: "codex",
		OAuthToken: "stale-tok", OAuthAccountID: "acct", BaseURL: srv.URL,
		Model: "gpt-test", HTTPClient: srv.Client(),
		OAuthRefresh: func(stdctx.Context) (string, error) {
			refreshed = true
			return "fresh-tok", nil
		},
	}
	a, ok := newCodexAgentFromNew(t, creds)
	if !ok {
		t.Fatal("expected codexAgent")
	}
	out, err := a.Review(stdctx.Background(), Context{Text: "diff"})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if !refreshed {
		t.Error("refresh callback was not invoked on 401")
	}
	if lastAuth != "Bearer fresh-tok" {
		t.Errorf("retry used %q, want Bearer fresh-tok", lastAuth)
	}
	if len(out.Findings) != 1 {
		t.Fatalf("findings = %d", len(out.Findings))
	}
}

func TestCodexAgentBackendErrorRedactsToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":"boom"}`)
	}))
	defer srv.Close()

	a := newTestCodexAgent(t, srv)
	_, err := a.Review(stdctx.Background(), Context{Text: "diff"})
	if err == nil {
		t.Fatal("expected error on 500")
	}
	if strings.Contains(err.Error(), "access-tok-1") {
		t.Errorf("error leaked the token: %v", err)
	}
}

func TestCodexAgentClassifiesStatus(t *testing.T) {
	shrinkCodexBackoff(t) // 429/5xx are retryable, don't sleep through real backoff
	tests := []struct {
		name      string
		status    int
		wantCode  string
		wantRetry bool
	}{
		{"401 expired", http.StatusUnauthorized, "agent.auth_expired", false},
		{"403 forbidden", http.StatusForbidden, "agent.auth_expired", false},
		{"429 rate limited", http.StatusTooManyRequests, "provider.rate_limited", true},
		{"500 unavailable", http.StatusInternalServerError, "agent.unavailable", true},
		{"503 unavailable", http.StatusServiceUnavailable, "agent.unavailable", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				// embed the bearer token in the body to prove redaction
				io.WriteString(w, `{"error":"denied for Bearer access-tok-1"}`)
			}))
			defer srv.Close()

			// No refresh callback ⇒ a 401 returns directly (does not loop).
			a := newTestCodexAgent(t, srv)
			a.refresh = nil
			_, err := a.Review(stdctx.Background(), Context{Text: "diff"})
			ce, ok := err.(*clierr.CLIError)
			if !ok {
				t.Fatalf("want *clierr.CLIError, got %T: %v", err, err)
			}
			if ce.Code != tt.wantCode {
				t.Fatalf("code = %q, want %q", ce.Code, tt.wantCode)
			}
			if ce.Retry != tt.wantRetry {
				t.Fatalf("retry = %v, want %v", ce.Retry, tt.wantRetry)
			}
			if ce.Hint == "" && (tt.wantCode == "agent.auth_expired") {
				t.Fatalf("expected a login hint for %s", tt.wantCode)
			}
			if strings.Contains(ce.Message, "access-tok-1") {
				t.Fatalf("token leaked into classified message: %q", ce.Message)
			}
		})
	}
}

// shrinkCodexBackoff makes the retry sleeps near-zero so the loop tests run
// fast and deterministically; restored on cleanup.
func shrinkCodexBackoff(t *testing.T) {
	t.Helper()
	base, maxb, attempts, maxRA := codexBaseBackoff, codexMaxBackoff, codexMaxAttempts, codexMaxRetryAfter
	codexBaseBackoff = time.Millisecond
	codexMaxBackoff = 2 * time.Millisecond
	codexMaxRetryAfter = 2 * time.Millisecond
	codexMaxAttempts = 3
	t.Cleanup(func() {
		codexBaseBackoff, codexMaxBackoff, codexMaxAttempts, codexMaxRetryAfter = base, maxb, attempts, maxRA
	})
}

func TestCodexAgentRetriesThenSucceeds(t *testing.T) {
	shrinkCodexBackoff(t)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls == 0 {
			calls++
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":"slow down"}`)
			return
		}
		calls++
		io.WriteString(w, codexMessageResp(codexFindingsJSON))
	}))
	defer srv.Close()

	a := newTestCodexAgent(t, srv)
	out, err := a.Review(stdctx.Background(), Context{Text: "diff"})
	if err != nil {
		t.Fatalf("Review after one 429 retry: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (one retry)", calls)
	}
	if len(out.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(out.Findings))
	}
}

func TestCodexAgentPersistent429SurfacesReset(t *testing.T) {
	shrinkCodexBackoff(t)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
		// usage_limit_reached body + a token to prove redaction
		io.WriteString(w, `{"error":{"type":"usage_limit_reached","resets_in_seconds":7200,"detail":"Bearer access-tok-1"}}`)
	}))
	defer srv.Close()

	a := newTestCodexAgent(t, srv)
	_, err := a.Review(stdctx.Background(), Context{Text: "diff"})
	ce, ok := err.(*clierr.CLIError)
	if !ok {
		t.Fatalf("want *clierr.CLIError, got %T: %v", err, err)
	}
	if ce.Code != "provider.rate_limited" || !ce.Retry {
		t.Fatalf("got %+v, want provider.rate_limited+retry", ce)
	}
	if calls != codexMaxAttempts {
		t.Fatalf("calls = %d, want %d (bounded attempts)", calls, codexMaxAttempts)
	}
	if got, _ := ce.Details["resets_in_seconds"].(int); got != 7200 {
		t.Fatalf("Details[resets_in_seconds] = %v, want 7200", ce.Details["resets_in_seconds"])
	}
	if !strings.Contains(ce.Hint, "resets in") {
		t.Fatalf("Hint missing reset window: %q", ce.Hint)
	}
	if strings.Contains(ce.Message, "access-tok-1") || strings.Contains(ce.Hint, "access-tok-1") {
		t.Fatalf("token leaked: msg=%q hint=%q", ce.Message, ce.Hint)
	}
}

func TestCodexAgentRetries5xx(t *testing.T) {
	shrinkCodexBackoff(t)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls < 2 {
			calls++
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		calls++
		io.WriteString(w, codexMessageResp(codexFindingsJSON))
	}))
	defer srv.Close()

	a := newTestCodexAgent(t, srv)
	out, err := a.Review(stdctx.Background(), Context{Text: "diff"})
	if err != nil {
		t.Fatalf("Review after two 503 retries: %v", err)
	}
	if calls != 3 || len(out.Findings) != 1 {
		t.Fatalf("calls=%d findings=%d, want 3/1", calls, len(out.Findings))
	}
}

func TestCodexAgentCancelDuringBackoffReturnsPromptly(t *testing.T) {
	// Long backoff so the test's own cancel, not a timer, ends the wait.
	base, attempts := codexBaseBackoff, codexMaxAttempts
	codexBaseBackoff = 30 * time.Second
	codexMaxAttempts = 3
	t.Cleanup(func() { codexBaseBackoff, codexMaxAttempts = base, attempts })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	ctx, cancel := stdctx.WithCancel(stdctx.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()

	a := newTestCodexAgent(t, srv)
	done := make(chan error, 1)
	go func() { _, err := a.Review(ctx, Context{Text: "diff"}); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, stdctx.Canceled) {
			t.Fatalf("err = %v, want context.Canceled (no double-wrap)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Review did not abort promptly on ctx cancel during backoff")
	}
}

func TestCodexAgentRefreshFailedIsAuthExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	creds := Credentials{
		Kind: config.KindOpenAI, Backend: "codex",
		OAuthToken: "stale-tok", OAuthAccountID: "acct", BaseURL: srv.URL,
		Model: "gpt-test", HTTPClient: srv.Client(),
		OAuthRefresh: func(stdctx.Context) (string, error) {
			return "", errors.New("refresh endpoint 400")
		},
	}
	a, ok := newCodexAgentFromNew(t, creds)
	if !ok {
		t.Fatal("expected codexAgent")
	}
	_, err := a.Review(stdctx.Background(), Context{Text: "diff"})
	ce, ok := err.(*clierr.CLIError)
	if !ok {
		t.Fatalf("want *clierr.CLIError, got %T: %v", err, err)
	}
	if ce.Code != "agent.auth_expired" || ce.Hint == "" {
		t.Fatalf("got %+v, want auth_expired+hint", ce)
	}
}

// TestCodexAgentRefreshTransientIsRetryable proves a transient refresh failure
// (Retry-typed clierr from the OAuth endpoint) surfaces as retryable
// agent.unavailable, not a misleading re-login prompt, preserving the cause.
func TestCodexAgentRefreshTransientIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	refreshErr := &clierr.CLIError{Code: "oauth.refresh_unavailable", Message: "network down", Retry: true}
	creds := Credentials{
		Kind: config.KindOpenAI, Backend: "codex",
		OAuthToken: "stale-tok", OAuthAccountID: "acct", BaseURL: srv.URL,
		Model: "gpt-test", HTTPClient: srv.Client(),
		OAuthRefresh: func(stdctx.Context) (string, error) { return "", refreshErr },
	}
	a, ok := newCodexAgentFromNew(t, creds)
	if !ok {
		t.Fatal("expected codexAgent")
	}
	_, err := a.Review(stdctx.Background(), Context{Text: "diff"})
	ce, ok := err.(*clierr.CLIError)
	if !ok {
		t.Fatalf("want *clierr.CLIError, got %T: %v", err, err)
	}
	if ce.Code != "agent.unavailable" || !ce.Retry {
		t.Fatalf("got %+v, want agent.unavailable + Retry", ce)
	}
	if !errors.Is(err, refreshErr) {
		t.Fatalf("refresh error not preserved as cause: %v", err)
	}
}

// TestCodexAgentResponseFailedRetryPath exercises response.failed both ways: a
// transient code retries to give-up (a bare, untyped error), a permanent code
// (invalid_request/content-policy) is terminal on the first attempt.
func TestCodexAgentResponseFailedRetryPath(t *testing.T) {
	t.Run("transient retries then gives up", func(t *testing.T) {
		shrinkCodexBackoff(t)
		var calls int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			io.WriteString(w, codexFailedSSE("server_error", "", "upstream hiccup"))
		}))
		defer srv.Close()

		a := newTestCodexAgent(t, srv)
		_, err := a.Review(stdctx.Background(), Context{Text: "diff"})
		if err == nil {
			t.Fatal("expected error after retries exhausted")
		}
		if calls != codexMaxAttempts {
			t.Fatalf("calls = %d, want %d (transient response.failed must retry)", calls, codexMaxAttempts)
		}
		// status 0 ⇒ classifyCodexStatus returns nil ⇒ bare untyped error
		if ce, ok := err.(*clierr.CLIError); ok {
			t.Fatalf("transient give-up should be a bare error, got typed %+v", ce)
		}
	})

	t.Run("permanent is terminal without retry", func(t *testing.T) {
		shrinkCodexBackoff(t)
		var calls int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			io.WriteString(w, codexFailedSSE("invalid_request_error", "content_policy_violation", "blocked"))
		}))
		defer srv.Close()

		a := newTestCodexAgent(t, srv)
		_, err := a.Review(stdctx.Background(), Context{Text: "diff"})
		if err == nil {
			t.Fatal("expected terminal error")
		}
		if calls != 1 {
			t.Fatalf("calls = %d, want 1 (permanent response.failed must not retry)", calls)
		}
	})
}
