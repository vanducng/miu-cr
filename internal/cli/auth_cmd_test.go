package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/vanducng/miu-cr/internal/config"
)

// allOAuthSecrets are the four secret fields whoami must never emit.
var allOAuthSecrets = []string{
	"access-token-LEAK",
	"refresh-token-LEAK",
	"id-token-LEAK",
	"api-key-LEAK",
}

func seedOAuth(t *testing.T) config.OAuthRecord {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	rec := config.OAuthRecord{
		Provider:     "openai",
		AccessToken:  "access-token-LEAK",
		RefreshToken: "refresh-token-LEAK",
		IDToken:      "id-token-LEAK",
		APIKey:       "api-key-LEAK",
		AccountID:    "acct_VISIBLE",
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	if err := config.SaveOAuth(rec); err != nil {
		t.Fatalf("seed oauth: %v", err)
	}
	return rec
}

func runWhoamiCmd(t *testing.T, pretty bool) string {
	t.Helper()
	prettyOutput = pretty
	t.Cleanup(func() { prettyOutput = false })
	var so bytes.Buffer
	if err := runWhoami(&so); err != nil {
		t.Fatalf("whoami: %v", err)
	}
	return so.String()
}

func runLogoutCmd(t *testing.T) string {
	t.Helper()
	prettyOutput = false
	var so bytes.Buffer
	if err := runLogout(&so); err != nil {
		t.Fatalf("logout: %v", err)
	}
	return so.String()
}

func TestWhoamiShowsIdentityNeverToken(t *testing.T) {
	seedOAuth(t)

	for _, pretty := range []bool{false, true} {
		out := runWhoamiCmd(t, pretty)
		if !strings.Contains(out, "acct_VISIBLE") {
			t.Errorf("pretty=%v: account_id missing from output: %s", pretty, out)
		}
		if !strings.Contains(out, "openai") {
			t.Errorf("pretty=%v: provider missing from output: %s", pretty, out)
		}
		for _, secret := range allOAuthSecrets {
			if strings.Contains(out, secret) {
				t.Errorf("pretty=%v: secret %q leaked into whoami output", pretty, secret)
			}
		}
	}
}

func TestWhoamiNoRecordNotLoggedIn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	out := runWhoamiCmd(t, false)
	env := decodeEnvelope(t, []byte(out))
	if !env.OK || env.Kind != "whoami" {
		t.Fatalf("envelope ok=%v kind=%q", env.OK, env.Kind)
	}
	data, _ := env.Data.(map[string]any)
	if data["logged_in"] != false {
		t.Errorf("logged_in = %v, want false", data["logged_in"])
	}
}

func TestLogoutIdempotentAndClears(t *testing.T) {
	seedOAuth(t)

	// First logout removes the record.
	out := runLogoutCmd(t)
	env := decodeEnvelope(t, []byte(out))
	data, _ := env.Data.(map[string]any)
	if data["removed"] != true {
		t.Errorf("first logout removed = %v, want true", data["removed"])
	}

	// whoami now reports not-logged-in.
	whoOut := runWhoamiCmd(t, false)
	whoData, _ := decodeEnvelope(t, []byte(whoOut)).Data.(map[string]any)
	if whoData["logged_in"] != false {
		t.Errorf("after logout logged_in = %v, want false", whoData["logged_in"])
	}

	// Second logout is idempotent (no record → removed=false, no error).
	out2 := runLogoutCmd(t)
	data2, _ := decodeEnvelope(t, []byte(out2)).Data.(map[string]any)
	if data2["removed"] != false {
		t.Errorf("second logout removed = %v, want false", data2["removed"])
	}
}
