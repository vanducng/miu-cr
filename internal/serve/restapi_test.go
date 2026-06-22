package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/store"
)

const testAPIToken = "miucr_FAKE_apibearer_0123456789"

// memStore is an in-memory ReviewStore for the REST tests.
type memStore struct {
	mu   sync.Mutex
	recs map[string]store.ReviewRecord
}

func newMemStore() *memStore { return &memStore{recs: map[string]store.ReviewRecord{}} }

func (m *memStore) UpsertReview(_ context.Context, rec store.ReviewRecord) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recs[rec.ID] = rec
	return rec.ID, nil
}

func (m *memStore) GetReview(_ context.Context, id string) (store.ReviewRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.recs[id]
	if !ok {
		return store.ReviewRecord{}, errors.New("not found")
	}
	return rec, nil
}

func newRESTServer(t *testing.T, disp Dispatcher, st ReviewStore, token string, now func() time.Time) *Server {
	t.Helper()
	return newServer(Config{
		Addr:          ":0",
		Secret:        []byte(testSecret),
		Repos:         []string{"octocat/hello"},
		ResolveToken:  func() (string, error) { return testToken, nil },
		Dispatcher:    disp,
		Logger:        nil,
		ReviewTimeout: 10 * time.Minute,
		APIToken:      []byte(token),
		ReviewStore:   st,
		Now:           now,
	})
}

func doREST(t *testing.T, srv *Server, method, path, auth string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, req)
	return rec
}

func TestREST_CreateReview_202ServerGenID(t *testing.T) {
	disp := &fakeDispatcher{accept: true}
	srv := newRESTServer(t, disp, newMemStore(), testAPIToken, nil)
	body := []byte(`{"owner":"octocat","repo":"hello","number":7}`)
	rec := doREST(t, srv, http.MethodPost, "/v1/reviews", "Bearer "+testAPIToken, body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	var env struct {
		OK         bool   `json:"ok"`
		APIVersion string `json:"api_version"`
		Data       struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !env.OK || env.APIVersion != "miucr.cli/v1" {
		t.Fatalf("envelope shape: %+v", env)
	}
	if env.Data.ID == "" || env.Data.Status != "pending" {
		t.Fatalf("want server-gen id + pending, got %+v", env.Data)
	}
	jobs := disp.submitted()
	if len(jobs) != 1 || jobs[0].ReviewID != env.Data.ID {
		t.Fatalf("job not enqueued with ReviewID; jobs=%+v", jobs)
	}
}

func TestREST_PendingThenDone_RoundTrip(t *testing.T) {
	st := newMemStore()
	// A dispatcher that, on submit, marks the review done via UpsertReview to
	// simulate the worker's final persist.
	disp := dispatcherFunc(func(j Job) bool {
		_, _ = st.UpsertReview(context.Background(), store.ReviewRecord{
			ID:       j.ReviewID,
			Status:   "done",
			Mode:     "pr",
			HeadSHA:  "abc",
			Findings: []engine.Finding{{File: "a.go", Line: 1, Severity: "high", Category: "bug", Rationale: "x"}},
			Stats:    map[string]any{"files_reviewed": float64(1)},
		})
		return true
	})
	srv := newRESTServer(t, disp, st, testAPIToken, nil)

	// Create.
	cr := doREST(t, srv, http.MethodPost, "/v1/reviews", "Bearer "+testAPIToken,
		[]byte(`{"owner":"octocat","repo":"hello","number":7}`))
	var crEnv struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(cr.Body.Bytes(), &crEnv)
	id := crEnv.Data.ID

	// Get → done (the fake dispatcher already flipped it).
	gr := doREST(t, srv, http.MethodGet, "/v1/reviews/"+id, "Bearer "+testAPIToken, nil)
	if gr.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body=%s", gr.Code, gr.Body.String())
	}
	if !strings.Contains(gr.Body.String(), `"status":"done"`) {
		t.Fatalf("GET body not done: %s", gr.Body.String())
	}
	// Whitelist: never RepoDir / head_sha / mode / repo_dir in the GET body.
	for _, banned := range []string{"repo_dir", "RepoDir", "head_sha", "HeadSHA"} {
		if strings.Contains(gr.Body.String(), banned) {
			t.Fatalf("GET body leaked %q: %s", banned, gr.Body.String())
		}
	}
	for _, want := range []string{`"id"`, `"status"`, `"created_at"`, `"findings"`, `"stats"`} {
		if !strings.Contains(gr.Body.String(), want) {
			t.Fatalf("GET body missing %s: %s", want, gr.Body.String())
		}
	}
}

func TestREST_Auth(t *testing.T) {
	srv := newRESTServer(t, &fakeDispatcher{accept: true}, newMemStore(), testAPIToken, nil)
	body := []byte(`{"owner":"octocat","repo":"hello","number":1}`)
	cases := []struct {
		name string
		auth string
	}{
		{"no bearer", ""},
		{"wrong bearer", "Bearer wrong-token"},
		{"no scheme", testAPIToken},
		{"empty bearer", "Bearer "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := doREST(t, srv, http.MethodPost, "/v1/reviews", c.auth, body)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
		})
	}
	// Case-insensitive scheme accepted.
	ok := doREST(t, srv, http.MethodPost, "/v1/reviews", "bEaReR "+testAPIToken, body)
	if ok.Code != http.StatusAccepted {
		t.Fatalf("case-insensitive scheme: status = %d, want 202", ok.Code)
	}
}

