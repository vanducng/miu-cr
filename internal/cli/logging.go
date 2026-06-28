package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/vanducng/miu-cr/internal/config"
)

const defaultTraceLogMaxBytes = 4096

func configureDefaultLogger(w io.Writer) error {
	log, err := newCLITextLogger(w)
	if err != nil {
		return err
	}
	slog.SetDefault(log)
	return nil
}

func newCLITextLogger(w io.Writer) (*slog.Logger, error) {
	level, err := parseCLILogLevel(os.Getenv("MIUCR_LOG_LEVEL"))
	if err != nil {
		return nil, err
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})), nil
}

func parseCLILogLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, &CLIError{Code: "config.invalid", Message: fmt.Sprintf("MIUCR_LOG_LEVEL %q is invalid: want debug, info, warn, or error", config.RedactString(raw)), Exit: 2}
	}
}

// captureReasoningFromEnv reads MIUCR_TRACE_REASONING; reuses boolEnv so bad
// values return config.invalid (exit 2). OFF by default.
func captureReasoningFromEnv() (bool, error) {
	return boolEnv("MIUCR_TRACE_REASONING")
}

// serveTraceSinkFactoryFromEnv returns a builder that, given a per-call logger,
// produces a trace sink — or nil when MIUCR_TRACE_LOG is off. Binding the logger
// per call lets callers tag each review's trace lines with job context.
func serveTraceSinkFactoryFromEnv(base *slog.Logger) (func(*slog.Logger) func(step string, payload any), error) {
	if base == nil {
		base = slog.Default()
	}
	enabled, err := boolEnv("MIUCR_TRACE_LOG")
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, nil
	}
	maxBytes, err := traceLogMaxBytes()
	if err != nil {
		return nil, err
	}
	base.Info("review trace logging enabled", "max_bytes", maxBytes)
	return func(l *slog.Logger) func(step string, payload any) {
		if l == nil {
			l = base
		}
		return newTraceLogSink(l, maxBytes)
	}, nil
}

func boolEnv(name string) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	switch strings.ToLower(raw) {
	case "", "0", "false", "off", "no":
		return false, nil
	case "1", "true", "on", "yes":
		return true, nil
	default:
		return false, &CLIError{Code: "config.invalid", Message: fmt.Sprintf("%s %q is invalid: want true or false", name, config.RedactString(raw)), Exit: 2}
	}
}

func traceLogMaxBytes() (int, error) {
	raw := strings.TrimSpace(os.Getenv("MIUCR_TRACE_LOG_MAX_BYTES"))
	if raw == "" {
		return defaultTraceLogMaxBytes, nil
	}
	maxBytes, err := strconv.Atoi(raw)
	if err != nil || maxBytes < 256 {
		return 0, &CLIError{Code: "config.invalid", Message: fmt.Sprintf("MIUCR_TRACE_LOG_MAX_BYTES %q is invalid: want an integer >= 256", config.RedactString(raw)), Exit: 2}
	}
	return maxBytes, nil
}

func newTraceLogSink(log *slog.Logger, maxBytes int) func(step string, payload any) {
	if log == nil {
		log = slog.Default()
	}
	return func(step string, payload any) {
		body, err := json.Marshal(payload)
		if err != nil {
			log.Debug("review trace marshal failed", "step", config.RedactString(step), "err", config.RedactString(err.Error()))
			return
		}
		text, truncated := truncateLogValue(config.RedactString(string(body)), maxBytes)
		log.Debug("review trace", "step", config.RedactString(step), "payload", text, "truncated", truncated)
	}
}

func truncateLogValue(value string, maxBytes int) (string, bool) {
	if maxBytes <= 0 {
		return "", value != ""
	}
	if len(value) <= maxBytes {
		return value, false
	}
	const marker = "...[truncated]..."
	keep := maxBytes - len(marker)
	if keep <= 0 {
		return utf8Prefix(value, maxBytes), true
	}
	prefixBytes := keep/2 + keep%2
	suffixBytes := keep / 2
	return utf8Prefix(value, prefixBytes) + marker + utf8Suffix(value, suffixBytes), true
}

func utf8Prefix(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	for maxBytes > 0 && !utf8.ValidString(value[:maxBytes]) {
		maxBytes--
	}
	return value[:maxBytes]
}

func utf8Suffix(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	start := len(value) - maxBytes
	for start < len(value) && !utf8.ValidString(value[start:]) {
		start++
	}
	return value[start:]
}
