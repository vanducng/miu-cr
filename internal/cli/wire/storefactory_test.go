package wire

import (
	stdctx "context"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/cli"
	"github.com/vanducng/miu-cr/internal/config"
)

// unroutable DSN: TEST-NET-1 (RFC 5737) + closed port → bounded connect failure,
// no live server needed.
const badPGDSN = "postgres://user:secret@192.0.2.1:1/db?sslmode=disable&connect_timeout=2"

func TestResolveBackendPrecedence(t *testing.T) {
	cases := []struct {
		name string
		env  string
		cfg  string
		want string
	}{
		{"env wins", "postgres", "sqlite", "postgres"},
		{"config when env empty", "", "postgres", "postgres"},
		{"empty config -> sqlite", "", "", "sqlite"},
		{"default sqlite", "", "sqlite", "sqlite"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MIUCR_STORE_BACKEND", tc.env)
			got := resolveBackend(config.Config{Store: config.Store{Backend: tc.cfg}})
			if got != tc.want {
				t.Fatalf("resolveBackend = %q, want %q", got, tc.want)
			}
		})
	}
}

// validateBackend accepts the known backends (empty/unset -> sqlite) and rejects
// an unrecognized explicit value with a typed config.invalid CLIError that names
// the bad value, never a silent sqlite fallback.
func TestValidateBackend(t *testing.T) {
	cases := []struct {
		name    string
		cfg     string
		want    string
		wantErr bool
	}{
		{"empty -> sqlite", "", "sqlite", false},
		{"sqlite", "sqlite", "sqlite", false},
		{"postgres", "postgres", "postgres", false},
		{"bogus -> error", "postgre", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MIUCR_STORE_BACKEND", "")
			got, err := validateBackend(config.Config{Store: config.Store{Backend: tc.cfg}})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("validateBackend(%q) = %q, want error", tc.cfg, got)
				}
				var ce *cli.CLIError
				if ce2, ok := err.(*cli.CLIError); ok {
					ce = ce2
				} else {
					t.Fatalf("want *cli.CLIError, got %T: %v", err, err)
				}
				if ce.Code != "config.invalid" {
					t.Fatalf("code = %q, want config.invalid", ce.Code)
				}
				if !strings.Contains(ce.Message, tc.cfg) {
					t.Fatalf("message %q must name the bad value %q", ce.Message, tc.cfg)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateBackend(%q) unexpected error: %v", tc.cfg, err)
			}
			if got != tc.want {
				t.Fatalf("validateBackend(%q) = %q, want %q", tc.cfg, got, tc.want)
			}
		})
	}
}

// A backend=postgres open failure surfaces a typed, redacted store.unavailable
// from both factory entry points, never a panic, never a silent nil.
func TestOpenStorePostgresFailureSurfaces(t *testing.T) {
	t.Setenv("MIUCR_STORE_BACKEND", "postgres")
	t.Setenv("MIUCR_PG_DSN", badPGDSN)
	cfg := config.Config{}

	for _, tc := range []struct {
		name string
		open func() (func(), error)
	}{
		{"openStore", func() (func(), error) {
			s, c, err := openStore(stdctx.Background(), cfg)
			_ = s
			return c, err
		}},
		{"openPRThreadStore", func() (func(), error) {
			s, c, err := openPRThreadStore(stdctx.Background(), cfg)
			_ = s
			return c, err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			closer, err := tc.open()
			if closer != nil {
				closer()
			}
			if err == nil {
				t.Fatal("expected open failure for unroutable postgres")
			}
			assertUnavailableRedacted(t, err)
		})
	}
}

// newPRThreadStore: sqlite degrades to nil; postgres propagates store.unavailable.
func TestNewPRThreadStoreBackendBranch(t *testing.T) {
	t.Run("sqlite nil-degrade requires opt-in flag", func(t *testing.T) {
		// MIUCR_PR_STORE unset -> nil store, no error (stateless path).
		t.Setenv("MIUCR_PR_STORE", "")
		s, c, err := newPRThreadStore(stdctx.Background(), config.Config{Store: config.Store{Backend: "sqlite"}})
		if err != nil || s != nil || c != nil {
			t.Fatalf("unset opt-in must yield (nil,nil,nil), got s=%v c!=nil=%v err=%v", s, c != nil, err)
		}
	})

	t.Run("postgres propagates open failure", func(t *testing.T) {
		t.Setenv("MIUCR_PR_STORE", "1")
		t.Setenv("MIUCR_STORE_BACKEND", "postgres")
		t.Setenv("MIUCR_PG_DSN", badPGDSN)
		s, c, err := newPRThreadStore(stdctx.Background(), config.Config{})
		if c != nil {
			c()
		}
		if err == nil || s != nil {
			t.Fatalf("postgres open failure must propagate, got s=%v err=%v", s, err)
		}
		assertUnavailableRedacted(t, err)
	})
}

// The MCP-Serve path must surface a backend=postgres open failure (not the old
// silent swallow). Serve loads config internally; env forces the postgres choice.
func TestMCPServeSurfacesPostgresFailure(t *testing.T) {
	t.Setenv("MIUCR_STORE_BACKEND", "postgres")
	t.Setenv("MIUCR_PG_DSN", badPGDSN)

	err := mcpServerImpl{}.Serve(stdctx.Background(), cli.MCPRequest{
		Transport: "stdio",
		Version:   "test",
		In:        strings.NewReader(""),
		Out:       &strings.Builder{},
		Err:       &strings.Builder{},
	})
	if err == nil {
		t.Fatal("MCP-Serve must surface the postgres open failure, got nil")
	}
	assertUnavailableRedacted(t, err)
}

func assertUnavailableRedacted(t *testing.T, err error) {
	t.Helper()
	var ce *cli.CLIError
	if ce2, ok := err.(*cli.CLIError); ok {
		ce = ce2
	} else {
		t.Fatalf("want *cli.CLIError, got %T: %v", err, err)
	}
	if ce.Code != "store.unavailable" {
		t.Fatalf("code = %q, want store.unavailable", ce.Code)
	}
	if strings.Contains(ce.Message, "secret") {
		t.Fatalf("DSN secret leaked into error: %q", ce.Message)
	}
}
