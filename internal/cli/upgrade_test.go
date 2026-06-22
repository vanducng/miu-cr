package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	stdctx "context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeTarGz(t *testing.T, binName string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	if err := tw.WriteHeader(&tar.Header{Name: binName, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	return buf.Bytes()
}

func makeZip(t *testing.T, binName string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(binName)
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// releaseServer fakes the GitHub releases API + asset/checksums download for a
// single tag. badSum corrupts the checksums entry to drive checksum_mismatch.
func releaseServer(t *testing.T, tag, asset string, archive []byte, badSum bool) (*httptest.Server, *int, *string) {
	t.Helper()
	sum := sha256Hex(archive)
	if badSum {
		sum = strings.Repeat("0", 64)
	}
	checksums := sum + "  " + asset + "\n"
	downloads := 0
	var seenAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if a := r.Header.Get("Authorization"); a != "" {
			seenAuth = a
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			_, _ = w.Write([]byte(`{"tag_name":"` + tag + `"}`))
		case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
			downloads++
			_, _ = w.Write([]byte(checksums))
		case strings.HasSuffix(r.URL.Path, "/"+asset):
			downloads++
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &downloads, &seenAuth
}

func useBases(t *testing.T, base string) {
	t.Helper()
	oa, od := githubAPIBase, githubDownloadBase
	githubAPIBase, githubDownloadBase = base, base
	t.Cleanup(func() { githubAPIBase, githubDownloadBase = oa, od })
}

func envData(t *testing.T, env Envelope) map[string]any {
	t.Helper()
	m, ok := env.Data.(map[string]any)
	if !ok {
		t.Fatalf("data is not an object: %#v", env.Data)
	}
	return m
}

func tempExe(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "miucr")
	if err := os.WriteFile(p, []byte("OLD-BINARY"), 0o755); err != nil {
		t.Fatalf("seed exe: %v", err)
	}
	return p
}

func TestUpgradeCheckDetectsNewer(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "secret-tok")
	srv, downloads, auth := releaseServer(t, "v9.9.9", "miucr_linux_x86_64.tar.gz", []byte("x"), false)
	useBases(t, srv.URL)

	var out bytes.Buffer
	exe := tempExe(t)
	err := runUpgrade(stdctx.Background(), srv.Client(), &out, &bytes.Buffer{},
		upgradeOpts{check: true}, upgradeEnv{current: "v0.13.0", goos: "linux", goarch: "amd64", execPath: exe})
	if err != nil {
		t.Fatalf("runUpgrade: %v", err)
	}
	env := decodeEnvelope(t, out.Bytes())
	if !env.OK || env.Kind != "upgrade.result" {
		t.Fatalf("unexpected envelope: %+v", env)
	}
	d := envData(t, env)
	if d["action"] != "check_only" || d["update_available"] != true {
		t.Fatalf("expected check_only + update_available: %#v", d)
	}
	if *downloads != 0 {
		t.Fatalf("check must not download, got %d", *downloads)
	}
	if *auth != "Bearer secret-tok" {
		t.Fatalf("API request missing bearer token, got %q", *auth)
	}
	if strings.Contains(out.String(), "secret-tok") {
		t.Fatalf("token leaked into envelope: %s", out.String())
	}
	if b, _ := os.ReadFile(exe); string(b) != "OLD-BINARY" {
		t.Fatalf("check must not replace the binary")
	}
}

func TestUpgradeDownloadVerifyReplace(t *testing.T) {
	newBin := []byte("NEW-BINARY-CONTENT")
	asset := "miucr_linux_x86_64.tar.gz"
	archive := makeTarGz(t, "miucr", newBin)
	srv, _, _ := releaseServer(t, "v9.9.9", asset, archive, false)
	useBases(t, srv.URL)

	var out bytes.Buffer
	exe := tempExe(t)
	err := runUpgrade(stdctx.Background(), srv.Client(), &out, &bytes.Buffer{},
		upgradeOpts{}, upgradeEnv{current: "v0.13.0", goos: "linux", goarch: "amd64", execPath: exe})
	if err != nil {
		t.Fatalf("runUpgrade: %v", err)
	}
	env := decodeEnvelope(t, out.Bytes())
	d := envData(t, env)
	if d["action"] != "upgraded" || d["to_version"] != "v9.9.9" || d["asset"] != asset {
		t.Fatalf("unexpected result: %#v", d)
	}
	got, err := os.ReadFile(exe)
	if err != nil || !bytes.Equal(got, newBin) {
		t.Fatalf("binary not replaced: %v / %q", err, got)
	}
	info, err := os.Stat(exe)
	if err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("binary not executable: %v / %v", err, info.Mode())
	}
}

