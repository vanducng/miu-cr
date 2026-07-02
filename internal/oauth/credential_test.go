package oauth

import (
	stdctx "context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vanducng/miu-cr/internal/config"
)

// fakeHome points config's home-derived paths (oauth.json) at a temp dir.
func fakeHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
}

func tokenServer(t *testing.T, body string, status int, hits *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("refresh Content-Type = %q, want application/json", ct)
		}
		raw, _ := io.ReadAll(r.Body)
		var got map[string]string
		_ = json.Unmarshal(raw, &got)
		if got["grant_type"] != "refresh_token" {
			t.Errorf("grant_type = %q", got["grant_type"])
		}
		if got["refresh_token"] == "" {
			t.Errorf("refresh_token missing from JSON body")
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
}

func metaFor(srv *httptest.Server) Meta {
	return Meta{Provider: "openai", TokenURL: srv.URL, ClientID: "app_test", BackendBaseURL: "https://backend.example/codex"}
}

func TestCredentialFreshNoRefresh(t *testing.T) {
	fakeHome(t)
	if err := config.SaveOAuth(config.OAuthRecord{
		Provider: "openai", AccessToken: "tok", RefreshToken: "ref",
		AccountID: "acct", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	hits := 0
	srv := tokenServer(t, "", 200, &hits)
	defer srv.Close()

	res, ok, err := Credential(stdctx.Background(), metaFor(srv), srv.Client(), nil)
	if err != nil || !ok {
		t.Fatalf("Credential: ok=%v err=%v", ok, err)
	}
	if res.AccessToken != "tok" || res.AccountID != "acct" {
		t.Errorf("resolved = %+v", res)
	}
	if res.BackendBaseURL != "https://backend.example/codex" {
		t.Errorf("backend = %q", res.BackendBaseURL)
	}
	if hits != 0 {
		t.Errorf("fresh token should not refresh, hits=%d", hits)
	}
}

// The shared singleflight refresh is detached from the caller's context, so a
// caller whose ctx is already cancelled (e.g. its review timed out) must not
// poison the grant for the collapsed callers that are still waiting.
func TestCredentialRefreshDetachesFromCallerCancel(t *testing.T) {
	fakeHome(t)
	if err := config.SaveOAuth(config.OAuthRecord{
		Provider: "openai", AccessToken: "old", RefreshToken: "ref-1",
		ExpiresAt: time.Now().Add(time.Minute), // within refreshSkew → triggers refresh
	}); err != nil {
		t.Fatal(err)
	}
	hits := 0
	body, _ := json.Marshal(map[string]any{"access_token": "new", "refresh_token": "ref-2", "expires_in": 3600})
	srv := tokenServer(t, string(body), 200, &hits)
	defer srv.Close()

	ctx, cancel := stdctx.WithCancel(stdctx.Background())
	cancel() // cancelled before the refresh runs
	res, ok, err := Credential(ctx, metaFor(srv), srv.Client(), nil)
	if err != nil || !ok {
		t.Fatalf("refresh must survive a cancelled caller ctx: ok=%v err=%v", ok, err)
	}
	if res.AccessToken != "new" {
		t.Errorf("access token = %q, want new", res.AccessToken)
	}
}

func TestCredentialRefreshesWhenExpiring(t *testing.T) {
	fakeHome(t)
	if err := config.SaveOAuth(config.OAuthRecord{
		Provider: "openai", AccessToken: "old", RefreshToken: "ref-1",
		ExpiresAt: time.Now().Add(time.Minute), // within refreshSkew
	}); err != nil {
		t.Fatal(err)
	}
	hits := 0
	body, _ := json.Marshal(map[string]any{"access_token": "new", "refresh_token": "ref-2", "expires_in": 3600})
	srv := tokenServer(t, string(body), 200, &hits)
	defer srv.Close()

	res, ok, err := Credential(stdctx.Background(), metaFor(srv), srv.Client(), nil)
	if err != nil || !ok {
		t.Fatalf("Credential: ok=%v err=%v", ok, err)
	}
	if res.AccessToken != "new" {
		t.Errorf("access token = %q, want new", res.AccessToken)
	}
	if hits != 1 {
		t.Errorf("expected one refresh, hits=%d", hits)
	}
	// SaveOAuth must have persisted the rotated refresh token.
	saved, _, _ := config.LoadOAuth()
	if saved.AccessToken != "new" || saved.RefreshToken != "ref-2" {
		t.Errorf("persisted record = %+v", saved)
	}
}

func TestCredentialRefreshClosureRotates(t *testing.T) {
	fakeHome(t)
	_ = config.SaveOAuth(config.OAuthRecord{
		Provider: "openai", AccessToken: "a0", RefreshToken: "r0",
		ExpiresAt: time.Now().Add(time.Hour),
	})
	hits := 0
	body, _ := json.Marshal(map[string]any{"access_token": "a1", "expires_in": 3600})
	srv := tokenServer(t, string(body), 200, &hits)
	defer srv.Close()

	res, ok, err := Credential(stdctx.Background(), metaFor(srv), srv.Client(), nil)
	if err != nil || !ok {
		t.Fatalf("Credential: %v", err)
	}
	tok, err := res.Refresh(stdctx.Background())
	if err != nil || tok != "a1" {
		t.Fatalf("Refresh = %q err=%v", tok, err)
	}
	if hits != 1 {
		t.Errorf("Refresh hits = %d", hits)
	}
}

// TestCredentialConcurrentRefreshNoRace exercises the Refresh closure from many
// goroutines; the sync.Mutex must serialize the rec read+reassign (run with -race).
func TestCredentialConcurrentRefreshNoRace(t *testing.T) {
	fakeHome(t)
	_ = config.SaveOAuth(config.OAuthRecord{
		Provider: "openai", AccessToken: "a0", RefreshToken: "r0",
		ExpiresAt: time.Now().Add(time.Hour),
	})
	hits := 0
	body, _ := json.Marshal(map[string]any{"access_token": "a1", "refresh_token": "r1", "expires_in": 3600})
	srv := tokenServer(t, string(body), 200, &hits)
	defer srv.Close()

	res, ok, err := Credential(stdctx.Background(), metaFor(srv), srv.Client(), nil)
	if err != nil || !ok {
		t.Fatalf("Credential: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if tok, e := res.Refresh(stdctx.Background()); e != nil || tok != "a1" {
				t.Errorf("Refresh = %q err=%v", tok, e)
			}
		}()
	}
	wg.Wait()
}

// TestRefreshUsesInjectedNow verifies ExpiresAt is computed from the injected now
// (deterministic) rather than the wall clock.
func TestRefreshUsesInjectedNow(t *testing.T) {
	fakeHome(t)
	_ = config.SaveOAuth(config.OAuthRecord{
		Provider: "openai", AccessToken: "old", RefreshToken: "ref",
		ExpiresAt: time.Now().Add(time.Minute), // within refreshSkew → triggers refresh
	})
	hits := 0
	body, _ := json.Marshal(map[string]any{"access_token": "new", "expires_in": 3600})
	srv := tokenServer(t, string(body), 200, &hits)
	defer srv.Close()

	fixed := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	_, ok, err := Credential(stdctx.Background(), metaFor(srv), srv.Client(), func() time.Time { return fixed })
	if err != nil || !ok {
		t.Fatalf("Credential: %v", err)
	}
	saved, _, _ := config.LoadOAuth()
	want := fixed.Add(3600 * time.Second)
	if !saved.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v (computed from injected now)", saved.ExpiresAt, want)
	}
}

