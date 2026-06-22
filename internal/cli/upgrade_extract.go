package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/vanducng/miu-cr/internal/config"
)

const maxBinaryBytes = 256 << 20

func binaryNameFor(goos string) string {
	if goos == "windows" {
		return "miucr.exe"
	}
	return "miucr"
}

func extractErr(msg string) error {
	return &CLIError{Code: "upgrade.extract_failed", Message: config.RedactString(msg), Exit: 1}
}

// extractBinary pulls the miucr binary out of the downloaded archive (tar.gz for
// unix, zip for windows).
func extractBinary(data []byte, asset, binName string) ([]byte, error) {
	if strings.HasSuffix(asset, ".zip") {
		return extractZip(data, binName)
	}
	return extractTarGz(data, binName)
}

func extractTarGz(data []byte, binName string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, extractErr("open gzip: " + err.Error())
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, extractErr("read tar: " + err.Error())
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == binName {
			return io.ReadAll(io.LimitReader(tr, maxBinaryBytes))
		}
	}
	return nil, extractErr("archive did not contain " + binName)
}

func extractZip(data []byte, binName string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, extractErr("open zip: " + err.Error())
	}
	for _, f := range zr.File {
		if filepath.Base(f.Name) == binName {
			rc, err := f.Open()
			if err != nil {
				return nil, extractErr("open zip entry: " + err.Error())
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, maxBinaryBytes))
		}
	}
	return nil, extractErr("archive did not contain " + binName)
}

func notWritable(path string, err error) error {
	msg := "cannot replace " + path
	if err != nil {
		msg += ": " + config.RedactString(err.Error())
	}
	return &CLIError{
		Code:    "upgrade.not_writable",
		Message: msg,
		Hint:    "re-run with write access to that directory (e.g. sudo) or reinstall via the install script",
		Exit:    1,
	}
}

// replaceBinary atomically swaps the binary at path with data. The temp file is
// created in the same dir so the final os.Rename is atomic (same filesystem).
// On Windows a running .exe can't be renamed over, so the current file is moved
// aside (.old) first; the leftover .old is removed best-effort.
func replaceBinary(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".miucr-upgrade-*")
	if err != nil {
		return notWritable(dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return notWritable(dir, err)
	}
	if err := tmp.Close(); err != nil {
		return notWritable(dir, err)
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return notWritable(dir, err)
	}
	if runtime.GOOS == "windows" {
		old := path + ".old"
		_ = os.Remove(old)
		if err := os.Rename(path, old); err != nil {
			return notWritable(path, err)
		}
		if err := os.Rename(tmpName, path); err != nil {
			if rb := os.Rename(old, path); rb != nil {
				return &CLIError{Code: "upgrade.not_writable", Message: fmt.Sprintf("upgrade failed and rollback failed; restore the binary from %s manually: %v (rollback: %v)", old, err, rb), Exit: 1}
			}
			return notWritable(path, err)
		}
		_ = os.Remove(old)
		return nil
	}
	if err := os.Rename(tmpName, path); err != nil {
		return notWritable(path, err)
	}
	return nil
}
