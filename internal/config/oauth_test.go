package config

import (
	"os"
	"strings"
	"testing"
	"time"
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
