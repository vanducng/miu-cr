package wire

import (
	"bytes"
	stdctx "context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
	stores := make([]config.OAuthStore, 8)
	var wg sync.WaitGroup
	for i := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, err := openOAuthStore(stdctx.Background())
			if err != nil {
				t.Errorf("open %d: %v", i, err)
			}
			stores[i] = st
		}()
	}
	wg.Wait()
	for i, st := range stores {
		if st != stores[0] || st == nil || st.Backend() != config.OAuthStorePostgres {
			t.Fatalf("store %d = %p, want the one shared postgres store %p", i, st, stores[0])
		}
	}
}

func TestOpenOAuthStoreSharedOpenIgnoresCallerCancel(t *testing.T) {
	dsn := os.Getenv("MIUCR_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("MIUCR_TEST_PG_DSN not set")
	}
	oauthStoreEnv(t, "postgres", dsn, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{5}, config.OAuthKeySize)))
	ctx, cancel := stdctx.WithCancel(stdctx.Background())
	cancel()
	st, err := openOAuthStore(ctx)
	if err != nil || st == nil {
		t.Fatalf("a cancelled first caller must not fail the shared open: %v", err)
	}
}

func TestOpenOAuthStoreCollapsesConcurrentFailedOpens(t *testing.T) {
	oauthStoreEnv(t, "postgres", badPGDSN, base64.StdEncoding.EncodeToString(make([]byte, config.OAuthKeySize)))
	start := time.Now()
	single, _ := openOAuthStore(stdctx.Background())
	one := time.Since(start)
	if single != nil {
		t.Fatal("unroutable DSN must not open")
	}
	start = time.Now()
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := openOAuthStore(stdctx.Background()); err == nil {
				t.Error("unroutable DSN must fail")
			}
		}()
	}
	wg.Wait()
	if four := time.Since(start); four > 2*one+time.Second {
		t.Fatalf("4 concurrent failed opens took %v vs %v for one; they must share one attempt", four, one)
	}
}

func TestOpenOAuthStoreBrokenConfigFailsUnlessEnvDecides(t *testing.T) {
	cases := []struct {
		name, backend, dsn, wantCode string
		wantFile                     bool
	}{
		{"no env fails loud", "", "", "config.invalid", false},
		{"env sqlite uses file", "sqlite", "", "", true},
		{"env postgres stays postgres", "postgres", badPGDSN, "oauth.key_missing", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oauthStoreEnv(t, tc.backend, tc.dsn, "")
			dir := filepath.Join(os.Getenv("HOME"), ".config", "miu", "cr")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[store\nbackend = "), 0o600); err != nil {
				t.Fatal(err)
			}
			st, err := openOAuthStore(stdctx.Background())
			if tc.wantFile {
				if _, ok := st.(config.FileOAuthStore); !ok || err != nil {
					t.Fatalf("got %T err=%v, want config.FileOAuthStore", st, err)
				}
				return
			}
			var ce *cli.CLIError
			if st != nil || !errors.As(err, &ce) || ce.Code != tc.wantCode {
				t.Fatalf("got %T err=%v, want %s", st, err, tc.wantCode)
			}
		})
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
