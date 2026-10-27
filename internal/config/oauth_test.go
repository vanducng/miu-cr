package config

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
)

func TestSaveLoadOAuthRoundTrip(t *testing.T) {
	fakeHome(t)

	rec := OAuthRecord{
		Provider:     "openai",
		AccessToken:  "synthetic-access-token",
		RefreshToken: "synthetic-refresh-token",
		IDToken:      "synthetic-id-token",
		AccountID:    "acct-synthetic",
		APIKey:       "sk-synthetic-key",
		ExpiresAt:    time.Now().Add(time.Hour).UTC().Truncate(time.Second),
	}
	if err := SaveOAuth(rec); err != nil {
		t.Fatalf("SaveOAuth: %v", err)
	}

	got, ok, err := LoadOAuth()
	if err != nil {
		t.Fatalf("LoadOAuth: %v", err)
	}
	if !ok {
		t.Fatal("LoadOAuth: ok=false after save")
	}
	if got != rec {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, rec)
	}
}

func TestSaveOAuthPerms(t *testing.T) {
	fakeHome(t)

	if err := SaveOAuth(OAuthRecord{Provider: "openai", AccessToken: "x"}); err != nil {
		t.Fatalf("SaveOAuth: %v", err)
	}

	path, _ := OAuthPath()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("oauth.json perms: want 0600, got %o", fi.Mode().Perm())
	}
	dir, _ := Dir()
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("config dir perms: want 0700, got %o", di.Mode().Perm())
	}
}

func TestLoadOAuthMissing(t *testing.T) {
	fakeHome(t)

	rec, ok, err := LoadOAuth()
	if err != nil {
		t.Fatalf("LoadOAuth missing: unexpected err %v", err)
	}
	if ok {
		t.Fatal("LoadOAuth missing: want ok=false")
	}
	if rec != (OAuthRecord{}) {
		t.Fatalf("LoadOAuth missing: want zero record, got %+v", rec)
	}
}

func TestOAuthExpiry(t *testing.T) {
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name           string
		expires        time.Time
		wantExpired    bool
		within         time.Duration
		wantExpiringIn bool
	}{
		{"zero never expires", time.Time{}, false, time.Hour, false},
		{"future not expired", now.Add(time.Hour), false, time.Minute, false},
		{"past expired", now.Add(-time.Minute), true, time.Minute, true},
		{"expiring within window", now.Add(2 * time.Minute), false, 5 * time.Minute, true},
		{"outside window", now.Add(10 * time.Minute), false, 5 * time.Minute, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := OAuthRecord{ExpiresAt: c.expires}
			if got := rec.Expired(now); got != c.wantExpired {
				t.Fatalf("Expired: got %v want %v", got, c.wantExpired)
			}
			if got := rec.ExpiringWithin(c.within, now); got != c.wantExpiringIn {
				t.Fatalf("ExpiringWithin: got %v want %v", got, c.wantExpiringIn)
			}
		})
	}
}

func TestOAuthTokenRedacted(t *testing.T) {
	token := "Authorization: Bearer synthetic-access-token-12345"
	if got := RedactString(token); strings.Contains(got, "synthetic-access-token-12345") {
		t.Fatalf("token leaked through RedactString: %q", got)
	}
	if got := RedactString("access_token=synthetic-secret-value"); strings.Contains(got, "synthetic-secret-value") {
		t.Fatalf("token assignment leaked through RedactString: %q", got)
	}
}

func TestFileOAuthStoreUnchanged(t *testing.T) {
	fakeHome(t)
	ctx := context.Background()
	st := FileOAuthStore{}
	if st.Backend() != OAuthStoreFile {
		t.Fatalf("backend = %q", st.Backend())
	}
	prior := OAuthRecord{Provider: "openai", AccessToken: "a0", RefreshToken: "r0", ExpiresAt: time.Now().Add(time.Hour).UTC().Truncate(time.Second)}
	if err := st.Save(ctx, prior); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got, ok, err := LoadOAuth(); err != nil || !ok || got != prior {
		t.Fatalf("Save must write oauth.json: got %+v ok=%v err=%v", got, ok, err)
	}
	next := prior
	next.AccessToken, next.RefreshToken = "a1", "r1"
	if saved, err := st.SaveRefreshed(ctx, prior, next); err != nil || !saved {
		t.Fatalf("SaveRefreshed: saved=%v err=%v", saved, err)
	}
	if got, ok, err := st.Load(ctx); err != nil || !ok || got != next {
		t.Fatalf("Load after refresh = %+v ok=%v err=%v", got, ok, err)
	}
	path, _ := OAuthPath()
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("oauth.json stat = %v err=%v", fi, err)
	}
	if removed, err := st.Delete(ctx); err != nil || !removed {
		t.Fatalf("first Delete: removed=%v err=%v", removed, err)
	}
	if removed, err := st.Delete(ctx); err != nil || removed {
		t.Fatalf("second Delete must be a no-op: removed=%v err=%v", removed, err)
	}
	if _, ok, err := st.Load(ctx); err != nil || ok {
		t.Fatalf("Load after delete: ok=%v err=%v", ok, err)
	}
}

func TestOAuthKeyFromEnv(t *testing.T) {
	valid := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, OAuthKeySize))
	short := base64.StdEncoding.EncodeToString([]byte("sixteen-byte-key"))
	cases := []struct {
		name, value, wantCode string
	}{
		{"missing", "", "oauth.key_missing"},
		{"not base64", "not-base64-secret-value!!", "oauth.key_invalid"},
		{"wrong size", short, "oauth.key_invalid"},
		{"valid", valid, ""},
		{"valid with newline", valid + "\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(OAuthKeyEnv, tc.value)
			key, err := OAuthKeyFromEnv()
			if tc.wantCode == "" {
				if err != nil || len(key) != OAuthKeySize {
					t.Fatalf("key len=%d err=%v", len(key), err)
				}
				return
			}
			var ce *clierr.CLIError
			if !errors.As(err, &ce) || ce.Code != tc.wantCode {
				t.Fatalf("err = %v, want %s", err, tc.wantCode)
			}
			if !strings.Contains(ce.Hint, OAuthKeyEnv) {
				t.Errorf("hint must name %s: %q", OAuthKeyEnv, ce.Hint)
			}
			if tc.value != "" && strings.Contains(ce.Message+ce.Hint, strings.TrimSpace(tc.value)) {
				t.Errorf("error echoed the key value: %q", ce.Message)
			}
		})
	}
}
