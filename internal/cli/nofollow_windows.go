//go:build windows

package cli

// ponytail: Windows has no O_NOFOLLOW; symlink creation needs elevated
// privilege there and host mode runs on Linux in practice. The post-open
// ModeSymlink check still rejects a symlinked secret file.
const oNoFollow = 0

func isSymlinkLoopErr(error) bool { return false }
