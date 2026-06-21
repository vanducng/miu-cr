//go:build unix

package rules

import (
	"errors"
	"os"
	"syscall"
)

// openNoFollow opens path read-only, atomically refusing a symlinked final
// component via O_NOFOLLOW. This closes the lstat-then-read TOCTOU: an attacker
// can't swap a regular file for a symlink between the check and the read. A
// symlink target is normalized to errSymlink so callers stay platform-agnostic.
func openNoFollow(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil && errors.Is(err, syscall.ELOOP) {
		return nil, errSymlink
	}
	return f, err
}
