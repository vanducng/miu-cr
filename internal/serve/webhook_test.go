package serve

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const testSecret = "s3cr3t-webhook-key"
const testToken = "ghp_FAKEtoken1234567890abcdefABCDEF"

type fakeDispatcher struct {
	mu     sync.Mutex
	jobs   []Job
	accept bool
}

func (f *fakeDispatcher) Submit(j Job) SubmitResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs = append(f.jobs, j)
	if !f.accept {
		return SubmitFull
	}
	return SubmitQueued
}

func (f *fakeDispatcher) submitted() []Job {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Job(nil), f.jobs...)
}

func sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func prPayload(action, owner, repo string, number int, draft bool) []byte {
	return []byte(fmt.Sprintf(`{
		"action": %q,
		"number": %d,
		"pull_request": {"number": %d, "draft": %t},
		"repository": {"name": %q, "owner": {"login": %q}}
	}`, action, number, number, draft, repo, owner))
}

func newTestServer(t *testing.T, disp Dispatcher, logBuf io.Writer) *Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return newServer(Config{
		Addr:         ":0",
		Secret:       []byte(testSecret),
		Repos:        []string{"octocat/hello"},
		ResolveToken: func() (string, error) { return testToken, nil },
		Dispatcher:   disp,
		Logger:       log,
	})
}

func post(t *testing.T, srv *Server, event string, body []byte, sig string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if event != "" {
		req.Header.Set("X-GitHub-Event", event)
	}
	if sig != "" {
		req.Header.Set("X-Hub-Signature-256", sig)
	}
	req.Header.Set("X-GitHub-Delivery", "test-delivery-1")
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, req)
	return rec
}

func TestWebhook_ValidOpened_Dispatches(t *testing.T) {
	disp := &fakeDispatcher{accept: true}
	var logBuf bytes.Buffer
	srv := newTestServer(t, disp, &logBuf)

	body := prPayload("opened", "octocat", "hello", 42, false)
	rec := post(t, srv, "pull_request", body, sign([]byte(testSecret), body))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	jobs := disp.submitted()
	if len(jobs) != 1 {
		t.Fatalf("submitted %d jobs, want 1", len(jobs))
	}
	if jobs[0].Ref != "octocat/hello#42" {
		t.Errorf("Ref = %q, want octocat/hello#42", jobs[0].Ref)
	}
	if jobs[0].Key != (prKey{Owner: "octocat", Repo: "hello", Number: 42}) {
		t.Errorf("Key = %+v", jobs[0].Key)
	}
	if jobs[0].Token != testToken {
		t.Errorf("Token not propagated to job")
	}
	assertNoSecrets(t, logBuf.String())
}

func TestWebhook_ActedActions(t *testing.T) {
	for _, action := range []string{"synchronize", "reopened", "ready_for_review"} {
		t.Run(action, func(t *testing.T) {
			disp := &fakeDispatcher{accept: true}
			srv := newTestServer(t, disp, io.Discard)
			body := prPayload(action, "octocat", "hello", 7, false)
			rec := post(t, srv, "pull_request", body, sign([]byte(testSecret), body))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if len(disp.submitted()) != 1 {
				t.Fatalf("want 1 dispatch for %q", action)
			}
		})
	}
}

func TestWebhook_BadSignature_401(t *testing.T) {
	disp := &fakeDispatcher{accept: true}
	srv := newTestServer(t, disp, io.Discard)
	body := prPayload("opened", "octocat", "hello", 1, false)
	rec := post(t, srv, "pull_request", body, "sha256=deadbeef")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if len(disp.submitted()) != 0 {
		t.Fatal("dispatched on bad signature")
	}
}

