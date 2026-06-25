//go:build !unix

package rules

import "os"

// openNoFollow has no atomic O_NOFOLLOW on this platform (e.g. Windows), so it
// lstat-rejects a symlink, then opens. The residual check-then-open TOCTOU is
// accepted: the rules dir is a static checkout (CI temp-clone / serve daemon),
// with no concurrent attacker process to win the race; the real threat, a
// committed symlink, is still blocked by the lstat reject.
func openNoFollow(path string) (*os.File, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, errSymlink
	}
	return os.Open(path)
}
