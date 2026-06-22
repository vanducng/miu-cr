package cli

import (
	stdctx "context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/vanducng/miu-cr/internal/config"
)

const upgradeHTTPTimeout = 5 * time.Minute

type upgradeOpts struct {
	check   bool
	version string
}

type upgradeEnv struct {
	current  string
	goos     string
	goarch   string
	execPath string
}

func upgradeCommand(_ *options) *cobra.Command {
	o := upgradeOpts{}
	cmd := &cobra.Command{
		Use:     "upgrade",
		Aliases: []string{"update"},
		Short:   "Self-update miucr from the latest GitHub release",
		RunE: func(cmd *cobra.Command, args []string) error {
			exe, err := os.Executable()
			if err != nil {
				return &CLIError{Code: "upgrade.not_writable", Message: "cannot locate the running binary: " + config.RedactString(err.Error()), Exit: 1}
			}
			if resolved, err := filepath.EvalSymlinks(exe); err == nil {
				exe = resolved
			}
			ctx, cancel := stdctx.WithTimeout(cmd.Context(), upgradeHTTPTimeout)
			defer cancel()
			return runUpgrade(ctx, &http.Client{}, cmd.OutOrStdout(), cmd.ErrOrStderr(), o, upgradeEnv{
				current:  versionString(),
				goos:     runtime.GOOS,
				goarch:   runtime.GOARCH,
				execPath: exe,
			})
		},
	}
	f := cmd.Flags()
	f.BoolVar(&o.check, "check", false, "Only report whether a newer version exists; do not download")
	f.StringVar(&o.version, "version", "", "Install a specific release tag instead of the latest")
	return cmd
}

func runUpgrade(ctx stdctx.Context, client *http.Client, stdout, stderr io.Writer, o upgradeOpts, env upgradeEnv) error {
	target, err := resolveTargetTag(ctx, client, o.version)
	if err != nil {
		return err
	}
	asset, assetErr := assetNameFor(env.goos, env.goarch)

	if o.check {
		available := !sameVersion(env.current, target)
		fmt.Fprintf(stderr, "miucr: current %s, latest %s\n", env.current, target)
		return writeUpgradeResult(stdout, env.current, target, asset, env.execPath, "check_only", map[string]any{
			"update_available": available,
		})
	}

	if sameVersion(env.current, target) {
		msg := "already on the latest version " + target
		fmt.Fprintln(stderr, "miucr: "+msg)
		return writeUpgradeResult(stdout, env.current, target, asset, env.execPath, "already_latest", map[string]any{
			"message": msg,
		})
	}

	if assetErr != nil {
		return assetErr
	}

	fmt.Fprintf(stderr, "miucr: downloading %s %s (%s/%s)...\n", asset, target, env.goos, env.goarch)
	archive, err := httpGetBytes(ctx, client, downloadURL(target, asset))
	if err != nil {
		return fetchFailed(err)
	}
	sums, err := fetchChecksums(ctx, client, target)
	if err != nil {
		return err
	}
	if err := verifyChecksum(archive, asset, sums); err != nil {
		return err
	}
	bin, err := extractBinary(archive, asset, binaryNameFor(env.goos))
	if err != nil {
		return err
	}
	if err := replaceBinary(env.execPath, bin); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "miucr: upgraded %s -> %s\n", env.current, target)
	return writeUpgradeResult(stdout, env.current, target, asset, env.execPath, "upgraded", nil)
}

func writeUpgradeResult(stdout io.Writer, from, to, asset, path, action string, extra map[string]any) error {
	data := map[string]any{
		"from_version": from,
		"to_version":   to,
		"asset":        asset,
		"path":         path,
		"action":       action,
	}
	for k, v := range extra {
		data[k] = v
	}
	return writeSuccess(stdout, "upgrade", "upgrade.result", data, map[string]any{"action": action})
}
