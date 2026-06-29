package cli

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseCLILogLevel(t *testing.T) {
	tests := []struct {
		raw  string
		want slog.Level
	}{
		{"", slog.LevelInfo},
		{"debug", slog.LevelDebug},
		{"INFO", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
	}
	for _, tt := range tests {
		got, err := parseCLILogLevel(tt.raw)
		if err != nil {
			t.Fatalf("parseCLILogLevel(%q): %v", tt.raw, err)
		}
		if got != tt.want {
			t.Fatalf("parseCLILogLevel(%q) = %v, want %v", tt.raw, got, tt.want)
		}
	}
	if _, err := parseCLILogLevel("verbose"); err == nil {
		t.Fatal("expected invalid log level to fail")
	}
}

func TestServeTraceSinkFromEnv(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	t.Setenv("MIUCR_TRACE_LOG", "")
	factory, err := serveTraceSinkFactoryFromEnv(log)
	if err != nil {
		t.Fatalf("default trace env: %v", err)
	}
	if factory != nil {
		t.Fatal("trace sink factory should be disabled by default")
	}

	t.Setenv("MIUCR_TRACE_LOG", "true")
	t.Setenv("MIUCR_TRACE_LOG_MAX_BYTES", "512")
	factory, err = serveTraceSinkFactoryFromEnv(log)
	if err != nil {
		t.Fatalf("enabled trace env: %v", err)
	}
	if factory == nil {
		t.Fatal("trace sink factory should be enabled")
	}
	factory(log.With("job_id", int64(7)))("system_prompt", "x")
	if !strings.Contains(buf.String(), "job_id=7") {
		t.Fatalf("per-job trace sink dropped bound context: %s", buf.String())
	}

	t.Setenv("MIUCR_TRACE_LOG_MAX_BYTES", "small")
	if _, err := serveTraceSinkFactoryFromEnv(log); err == nil {
		t.Fatal("invalid max bytes should fail")
	}
}

func TestTraceLogSinkRedactsAndTruncates(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	sink := newTraceLogSink(log, 128)

	sink("user_prompt", map[string]string{
		"text": "password=synthetic-secret-value " + strings.Repeat("x", 300),
	})

	out := buf.String()
	if !strings.Contains(out, "review trace") || !strings.Contains(out, "step=user_prompt") {
		t.Fatalf("missing trace log fields: %s", out)
	}
	if strings.Contains(out, "synthetic-secret-value") {
		t.Fatalf("secret leaked through trace log: %s", out)
	}
	if !strings.Contains(out, "password=[redacted]") {
		t.Fatalf("redacted marker missing: %s", out)
	}
	if !strings.Contains(out, "truncated=true") || !strings.Contains(out, "...[truncated]...") {
		t.Fatalf("trace payload was not truncated: %s", out)
	}
}

func TestTraceLogSinkMarksFinalResponseTerminality(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	sink := newTraceLogSink(log, 512)

	sink("final_response", map[string]any{
		"source":          "subagent",
		"subagent":        "backend",
		"review_terminal": false,
		"payload":         `{"findings":[]}`,
	})
	out := buf.String()
	if !strings.Contains(out, "source=subagent") || !strings.Contains(out, "subagent=backend") || !strings.Contains(out, "review_terminal=false") {
		t.Fatalf("missing subagent trace attrs: %s", out)
	}
	if strings.Count(out, "review_terminal=") != 1 {
		t.Fatalf("terminal attr should be emitted once: %s", out)
	}

	buf.Reset()
	sink("final_response", `{"findings":[]}`)
	if !strings.Contains(buf.String(), "review_terminal=true") {
		t.Fatalf("top-level final_response should be terminal: %s", buf.String())
	}
}

func TestTruncateLogValueKeepsHeadAndTail(t *testing.T) {
	got, truncated := truncateLogValue("start-"+strings.Repeat("中", 50)+"-end", 40)
	if !truncated {
		t.Fatal("expected value to be truncated")
	}
	if len(got) > 40 {
		t.Fatalf("truncated value length = %d, want <= 40", len(got))
	}
	if !strings.HasPrefix(got, "start-") || !strings.HasSuffix(got, "-end") {
		t.Fatalf("truncated value should keep head and tail: %q", got)
	}
	if !strings.Contains(got, "...[truncated]...") {
		t.Fatalf("missing middle truncation marker: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncated value is not valid UTF-8: %q", got)
	}
}
