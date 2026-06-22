package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeIDToken builds a 3-segment JWT whose payload carries chatgpt_account_id.
func fakeIDToken(accountID string) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(
		`{"https://api.openai.com/auth":{"chatgpt_account_id":"` + accountID + `"}}`))
	return hdr + "." + payload + ".sig"
}

// fakeAuthServer is an httptest token endpoint. It accepts the code exchange and
// the best-effort token-exchange grant, returning tokens for the former.
func fakeAuthServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		if r.Form.Get("grant_type") == "urn:ietf:params:oauth:grant-type:token-exchange" {
			_ = json.NewEncoder(w).Encode(map[string]string{"api_key": "sk-exchanged-FAKE"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "fake-access-token-SECRET",
			"refresh_token": "fake-refresh-token-SECRET",
			"id_token":      fakeIDToken("acct_FAKE123"),
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// hitCallback parses the authorize URL and fires the loopback callback with the
// given state and a fake code, emulating the browser redirect.
func hitCallback(t *testing.T, state, code string) func(string) error {
	t.Helper()
	return func(authURL string) error {
		u, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		redirect := u.Query().Get("redirect_uri")
		go func() {
			cb := redirect + "?state=" + url.QueryEscape(state) + "&code=" + url.QueryEscape(code)
			resp, err := http.Get(cb)
			if err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
}

func runLoginCmd(t *testing.T, browser func(string) error, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	prettyOutput = false
	orig := browserOpen
	browserOpen = browser
	t.Cleanup(func() { browserOpen = orig })

	cmd := loginCommand(&options{output: "json"})
	var so, se bytes.Buffer
	cmd.SetOut(&so)
	cmd.SetErr(&se)
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err = cmd.Execute()
	return so.String(), se.String(), err
}

func isolateLoginEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func TestLoginPKCEFlowWritesTokenSecretFreeEnvelope(t *testing.T) {
	isolateLoginEnv(t)
	auth := fakeAuthServer(t)

	// state is generated inside runLogin; capture it by intercepting the authorize URL.
	var captured string
	browser := func(authURL string) error {
		u, _ := url.Parse(authURL)
		captured = u.Query().Get("state")
		return hitCallback(t, captured, "fake-code")(authURL)
	}

	stdout, stderr, err := runLoginCmd(t, browser, "--provider", "openai", "--base-url", auth.URL, "--port", "1455")
	if err != nil {
		t.Fatalf("login: %v\nstderr=%s", err, stderr)
	}

	env := decodeEnvelope(t, []byte(stdout))
	if !env.OK || env.Kind != "login.result" {
		t.Fatalf("envelope ok=%v kind=%q", env.OK, env.Kind)
	}
	data, _ := env.Data.(map[string]any)
	if data["provider"] != "openai" {
		t.Errorf("provider = %v", data["provider"])
	}
	if data["account_id"] != "acct_FAKE123" {
		t.Errorf("account_id = %v, want acct_FAKE123", data["account_id"])
	}
	if data["has_api_key"] != true {
		t.Errorf("has_api_key = %v, want true", data["has_api_key"])
	}

	// No token strings anywhere in stdout or stderr.
	for _, secret := range []string{"fake-access-token-SECRET", "fake-refresh-token-SECRET", "sk-exchanged-FAKE"} {
		if strings.Contains(stdout, secret) {
			t.Errorf("secret %q leaked into envelope", secret)
		}
		if strings.Contains(stderr, secret) {
			t.Errorf("secret %q leaked into stderr", secret)
		}
	}

	// oauth.json written, 0600, and carries the tokens (only place they live).
	oauthPath := filepath.Join(os.Getenv("HOME"), ".config", "miu", "cr", "oauth.json")
	info, statErr := os.Stat(oauthPath)
	if statErr != nil {
		t.Fatalf("stat oauth.json: %v", statErr)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("oauth.json perm = %v, want 0600", info.Mode().Perm())
	}
	raw, _ := os.ReadFile(oauthPath)
	if !strings.Contains(string(raw), "fake-access-token-SECRET") {
		t.Errorf("oauth.json missing access token")
	}
}

func TestLoginStateMismatchRejected(t *testing.T) {
	isolateLoginEnv(t)
	auth := fakeAuthServer(t)

	// Browser fires the callback with a WRONG state.
	browser := hitCallback(t, "wrong-state", "fake-code")

	_, _, err := runLoginCmd(t, browser, "--provider", "openai", "--base-url", auth.URL, "--port", "1455")
	if err == nil {
		t.Fatal("expected state mismatch error")
	}
	var cliErr *CLIError
	if !errors.As(err, &cliErr) || cliErr.Code != "login.state_mismatch" {
		t.Fatalf("err code = %v, want login.state_mismatch", err)
	}
}

func TestLoginUnknownProviderRejected(t *testing.T) {
	isolateLoginEnv(t)
	for _, name := range []string{"anthropic", "bogus"} {
		_, _, err := runLoginCmd(t, func(string) error { return nil }, "--provider", name)
		var cliErr *CLIError
		if !errors.As(err, &cliErr) || cliErr.Code != "login.provider_unsupported" {
			t.Errorf("provider %q: err = %v, want login.provider_unsupported", name, err)
		}
	}
}

func TestLoginNoBrowserPrintsURLAndUsesRegistryPorts(t *testing.T) {
	isolateLoginEnv(t)
	auth := fakeAuthServer(t)

	prettyOutput = false
	orig := browserOpen
	browserOpen = func(string) error {
		t.Errorf("browserOpen must not run with --no-browser")
		return nil
	}
	t.Cleanup(func() { browserOpen = orig })

	cmd := loginCommand(&options{output: "json"})
	var so bytes.Buffer
	se := &syncBuffer{}
	cmd.SetOut(&so)
	cmd.SetErr(se)
	cmd.SetArgs([]string{"--provider", "openai", "--base-url", auth.URL, "--port", "1457", "--no-browser"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()

	state := waitForState(t, se)
	cb := "http://localhost:1457/auth/callback?state=" + url.QueryEscape(state) + "&code=fake-code"
	resp, getErr := http.Get(cb)
	if getErr != nil {
		t.Fatalf("callback GET: %v", getErr)
	}
	resp.Body.Close()

	if err := <-done; err != nil {
		t.Fatalf("login --no-browser: %v\nstderr=%s", err, se.String())
	}
	if !strings.Contains(se.String(), "/oauth/authorize") {
		t.Errorf("authorize URL not printed to stderr: %s", se.String())
	}
}

// syncBuffer is a goroutine-safe bytes.Buffer for capturing stderr while the
// command runs concurrently.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitForState polls stderr until the authorize URL appears, then extracts its
// state query param.
func waitForState(t *testing.T, se *syncBuffer) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(se.String(), "\n") {
			if !strings.Contains(line, "/oauth/authorize") {
				continue
			}
			if u, err := url.Parse(strings.TrimSpace(line)); err == nil {
				if s := u.Query().Get("state"); s != "" {
					return s
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("authorize URL with state never printed; stderr=%s", se.String())
	return ""
}
