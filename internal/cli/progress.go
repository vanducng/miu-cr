package cli

import (
	"fmt"
	"io"
	"os"
)

// newProgress builds the stderr progress sink for review. Progress is shown when
// !quiet && (verbose || stderr is a terminal): interactive runs get it by
// default; piped/CI (not a char device) stays silent so the stdout envelope and
// its parsers are unaffected; -v forces it on, -q forces it off. A nil sink is a
// silent no-op downstream. Only milestone strings + file paths/tool names ever
// reach it — never tokens.
func newProgress(w io.Writer, verbose, quiet bool) func(string) {
	if quiet || (!verbose && !stderrIsTerminal()) {
		return nil
	}
	return func(msg string) { fmt.Fprintln(w, "miu-cr: "+msg) }
}

// stderrIsTerminal reports whether stderr is a character device (an interactive
// terminal) using only the stdlib — no new dependency.
func stderrIsTerminal() bool {
	fi, _ := os.Stderr.Stat()
	return fi != nil && fi.Mode()&os.ModeCharDevice != 0
}
