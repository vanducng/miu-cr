package serve

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestServeSmoke wires a fake reviewFn into a real Pool + Server, serves the mux
// over httptest, POSTs a real-HMAC pull_request "opened" payload, and asserts the
// fake reviewer received Ref=owner/repo#N within a short wait. /healthz→200. No
// network, no LLM.
func TestServeSmoke(t *testing.T) {
	var (
		mu   sync.Mutex
		refs []string
	)
	got := make(chan string, 1)
	reviewFn := func(j Job) {
		mu.Lock()
		refs = append(refs, j.Ref)
		mu.Unlock()
		got <- j.Ref
	}

	srv, pool := New(Config{
		Addr:         ":0",
		Secret:       []byte(testSecret),
		Repos:        []string{"octocat/hello"},
		ResolveToken: func() (string, error) { return testToken, nil },
	}, reviewFn)
	defer pool.Drain()

	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	body := prPayload("opened", "octocat", "hello", 42, false)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/webhook", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-GitHub-Delivery", "smoke-1")
	req.Header.Set("X-Hub-Signature-256", sign([]byte(testSecret), body))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /webhook: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/webhook status = %d, want 200", resp.StatusCode)
	}

	select {
	case ref := <-got:
		if ref != "octocat/hello#42" {
			t.Fatalf("reviewer got Ref = %q, want octocat/hello#42", ref)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reviewer never received the dispatched job")
	}

	mu.Lock()
	n := len(refs)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("reviewFn called %d times, want 1", n)
	}

	hresp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer hresp.Body.Close()
	if hresp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz status = %d, want 200", hresp.StatusCode)
	}
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(hresp.Body)
	if !bytes.Contains(buf.Bytes(), []byte(`"status":"ok"`)) {
		t.Errorf("/healthz body = %s", buf.String())
	}
}
