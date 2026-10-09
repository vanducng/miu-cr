package github

import (
	stdctx "context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	gh "github.com/google/go-github/v84/github"
)

func testGHClient(t *testing.T, h http.Handler) ghClient {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	c := gh.NewClient(srv.Client())
	c.BaseURL = base
	return ghClient{c: c}
}

func TestCurrentLoginUsesAuthenticatedUser(t *testing.T) {
	var graphql atomic.Int32
	c := testGHClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/graphql" {
			graphql.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"login":"octocat"}`))
	}))
	login, err := c.CurrentLogin(stdctx.Background())
	if err != nil || login != "octocat" {
		t.Fatalf("login=%q err=%v", login, err)
	}
	if graphql.Load() != 0 {
		t.Fatal("GET /user success must not query the viewer")
	}
}

func TestCurrentLoginFallsBackToViewerForInstallationToken(t *testing.T) {
	c := testGHClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/user" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
			return
		}
		if r.URL.Path != "/graphql" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":{"viewer":{"login":"miucr[bot]"}}}`))
	}))
	login, err := c.CurrentLogin(stdctx.Background())
	if err != nil || login != "miucr[bot]" {
		t.Fatalf("login=%q err=%v", login, err)
	}
}

func TestCurrentLoginDoesNotHideUserAuthFailure(t *testing.T) {
	c := testGHClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/graphql" {
			t.Errorf("non-403 user error must not query the viewer")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	login, err := c.CurrentLogin(stdctx.Background())
	if err == nil || login != "" {
		t.Fatalf("login=%q err=%v, want user auth error", login, err)
	}
}

func TestCurrentLoginViewerErrorFailsClosed(t *testing.T) {
	c := testGHClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/user") {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"viewer":{"login":""}},"errors":[{"message":"viewer unavailable"}]}`))
	}))
	login, err := c.CurrentLogin(stdctx.Background())
	if err == nil || login != "" || !strings.Contains(err.Error(), "viewer unavailable") {
		t.Fatalf("login=%q err=%v", login, err)
	}
}
