// Package clierr holds the typed CLI failure value. It is a leaf package (no
// internal imports) so engine/agent/store/diff can return CLIError without
// transitively importing the full cli package, which lets cli import those
// packages and share their types directly instead of duplicating them.
package clierr

// CLIError is a typed command failure carrying the envelope error fields plus a
// process exit code, so a single error value drives both stdout and the shell status.
type CLIError struct {
	Code      string
	Message   string
	Hint      string
	Exit      int
	Details   map[string]any
	Retry     bool
	SafeRetry bool
	// AlreadyWritten signals the command already emitted its own envelope: Execute
	// carries the exit code but does NOT overwrite stdout with an error-only envelope.
	AlreadyWritten bool
	// Cause preserves the wrapped error for errors.Is/As; it is never rendered into
	// the envelope (only Message is), so an already-redacted cause leaks nothing.
	Cause error
}

func (e *CLIError) Error() string { return e.Message }

func (e *CLIError) Unwrap() error { return e.Cause }
