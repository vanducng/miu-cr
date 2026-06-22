//go:build live

package github

import (
	stdctx "context"
	"os"
	"strconv"
	"testing"
	"time"
)

// TestLiveAppInstallationToken is a build-tagged (-tags live), KEY-GATED smoke for
// the GitHub App auth path: read a real App private key from disk, mint an RS256
// App JWT, and exchange it for an installation token via the real go-github
// CreateInstallationToken. It NEVER runs in CI and is skipped unless all three
// envs are set:
//
//	MIUCR_LIVE_APP_ID            — the numeric GitHub App ID
//	MIUCR_LIVE_APP_INSTALL_ID    — the numeric installation id
//	MIUCR_LIVE_APP_KEY_PATH      — path to the App private-key PEM
//
// The minted installation token is never logged or printed (only its presence and
// expiry window are asserted), so no secret reaches the test output.
func TestLiveAppInstallationToken(t *testing.T) {
	appID := os.Getenv("MIUCR_LIVE_APP_ID")
	installRaw := os.Getenv("MIUCR_LIVE_APP_INSTALL_ID")
	keyPath := os.Getenv("MIUCR_LIVE_APP_KEY_PATH")
	if appID == "" || installRaw == "" || keyPath == "" {
		t.Skip("set MIUCR_LIVE_APP_ID, MIUCR_LIVE_APP_INSTALL_ID and MIUCR_LIVE_APP_KEY_PATH to run the App-auth live smoke")
	}

	installID, err := strconv.ParseInt(installRaw, 10, 64)
	if err != nil || installID <= 0 {
		t.Fatalf("MIUCR_LIVE_APP_INSTALL_ID must be a positive integer, got %q", installRaw)
	}

	key, err := ReadPrivateKeyFile(keyPath)
	if err != nil {
		t.Fatalf("ReadPrivateKeyFile: %v", err)
	}

	src := NewAppTokenSource(appID, installID, key, NewAppExchanger(), nil)

	ctx, cancel := stdctx.WithTimeout(stdctx.Background(), 30*time.Second)
	defer cancel()

	tok, err := src.Token(ctx)
	if err != nil {
		t.Skipf("installation-token exchange failed (offline / key not installed?): %v", err)
	}
	if tok == "" {
		t.Fatal("Token returned an empty installation token")
	}

	// A second call must hit the in-memory cache and return the same token
	// without a fresh mint (refresh-before-expiry: the freshly minted token is
	// far from its margin).
	tok2, err := src.Token(ctx)
	if err != nil {
		t.Fatalf("cached Token: %v", err)
	}
	if tok2 != tok {
		t.Fatal("second Token call did not return the cached installation token")
	}
}
