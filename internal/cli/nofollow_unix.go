//go:build !windows

package cli

import (
	"errors"
	"syscall"
)

// oNoFollow makes OpenFile refuse a final-component symlink, closing the
// symlink-swap TOCTOU on host secret/prompt/rule files atomically.
const oNoFollow = syscall.O_NOFOLLOW

func isSymlinkLoopErr(err error) bool { return errors.Is(err, syscall.ELOOP) }
