package wire

import (
	stdctx "context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/cli"
	"github.com/vanducng/miu-cr/internal/config"
)

func oauthStoreEnv(t *testing.T, backend, dsn, key string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MIUCR_STORE_BACKEND", backend)
	t.Setenv("MIUCR_PG_DSN", dsn)
	t.Setenv(config.OAuthKeyEnv, key)
}

func TestOpenOAuthStoreDefaultsToFile(t *testing.T) {
	for _, backend := range []string{"", "sqlite"} {
		oauthStoreEnv(t, backend, "", "")
		st, err := openOAuthStore(stdctx.Background())
		if err != nil {
			t.Fatalf("backend %q: %v", backend, err)
		}
		if _, ok := st.(config.FileOAuthStore); !ok {
			t.Fatalf("backend %q: got %T, want config.FileOAuthStore", backend, st)
		}
	}
}

func TestOpenOAuthStorePostgresRequiresKeyAndDSN(t *testing.T) {
	short := base64.StdEncoding.EncodeToString([]byte("sixteen-byte-key"))
	cases := []struct {
		name, dsn, key, wantCode string
	}{
		{"no dsn", "", short, "config.invalid"},
		{"missing key", badPGDSN, "", "oauth.key_missing"},
		{"wrong key size", badPGDSN, short, "oauth.key_invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oauthStoreEnv(t, "postgres", tc.dsn, tc.key)
			_, err := openOAuthStore(stdctx.Background())
			var ce *cli.CLIError
			if !errors.As(err, &ce) || ce.Code != tc.wantCode {
				t.Fatalf("err = %v, want %s", err, tc.wantCode)
			}
			if strings.Contains(ce.Message, "secret") {
				t.Fatalf("error leaked the DSN password: %q", ce.Message)
			}
		})
	}
}

func TestOpenOAuthStorePostgresIsSharedPerProcess(t *testing.T) {
	dsn := os.Getenv("MIUCR_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("MIUCR_TEST_PG_DSN not set")
	}
	oauthStoreEnv(t, "postgres", dsn, base64.StdEncoding.EncodeToString(make([]byte, config.OAuthKeySize)))
	a, err := openOAuthStore(stdctx.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, err := openOAuthStore(stdctx.Background())
	if err != nil {
		t.Fatal(err)
	}
	if a != b || a.Backend() != config.OAuthStorePostgres {
		t.Fatalf("want one shared postgres store, got %p (%s) and %p", a, a.Backend(), b)
	}
}

func TestOpenOAuthStoreIgnoresBrokenConfigOnFileBackend(t *testing.T) {
	oauthStoreEnv(t, "", "", "")
	dir := filepath.Join(os.Getenv("HOME"), ".config", "miu", "cr")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[store\nbackend = "), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := openOAuthStore(stdctx.Background())
	if err != nil {
		t.Fatalf("broken config.toml must not block the file store: %v", err)
	}
	if _, ok := st.(config.FileOAuthStore); !ok {
		t.Fatalf("got %T, want config.FileOAuthStore", st)
	}
}

func TestOAuthResolverUsesConfiguredStore(t *testing.T) {
	oauthStoreEnv(t, "postgres", badPGDSN, "")
	_, _, err := oauthResolver()(stdctx.Background())
	var ce *cli.CLIError
	if !errors.As(err, &ce) || ce.Code != "oauth.key_missing" {
		t.Fatalf("resolver must go through the postgres oauth store, err = %v", err)
	}
}