func TestCredentialNoFileNotOK(t *testing.T) {
	fakeHome(t)
	hits := 0
	srv := tokenServer(t, "", 200, &hits)
	defer srv.Close()
	_, ok, err := Credential(stdctx.Background(), metaFor(srv), srv.Client(), nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if ok {
		t.Error("expected ok=false with no oauth.json")
	}
}

func TestCredentialWrongProviderNotOK(t *testing.T) {
	fakeHome(t)
	_ = config.SaveOAuth(config.OAuthRecord{Provider: "other", AccessToken: "x", ExpiresAt: time.Now().Add(time.Hour)})
	hits := 0
	srv := tokenServer(t, "", 200, &hits)
	defer srv.Close()
	_, ok, _ := Credential(stdctx.Background(), metaFor(srv), srv.Client(), nil)
	if ok {
		t.Error("expected ok=false for a different provider's record")
	}
}

func TestRefreshErrorRedactsAndNoLeak(t *testing.T) {
	fakeHome(t)
	_ = config.SaveOAuth(config.OAuthRecord{
		Provider: "openai", AccessToken: "old", RefreshToken: "secret-refresh",
		ExpiresAt: time.Now().Add(time.Minute),
	})
	hits := 0
	srv := tokenServer(t, "nope", http.StatusForbidden, &hits)
	defer srv.Close()
	_, _, err := Credential(stdctx.Background(), metaFor(srv), srv.Client(), nil)
	if err == nil {
		t.Fatal("expected refresh failure")
	}
	if strings.Contains(err.Error(), "secret-refresh") {
		t.Errorf("error leaked refresh token: %v", err)
	}
}