func TestUpgradeChecksumMismatch(t *testing.T) {
	asset := "miucr_linux_x86_64.tar.gz"
	archive := makeTarGz(t, "miucr", []byte("whatever"))
	srv, _, _ := releaseServer(t, "v9.9.9", asset, archive, true)
	useBases(t, srv.URL)

	exe := tempExe(t)
	err := runUpgrade(stdctx.Background(), srv.Client(), &bytes.Buffer{}, &bytes.Buffer{},
		upgradeOpts{}, upgradeEnv{current: "v0.13.0", goos: "linux", goarch: "amd64", execPath: exe})
	var cliErr *CLIError
	if !errors.As(err, &cliErr) || cliErr.Code != "upgrade.checksum_mismatch" {
		t.Fatalf("expected upgrade.checksum_mismatch, got %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "OLD-BINARY" {
		t.Fatalf("binary must not change on checksum mismatch")
	}
}

func TestUpgradeAlreadyLatest(t *testing.T) {
	srv, downloads, _ := releaseServer(t, "v9.9.9", "miucr_linux_x86_64.tar.gz", []byte("x"), false)
	useBases(t, srv.URL)

	var out bytes.Buffer
	exe := tempExe(t)
	err := runUpgrade(stdctx.Background(), srv.Client(), &out, &bytes.Buffer{},
		upgradeOpts{}, upgradeEnv{current: "v9.9.9", goos: "linux", goarch: "amd64", execPath: exe})
	if err != nil {
		t.Fatalf("runUpgrade: %v", err)
	}
	d := envData(t, decodeEnvelope(t, out.Bytes()))
	if d["action"] != "already_latest" {
		t.Fatalf("expected already_latest, got %#v", d)
	}
	if *downloads != 0 {
		t.Fatalf("already-latest must not download, got %d", *downloads)
	}
}

func TestUpgradeNoAssetForOS(t *testing.T) {
	// --version skips the API; linux/arm64 is unpublished → upgrade.no_asset.
	err := runUpgrade(stdctx.Background(), http.DefaultClient, &bytes.Buffer{}, &bytes.Buffer{},
		upgradeOpts{version: "v9.9.9"}, upgradeEnv{current: "v0.13.0", goos: "linux", goarch: "arm64", execPath: tempExe(t)})
	var cliErr *CLIError
	if !errors.As(err, &cliErr) || cliErr.Code != "upgrade.no_asset" {
		t.Fatalf("expected upgrade.no_asset, got %v", err)
	}
}

func TestExtractBinaryZip(t *testing.T) {
	want := []byte("WIN-BINARY")
	z := makeZip(t, "miucr.exe", want)
	got, err := extractBinary(z, "miucr_windows_x86_64.zip", "miucr.exe")
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("extractZip: %v / %q", err, got)
	}
	if _, err := extractBinary(makeZip(t, "other", want), "x.zip", "miucr.exe"); err == nil {
		t.Fatalf("expected extract_failed for missing binary")
	}
}

func TestParseChecksums(t *testing.T) {
	body := []byte("abc123  miucr_linux_x86_64.tar.gz\ndef456 *miucr_windows_x86_64.zip\n")
	m := parseChecksums(body)
	if m["miucr_linux_x86_64.tar.gz"] != "abc123" || m["miucr_windows_x86_64.zip"] != "def456" {
		t.Fatalf("parseChecksums: %#v", m)
	}
}
