package oauth

import (
	stdctx "context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/vanducng/miu-cr/internal/config"
)

type casStore struct {
	mu  sync.Mutex
	rec config.OAuthRecord
	ok  bool
}

func (*casStore) Backend() string { return config.OAuthStorePostgres }

func (s *casStore) Load(stdctx.Context) (config.OAuthRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec, s.ok, nil
}

func (s *casStore) Save(_ stdctx.Context, rec config.OAuthRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rec, s.ok = rec, true
	return nil
}

func (s *casStore) SaveRefreshed(_ stdctx.Context, prior, next config.OAuthRecord) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ok || s.rec.RefreshToken != prior.RefreshToken {
		return false, nil
	}
	s.rec = next
	return true, nil
}

func (s *casStore) Delete(stdctx.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	was := s.ok
	s.ok = false
	return was, nil
}

func rotatingTokenServer(t *testing.T, onRefresh func()) *httptest.Server {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"access_token": "a1", "refresh_token": "r1", "expires_in": 3600})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if onRefresh != nil {
			onRefresh()
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCredentialRefreshPersistsRotationToStore(t *testing.T) {
	st := &casStore{}
	_ = st.Save(stdctx.Background(), config.OAuthRecord{Provider: "openai", AccessToken: "a0", RefreshToken: "r0", ExpiresAt: time.Now().Add(time.Minute)})
	srv := rotatingTokenServer(t, nil)

	res, ok, err := Credential(stdctx.Background(), st, metaFor(srv), srv.Client(), nil)
	if err != nil || !ok || res.AccessToken != "a1" {
		t.Fatalf("Credential: tok=%q ok=%v err=%v", res.AccessToken, ok, err)
	}
	if got, _, _ := st.Load(stdctx.Background()); got.RefreshToken != "r1" || got.AccessToken != "a1" {
		t.Fatalf("rotated token not persisted: %+v", got)
	}
}

func TestCredentialRefreshLosesToNewerLogin(t *testing.T) {
	st := &casStore{}
	_ = st.Save(stdctx.Background(), config.OAuthRecord{Provider: "openai", AccessToken: "a0", RefreshToken: "r0", ExpiresAt: time.Now().Add(time.Minute)})
	login := config.OAuthRecord{Provider: "openai", AccessToken: "login-a", RefreshToken: "login-r", ExpiresAt: time.Now().Add(time.Hour)}
	srv := rotatingTokenServer(t, func() { _ = st.Save(stdctx.Background(), login) })

	res, ok, err := Credential(stdctx.Background(), st, metaFor(srv), srv.Client(), nil)
	if err != nil || !ok || res.AccessToken != "a1" {
		t.Fatalf("Credential: tok=%q ok=%v err=%v", res.AccessToken, ok, err)
	}
	if got, _, _ := st.Load(stdctx.Background()); got != login {
		t.Fatalf("newer login must win, store holds %+v", got)
	}
}

func TestCredentialPicksUpLoginFromAnotherProcess(t *testing.T) {
	st := &casStore{}
	_ = st.Save(stdctx.Background(), config.OAuthRecord{Provider: "openai", AccessToken: "first", RefreshToken: "r0", ExpiresAt: time.Now().Add(time.Hour)})
	srv := rotatingTokenServer(t, func() { t.Error("fresh tokens must not refresh") })

	first, _, _ := Credential(stdctx.Background(), st, metaFor(srv), srv.Client(), nil)
	_ = st.Save(stdctx.Background(), config.OAuthRecord{Provider: "openai", AccessToken: "second", RefreshToken: "r9", ExpiresAt: time.Now().Add(time.Hour)})
	second, ok, err := Credential(stdctx.Background(), st, metaFor(srv), srv.Client(), nil)
	if err != nil || !ok || first.AccessToken != "first" || second.AccessToken != "second" {
		t.Fatalf("resolve must reread the store: first=%q second=%q ok=%v err=%v", first.AccessToken, second.AccessToken, ok, err)
	}
}

func TestCredentialRefreshLostRaceUsesStoredRotation(t *testing.T) {
	st := &casStore{}
	_ = st.Save(stdctx.Background(), config.OAuthRecord{Provider: "openai", AccessToken: "a0", RefreshToken: "r0", ExpiresAt: time.Now().Add(time.Minute)})
	other := config.OAuthRecord{Provider: "openai", AccessToken: "other-a1", RefreshToken: "other-r1", ExpiresAt: time.Now().Add(time.Hour)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = st.SaveRefreshed(stdctx.Background(), config.OAuthRecord{RefreshToken: "r0"}, other)
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	res, ok, err := Credential(stdctx.Background(), st, metaFor(srv), srv.Client(), nil)
	if err != nil || !ok || res.AccessToken != "other-a1" {
		t.Fatalf("lost grant race must reuse the stored rotation: tok=%q ok=%v err=%v", res.AccessToken, ok, err)
	}
	if got, _, _ := st.Load(stdctx.Background()); got != other {
		t.Fatalf("store must keep the other process's rotation, got %+v", got)
	}
}
