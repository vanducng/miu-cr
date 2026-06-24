package agent

import (
	stdctx "context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
