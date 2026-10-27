package cli

import (
	stdctx "context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vanducng/miu-cr/internal/config"
)

type memOAuthStore struct {
	mu  sync.Mutex
	rec config.OAuthRecord
	ok  bool
}

func (*memOAuthStore) Backend() string { return config.OAuthStorePostgres }

func (m *memOAuthStore) Load(stdctx.Context) (config.OAuthRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rec, m.ok, nil
}

func (m *memOAuthStore) Save(_ stdctx.Context, rec config.OAuthRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rec, m.ok = rec, true
	return nil
}

func (m *memOAuthStore) SaveRefreshed(ctx stdctx.Context, _, next config.OAuthRecord) (bool, error) {
	return true, m.Save(ctx, next)
}

func (m *memOAuthStore) Delete(stdctx.Context) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	was := m.ok
	m.rec, m.ok = config.OAuthRecord{}, false
	return was, nil
}

func useOAuthStore(t *testing.T, st config.OAuthStore, err error) {
	t.Helper()
	prev := oauthStoreFactory
	SetOAuthStoreFactory(func(stdctx.Context) (config.OAuthStore, error) { return st, err })
	t.Cleanup(func() { SetOAuthStoreFactory(prev) })
}

func TestLoginFileStoreReportsPath(t *testing.T) {
	isolateLoginEnv(t)
	auth := fakeAuthServer(t)
	browser := func(authURL string) error {
		u, _ := url.Parse(authURL)
		return hitCallback(t, u.Query().Get("state"), "fake-code")(authURL)
	}
	stdout, stderr, err := runLoginCmd(t, browser, "--provider", "openai", "--base-url", auth.URL, "--port", "1455")
	if err != nil {
		t.Fatalf("login: %v\nstderr=%s", err, stderr)
	}
	data, _ := decodeEnvelope(t, []byte(stdout)).Data.(map[string]any)
	wantPath, _ := config.OAuthPath()
	if data["oauth_store"] != "file" || data["oauth_path"] != wantPath {
		t.Fatalf("store fields = %v / %v, want file / %s", data["oauth_store"], data["oauth_path"], wantPath)
	}
	if !strings.Contains(stderr, "token cached at "+wantPath) {
		t.Errorf("stderr must name the file path:\n%s", stderr)
	}
}

func TestLoginPostgresStoreReportsNoFilePath(t *testing.T) {
	isolateLoginEnv(t)
	st := &memOAuthStore{}
	useOAuthStore(t, st, nil)
	auth := fakeAuthServer(t)
	browser := func(authURL string) error {
		u, _ := url.Parse(authURL)
		return hitCallback(t, u.Query().Get("state"), "fake-code")(authURL)
	}
	stdout, stderr, err := runLoginCmd(t, browser, "--provider", "openai", "--base-url", auth.URL, "--port", "1455")
	if err != nil {
		t.Fatalf("login: %v\nstderr=%s", err, stderr)
	}
	data, _ := decodeEnvelope(t, []byte(stdout)).Data.(map[string]any)
	if data["oauth_store"] != "postgres" {
		t.Errorf("oauth_store = %v, want postgres", data["oauth_store"])
	}
	if _, has := data["oauth_path"]; has {
		t.Errorf("postgres login must not report a file path: %v", data["oauth_path"])
	}
	if !strings.Contains(stderr, "stored encrypted in postgres") || strings.Contains(stderr, "oauth.json") {
		t.Errorf("stderr must name the postgres store, not a file:\n%s", stderr)
	}
	if rec, ok, _ := st.Load(stdctx.Background()); !ok || rec.RefreshToken != "fake-refresh-token-SECRET" {
		t.Errorf("store did not receive the login record: ok=%v", ok)
	}
	for _, secret := range []string{"fake-access-token-SECRET", "fake-refresh-token-SECRET"} {
		if strings.Contains(stdout+stderr, secret) {
			t.Errorf("secret %q leaked", secret)
		}
	}
}

func TestLoginStoreErrorFailsBeforeBrowser(t *testing.T) {
	isolateLoginEnv(t)
	useOAuthStore(t, nil, config.OAuthKeyError("oauth.key_missing", "MIUCR_OAUTH_KEY is not set"))
	opened := false
	_, _, err := runLoginCmd(t, func(string) error { opened = true; return nil }, "--provider", "openai", "--port", "1455")
	var ce *CLIError
	if !errors.As(err, &ce) || ce.Code != "oauth.key_missing" {
		t.Fatalf("err = %v, want oauth.key_missing", err)
	}
	if opened {
		t.Error("login must fail on the store before starting the browser flow")
	}
}

func TestWhoamiLogoutPostgresStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := &memOAuthStore{}
	_ = st.Save(stdctx.Background(), config.OAuthRecord{
		Provider: "openai", AccessToken: "access-token-LEAK", RefreshToken: "refresh-token-LEAK",
		AccountID: "acct_VISIBLE", ExpiresAt: time.Now().Add(time.Hour),
	})
	useOAuthStore(t, st, nil)

	out := runWhoamiCmd(t, false)
	data, _ := decodeEnvelope(t, []byte(out)).Data.(map[string]any)
	if data["logged_in"] != true || data["oauth_store"] != "postgres" || data["account_id"] != "acct_VISIBLE" {
		t.Fatalf("whoami data = %v", data)
	}
	if _, has := data["oauth_path"]; has {
		t.Errorf("postgres whoami must not report a file path")
	}
	if strings.Contains(out, "LEAK") {
		t.Errorf("whoami leaked a token: %s", out)
	}

	first, _ := decodeEnvelope(t, []byte(runLogoutCmd(t))).Data.(map[string]any)
	second, _ := decodeEnvelope(t, []byte(runLogoutCmd(t))).Data.(map[string]any)
	if first["removed"] != true || second["removed"] != false || first["oauth_store"] != "postgres" {
		t.Fatalf("logout = %v then %v", first, second)
	}
}
