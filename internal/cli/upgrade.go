package cli

import (
	stdctx "context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/vanducng/miu-cr/internal/config"
)

const releaseRepo = "vanducng/miu-cr"

// Overridable in tests to point at an httptest server.
var (
	githubAPIBase      = "https://api.github.com"
	githubDownloadBase = "https://github.com"
)

const maxDownloadBytes = 96 << 20 // caps a hostile/huge asset (binaries are ~20MB)

// ghToken mirrors install.sh: GITHUB_TOKEN then GH_TOKEN. Never logged.
func ghToken() string {
	for _, v := range []string{os.Getenv("GITHUB_TOKEN"), os.Getenv("GH_TOKEN")} {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

func httpGetBytes(ctx stdctx.Context, client *http.Client, target string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	// Bearer only on the API host, matching install.sh's download_stdout.
	if tok := ghToken(); tok != "" && sameHost(target, githubAPIBase) {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", target, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadBytes))
	if err != nil {
		return nil, err
	}
	return body, nil
}

func sameHost(a, b string) bool {
	ua, err := url.Parse(a)
	if err != nil {
		return false
	}
	ub, err := url.Parse(b)
	if err != nil {
		return false
	}
	return ua.Host == ub.Host
}

func fetchFailed(err error) error {
	return &CLIError{
		Code:    "upgrade.fetch_failed",
		Message: config.RedactString(err.Error()),
		Hint:    "check your network or GITHUB_TOKEN, then retry",
		Retry:   true,
		Exit:    1,
	}
}

// resolveTargetTag returns the tag to install: the --version override
// (v-prefixed) or the latest published release's tag_name.
func resolveTargetTag(ctx stdctx.Context, client *http.Client, version string) (string, error) {
	if v := strings.TrimSpace(version); v != "" {
		return ensureVPrefix(v), nil
	}
	body, err := httpGetBytes(ctx, client, githubAPIBase+"/repos/"+releaseRepo+"/releases/latest")
	if err != nil {
		return "", fetchFailed(err)
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &rel); err != nil || strings.TrimSpace(rel.TagName) == "" {
		return "", fetchFailed(errors.New("could not parse tag_name from releases/latest"))
	}
	return rel.TagName, nil
}

func ensureVPrefix(v string) string {
	if v != "" && v[0] >= '0' && v[0] <= '9' {
		return "v" + v
	}
	return v
}

// sameVersion compares two tags ignoring a leading v.
func sameVersion(a, b string) bool {
	return strings.TrimPrefix(a, "v") == strings.TrimPrefix(b, "v")
}

// assetNameFor builds the release archive name for goos/goarch, matching
// .goreleaser.yaml: miucr_<os>_<x86_64|arm64>.<tar.gz|zip>. Unpublished combos
// (linux/arm64, windows/arm64, anything else) return upgrade.no_asset.
func assetNameFor(goos, goarch string) (string, error) {
	arch := ""
	switch goarch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "arm64"
	}
	published := arch != "" && (goos == "darwin" || ((goos == "linux" || goos == "windows") && arch == "x86_64"))
	if !published {
		return "", &CLIError{
			Code:    "upgrade.no_asset",
			Message: fmt.Sprintf("no published release asset for %s/%s", goos, goarch),
			Hint:    "build from source: go install github.com/" + releaseRepo + "/cmd/miucr@latest",
			Exit:    1,
		}
	}
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("miucr_%s_%s.%s", goos, arch, ext), nil
}

func downloadURL(tag, asset string) string {
	return githubDownloadBase + "/" + releaseRepo + "/releases/download/" + tag + "/" + asset
}

// fetchChecksums downloads checksums.txt and maps asset name -> hex sha256.
func fetchChecksums(ctx stdctx.Context, client *http.Client, tag string) (map[string]string, error) {
	body, err := httpGetBytes(ctx, client, downloadURL(tag, "checksums.txt"))
	if err != nil {
		return nil, fetchFailed(err)
	}
	return parseChecksums(body), nil
}

func parseChecksums(body []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(body), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 {
			out[strings.TrimPrefix(f[len(f)-1], "*")] = f[0]
		}
	}
	return out
}

func verifyChecksum(data []byte, asset string, sums map[string]string) error {
	want, ok := sums[asset]
	if !ok {
		return &CLIError{Code: "upgrade.checksum_mismatch", Message: "checksum for " + asset + " not found in checksums.txt", Exit: 1}
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, want) {
		return &CLIError{Code: "upgrade.checksum_mismatch", Message: fmt.Sprintf("checksum mismatch for %s: expected %s, got %s", asset, want, got), Exit: 1}
	}
	return nil
}
