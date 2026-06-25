package cli

import (
	"fmt"
	"io"
	"os"
	"time"
)

// newProgress builds the stderr progress sink for review. Progress is shown when
// !quiet && (verbose || stderr is a terminal): interactive runs get it by
// default; piped/CI (not a char device) stays silent so the stdout envelope and
// its parsers are unaffected; -v forces it on, -q forces it off. A nil sink is a
// silent no-op downstream. Only milestone strings + file paths/tool names ever
// reach it, never tokens.
func newProgress(w io.Writer, verbose, quiet bool) func(string) {
	if quiet || (!verbose && !isTerminal(w)) {
		return nil
	}
	return func(msg string) {
		fmt.Fprintf(w, "miu-cr: %s %s\n", time.Now().Format("15:04:05.000"), msg)
	}
}

// isTerminal reports whether w is a character device (an interactive terminal),
// checking the actual writer passed in (not a hardcoded os.Stderr) using only the
// stdlib; no new dependency. A non-*os.File writer (e.g. a test buffer) is not a
// terminal, so auto-progress stays off there.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, _ := f.Stat()
	return fi != nil && fi.Mode()&os.ModeCharDevice != 0
}