func TestWebhook_MissingSignature_401(t *testing.T) {
	disp := &fakeDispatcher{accept: true}
	srv := newTestServer(t, disp, io.Discard)
	body := prPayload("opened", "octocat", "hello", 1, false)
	rec := post(t, srv, "pull_request", body, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if len(disp.submitted()) != 0 {
		t.Fatal("dispatched on missing signature")
	}
}

func TestWebhook_OversizedBody_413(t *testing.T) {
	disp := &fakeDispatcher{accept: true}
	srv := newTestServer(t, disp, io.Discard)
	big := bytes.Repeat([]byte("a"), maxBodyBytes+1)
	body := append([]byte(`{"action":"opened","x":"`), big...)
	body = append(body, []byte(`"}`)...)
	rec := post(t, srv, "pull_request", body, sign([]byte(testSecret), body))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if len(disp.submitted()) != 0 {
		t.Fatal("dispatched on oversized body")
	}
}

func TestWebhook_NonPullRequestEvent_200NoDispatch(t *testing.T) {
	disp := &fakeDispatcher{accept: true}
	srv := newTestServer(t, disp, io.Discard)
	body := []byte(`{"zen":"keep it simple"}`)
	rec := post(t, srv, "ping", body, sign([]byte(testSecret), body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(disp.submitted()) != 0 {
		t.Fatal("dispatched on non-PR event")
	}
}

func TestWebhook_UnactedAction_200NoDispatch(t *testing.T) {
	disp := &fakeDispatcher{accept: true}
	srv := newTestServer(t, disp, io.Discard)
	body := prPayload("closed", "octocat", "hello", 9, false)
	rec := post(t, srv, "pull_request", body, sign([]byte(testSecret), body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(disp.submitted()) != 0 {
		t.Fatal("dispatched on unacted action")
	}
}

func TestWebhook_DraftOnOpen_200NoDispatch(t *testing.T) {
	disp := &fakeDispatcher{accept: true}
	srv := newTestServer(t, disp, io.Discard)
	body := prPayload("opened", "octocat", "hello", 3, true)
	rec := post(t, srv, "pull_request", body, sign([]byte(testSecret), body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(disp.submitted()) != 0 {
		t.Fatal("dispatched on draft-on-open")
	}
}

func TestWebhook_RepoNotInAllowlist_200NoDispatch(t *testing.T) {
	disp := &fakeDispatcher{accept: true}
	srv := newTestServer(t, disp, io.Discard)
	body := prPayload("opened", "attacker", "evil", 1, false)
	rec := post(t, srv, "pull_request", body, sign([]byte(testSecret), body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(disp.submitted()) != 0 {
		t.Fatal("dispatched for non-allowlisted repo")
	}
}

func TestWebhook_FullQueue_LoudNoSilentDrop(t *testing.T) {
	disp := &fakeDispatcher{accept: false} // queue full
	var logBuf bytes.Buffer
	srv := newTestServer(t, disp, &logBuf)
	body := prPayload("opened", "octocat", "hello", 5, false)
	rec := post(t, srv, "pull_request", body, sign([]byte(testSecret), body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(disp.submitted()) != 1 {
		t.Fatal("Submit not attempted")
	}
	if !strings.Contains(logBuf.String(), "queue full") {
		t.Errorf("expected loud drop log, got: %s", logBuf.String())
	}
}

func TestHealthz_200(t *testing.T) {
	srv := newTestServer(t, &fakeDispatcher{accept: true}, io.Discard)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestWebhook_NoSecretsInLog(t *testing.T) {
	disp := &fakeDispatcher{accept: true}
	var logBuf bytes.Buffer
	srv := newTestServer(t, disp, &logBuf)
	body := prPayload("opened", "octocat", "hello", 42, false)
	post(t, srv, "pull_request", body, sign([]byte(testSecret), body))
	assertNoSecrets(t, logBuf.String())
}

func TestWebhook_ReviewCommentDispatchesReply(t *testing.T) {
	disp := &fakeDispatcher{accept: true}
	srv := newTestServer(t, disp, io.Discard)
	body := []byte(`{
		"action": "created",
		"pull_request": {"number": 42},
		"comment": {"id": 9, "body": "Fixed in abcdef1: tightened the bound", "user": {"login": "dev"}, "in_reply_to_id": 8},
		"repository": {"name": "hello", "owner": {"login": "octocat"}}
	}`)
	rec := post(t, srv, "pull_request_review_comment", body, sign([]byte(testSecret), body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	jobs := disp.submitted()
	if len(jobs) != 1 || jobs[0].Kind != JobKindThreadReply || jobs[0].Reply == nil || jobs[0].Reply.CommentID != 9 {
		t.Fatalf("jobs = %+v", jobs)
	}
	if jobs[0].Ref != "octocat/hello#42" || jobs[0].Key.CommentID != 9 {
		t.Fatalf("job key = %+v ref=%s", jobs[0].Key, jobs[0].Ref)
	}
}

func TestWebhook_BotCommentIgnored(t *testing.T) {
	disp := &fakeDispatcher{accept: true}
	srv := newTestServer(t, disp, io.Discard)
	body := []byte(`{
		"action": "created",
		"pull_request": {"number": 42},
		"comment": {"id": 9, "body": "<!-- miu-cr-bot -->\nresolved", "user": {"login": "miucr"}, "in_reply_to_id": 8},
		"repository": {"name": "hello", "owner": {"login": "octocat"}}
	}`)
	rec := post(t, srv, "pull_request_review_comment", body, sign([]byte(testSecret), body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(disp.submitted()) != 0 {
		t.Fatal("bot comment dispatched")
	}
}

func TestWebhook_IssueCommentRequiresPullRequest(t *testing.T) {
	disp := &fakeDispatcher{accept: true}
	srv := newTestServer(t, disp, io.Discard)
	plain := []byte(`{
		"action": "created",
		"issue": {"number": 7},
		"comment": {"id": 3, "body": "Deferred: tracked in #4 because the change is safe", "user": {"login": "dev"}},
		"repository": {"name": "hello", "owner": {"login": "octocat"}}
	}`)
	rec := post(t, srv, "issue_comment", plain, sign([]byte(testSecret), plain))
	if rec.Code != http.StatusOK || len(disp.submitted()) != 0 {
		t.Fatalf("non-PR issue comment dispatched: status=%d jobs=%d", rec.Code, len(disp.submitted()))
	}
	pr := []byte(`{
		"action": "created",
		"issue": {"number": 7, "pull_request": {"url": "https://api.github.com/repos/octocat/hello/pulls/7"}},
		"comment": {"id": 4, "body": "Deferred: tracked in #4 because the change is safe", "user": {"login": "dev"}},
		"repository": {"name": "hello", "owner": {"login": "octocat"}}
	}`)
	rec = post(t, srv, "issue_comment", pr, sign([]byte(testSecret), pr))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	jobs := disp.submitted()
	if len(jobs) != 1 || jobs[0].Kind != JobKindThreadReply || jobs[0].Reply.Kind != "issue_comment" {
		t.Fatalf("jobs = %+v", jobs)
	}
}

func assertNoSecrets(t *testing.T, logOut string) {
	t.Helper()
	if strings.Contains(logOut, testSecret) {
		t.Errorf("webhook secret leaked into log: %s", logOut)
	}
	if strings.Contains(logOut, testToken) {
		t.Errorf("github token leaked into log: %s", logOut)
	}
}
