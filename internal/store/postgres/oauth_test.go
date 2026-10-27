package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/oauth"
)

func testOAuthKey(b byte) []byte { return bytes.Repeat([]byte{b}, config.OAuthKeySize) }

func syntheticOAuthRecord() config.OAuthRecord {
	return config.OAuthRecord{
		Provider:     "openai",
		AccessToken:  "synthetic-access-token-SECRET",
		RefreshToken: "synthetic-refresh-token-SECRET",
		IDToken:      "synthetic-id-token-SECRET",
		AccountID:    "acct-synthetic",
		APIKey:       "sk-synthetic-SECRET",
		ExpiresAt:    time.Now().Add(time.Hour).UTC().Truncate(time.Second),
	}
}

func wantCLICode(t *testing.T, err error, code string) *clierr.CLIError {
	t.Helper()
	var ce *clierr.CLIError
	if !errors.As(err, &ce) || ce.Code != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
	if strings.Contains(ce.Message+ce.Hint, "SECRET") {
		t.Fatalf("error leaked a token: %q", ce.Message)
	}
	return ce
}

func TestOAuthStoreRejectsWrongKeySize(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33} {
		_, err := newOAuthStore(nil, make([]byte, n))
		wantCLICode(t, err, "oauth.key_invalid")
	}
}

func TestOAuthSealRoundTripAndBadKey(t *testing.T) {
	a, err := newOAuthStore(nil, testOAuthKey(1))
	if err != nil {
		t.Fatal(err)
	}
	b, err := newOAuthStore(nil, testOAuthKey(2))
	if err != nil {
		t.Fatal(err)
	}
	rec := syntheticOAuthRecord()
	sealed, err := a.seal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("SECRET")) {
		t.Fatal("sealed blob carries plaintext token bytes")
	}
	got, err := a.open(sealed)
	if err != nil || got != rec {
		t.Fatalf("open = %+v err=%v", got, err)
	}
	ce := wantCLICode(t, func() error { _, err := b.open(sealed); return err }(), "oauth.decrypt_failed")
	if !strings.Contains(ce.Hint, config.OAuthKeyEnv) {
		t.Errorf("decrypt hint must name %s: %q", config.OAuthKeyEnv, ce.Hint)
	}
	for _, bad := range [][]byte{nil, {oauthSealVersion}, append([]byte{9}, sealed[1:]...), sealed[:len(sealed)-1]} {
		_, err := a.open(bad)
		wantCLICode(t, err, "oauth.decrypt_failed")
	}
}

func openOAuth(t *testing.T, key []byte) *OAuthStore {
	t.Helper()
	dsn := os.Getenv("MIUCR_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("MIUCR_TEST_PG_DSN not set")
	}
	s, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	st, err := s.OAuthStore(key)
	if err != nil {
		t.Fatalf("OAuthStore: %v", err)
	}
	return st
}

func resetOAuth(t *testing.T, st *OAuthStore) {
	t.Helper()
	if _, err := st.db.ExecContext(context.Background(), `TRUNCATE oauth_credentials`); err != nil {
		t.Fatalf("truncate oauth_credentials: %v", err)
	}
}