func TestREST_EmptyConfiguredToken_NoRoutes(t *testing.T) {
	// No API token → the /v1 routes are not registered → POST gets 404 (no route),
	// proving an empty configured token never authenticates as empty==empty.
	srv := newServer(Config{
		Addr:         ":0",
		Secret:       []byte(testSecret),
		Repos:        []string{"octocat/hello"},
		ResolveToken: func() (string, error) { return testToken, nil },
		Dispatcher:   &fakeDispatcher{accept: true},
		Logger:       nil,
		ReviewStore:  newMemStore(),
		// APIToken empty.
	})
	rec := doREST(t, srv, http.MethodPost, "/v1/reviews", "Bearer ", []byte(`{}`))
	if rec.Code == http.StatusAccepted {
		t.Fatalf("empty configured token authenticated an empty bearer (got 202)")
	}
	// /webhook + /healthz stay registered.
	hz := doREST(t, srv, http.MethodGet, "/healthz", "", nil)
	if hz.Code != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", hz.Code)
	}
}

func TestREST_OffAllowlist_403(t *testing.T) {
	srv := newRESTServer(t, &fakeDispatcher{accept: true}, newMemStore(), testAPIToken, nil)
	rec := doREST(t, srv, http.MethodPost, "/v1/reviews", "Bearer "+testAPIToken,
		[]byte(`{"owner":"attacker","repo":"evil","number":1}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestREST_UnknownID_404(t *testing.T) {
	srv := newRESTServer(t, &fakeDispatcher{accept: true}, newMemStore(), testAPIToken, nil)
	rec := doREST(t, srv, http.MethodGet, "/v1/reviews/nope", "Bearer "+testAPIToken, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestREST_WrongMethod_405(t *testing.T) {
	srv := newRESTServer(t, &fakeDispatcher{accept: true}, newMemStore(), testAPIToken, nil)
	// PUT /v1/reviews is not registered (only POST) → Go 1.22+ mux returns 405.
	req := httptest.NewRequest(http.MethodPut, "/v1/reviews", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+testAPIToken)
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestREST_OversizedBody_413(t *testing.T) {
	srv := newRESTServer(t, &fakeDispatcher{accept: true}, newMemStore(), testAPIToken, nil)
	big := bytes.Repeat([]byte("a"), maxAPIBodyBytes+1)
	body := append([]byte(`{"owner":"octocat","repo":"hello","number":1,"x":"`), big...)
	body = append(body, []byte(`"}`)...)
	rec := doREST(t, srv, http.MethodPost, "/v1/reviews", "Bearer "+testAPIToken, body)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%s", rec.Code, rec.Body.String())
	}
}

func TestREST_StuckPending_GETRecoversToFailed(t *testing.T) {
	st := newMemStore()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, _ = st.UpsertReview(context.Background(), store.ReviewRecord{
		ID: "stuck-1", Status: "pending", Mode: "pr", CreatedAt: created,
	})
	// now is well past created+reviewTO (10m).
	now := func() time.Time { return created.Add(time.Hour) }
	srv := newRESTServer(t, &fakeDispatcher{accept: true}, st, testAPIToken, now)
	rec := doREST(t, srv, http.MethodGet, "/v1/reviews/stuck-1", "Bearer "+testAPIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"failed"`) {
		t.Fatalf("stuck pending not recovered to failed: %s", rec.Body.String())
	}
	got, _ := st.GetReview(context.Background(), "stuck-1")
	if got.Status != "failed" {
		t.Fatalf("store not updated to failed: %q", got.Status)
	}
}

func TestREST_BadJSON_400(t *testing.T) {
	srv := newRESTServer(t, &fakeDispatcher{accept: true}, newMemStore(), testAPIToken, nil)
	rec := doREST(t, srv, http.MethodPost, "/v1/reviews", "Bearer "+testAPIToken, []byte(`{not json`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// dispatcherFunc adapts a func to Dispatcher.
type dispatcherFunc func(Job) bool

func (f dispatcherFunc) Submit(j Job) bool { return f(j) }