func TestOAuthStorePostgresRoundTrip(t *testing.T) {
	st := openOAuth(t, testOAuthKey(1))
	resetOAuth(t, st)
	ctx := context.Background()

	if _, ok, err := st.Load(ctx); err != nil || ok {
		t.Fatalf("empty Load: ok=%v err=%v", ok, err)
	}
	rec := syntheticOAuthRecord()
	if err := st.Save(ctx, rec); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := st.Load(ctx)
	if err != nil || !ok || got != rec {
		t.Fatalf("Load = %+v ok=%v err=%v", got, ok, err)
	}

	var provider, account string
	var sealed []byte
	if err := st.db.QueryRowContext(ctx, `SELECT provider, account_id, sealed FROM oauth_credentials WHERE id = $1`, oauthCredentialID).Scan(&provider, &account, &sealed); err != nil {
		t.Fatal(err)
	}
	if provider != "openai" || account != "acct-synthetic" || bytes.Contains(sealed, []byte("SECRET")) {
		t.Fatalf("row = %q %q plaintext-free=%v", provider, account, !bytes.Contains(sealed, []byte("SECRET")))
	}
	var applied bool
	if err := st.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = '0007_oauth_credentials')`).Scan(&applied); err != nil || !applied {
		t.Fatalf("0007_oauth_credentials not recorded: %v", err)
	}

	if removed, err := st.Delete(ctx); err != nil || !removed {
		t.Fatalf("Delete: removed=%v err=%v", removed, err)
	}
	if removed, err := st.Delete(ctx); err != nil || removed {
		t.Fatalf("second Delete: removed=%v err=%v", removed, err)
	}
}

func TestOAuthStorePostgresWrongKeyDecryptFails(t *testing.T) {
	st := openOAuth(t, testOAuthKey(1))
	resetOAuth(t, st)
	if err := st.Save(context.Background(), syntheticOAuthRecord()); err != nil {
		t.Fatal(err)
	}
	other := openOAuth(t, testOAuthKey(2))
	_, ok, err := other.Load(context.Background())
	if ok {
		t.Fatal("wrong key must not yield a record")
	}
	wantCLICode(t, err, "oauth.decrypt_failed")
}

func TestOAuthStorePostgresSaveRefreshedCAS(t *testing.T) {
	st := openOAuth(t, testOAuthKey(1))
	resetOAuth(t, st)
	ctx := context.Background()
	prior := syntheticOAuthRecord()
	if err := st.Save(ctx, prior); err != nil {
		t.Fatal(err)
	}
	next := prior
	next.AccessToken, next.RefreshToken = "rotated-access", "rotated-refresh"

	for i := 0; i < 2; i++ {
		if saved, err := st.SaveRefreshed(ctx, prior, next); err != nil || !saved {
			t.Fatalf("SaveRefreshed attempt %d: saved=%v err=%v", i, saved, err)
		}
	}
	if got, _, _ := st.Load(ctx); got != next {
		t.Fatalf("Load after refresh = %+v", got)
	}

	login := syntheticOAuthRecord()
	login.AccessToken, login.RefreshToken = "login-access", "login-refresh"
	if err := st.Save(ctx, login); err != nil {
		t.Fatal(err)
	}
	stale := next
	stale.AccessToken, stale.RefreshToken = "stale-access", "stale-refresh"
	if saved, err := st.SaveRefreshed(ctx, next, stale); err != nil || saved {
		t.Fatalf("refresh over a newer login: saved=%v err=%v", saved, err)
	}
	if got, _, _ := st.Load(ctx); got != login {
		t.Fatalf("newer login must win, got %+v", got)
	}

	if _, err := st.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if saved, err := st.SaveRefreshed(ctx, login, stale); err != nil || saved {
		t.Fatalf("refresh after logout: saved=%v err=%v", saved, err)
	}
	if _, ok, _ := st.Load(ctx); ok {
		t.Fatal("refresh after logout must not resurrect the row")
	}
}

func TestOAuthSaveRefreshedStopsAtDeadline(t *testing.T) {
	db, err := sql.Open("pgx", "postgres://user:secret@127.0.0.1:1/db?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	st, err := newOAuthStore(db, testOAuthKey(1))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	start := time.Now()
	saved, err := st.SaveRefreshed(ctx, syntheticOAuthRecord(), syntheticOAuthRecord())
	if saved || time.Since(start) > 5*time.Second {
		t.Fatalf("saved=%v after %v", saved, time.Since(start))
	}
	ce := wantCLICode(t, err, "store.unavailable")
	if !strings.Contains(ce.Message, "attempts") || strings.Contains(ce.Message, "secret") {
		t.Fatalf("message = %q", ce.Message)
	}
}

func TestOAuthStorePostgresRotationSurvivesRestart(t *testing.T) {
	loginProc := openOAuth(t, testOAuthKey(1))
	resetOAuth(t, loginProc)
	ctx := context.Background()
	expiring := syntheticOAuthRecord()
	expiring.ExpiresAt = time.Now().Add(time.Minute)
	if err := loginProc.Save(ctx, expiring); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["refresh_token"] != expiring.RefreshToken {
			t.Errorf("refresh used a stale token")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "rotated-access", "refresh_token": "rotated-refresh", "expires_in": 3600})
	}))
	defer srv.Close()
	meta := oauth.Meta{Provider: "openai", TokenURL: srv.URL, ClientID: "app_test", BackendBaseURL: "https://backend.example/codex"}

	server := openOAuth(t, testOAuthKey(1))
	res, ok, err := oauth.Credential(ctx, server, meta, srv.Client(), nil)
	if err != nil || !ok || res.AccessToken != "rotated-access" {
		t.Fatalf("Credential: tok=%q ok=%v err=%v", res.AccessToken, ok, err)
	}

	restarted := openOAuth(t, testOAuthKey(1))
	got, ok, err := restarted.Load(ctx)
	if err != nil || !ok || got.RefreshToken != "rotated-refresh" || got.AccessToken != "rotated-access" {
		t.Fatalf("rotated token lost across restart: %+v ok=%v err=%v", got, ok, err)
	}

	relogin := syntheticOAuthRecord()
	relogin.AccessToken = "relogin-access"
	if err := loginProc.Save(ctx, relogin); err != nil {
		t.Fatal(err)
	}
	res, ok, err = oauth.Credential(ctx, server, meta, srv.Client(), nil)
	if err != nil || !ok || res.AccessToken != "relogin-access" {
		t.Fatalf("running server must pick up a login from another process: tok=%q ok=%v err=%v", res.AccessToken, ok, err)
	}
}
