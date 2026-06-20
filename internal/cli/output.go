package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/vanducng/miu-cr/internal/config"
)

const apiVersion = "miucr.cli/v1"

// prettyOutput indents envelope JSON when --output pretty is set.
var prettyOutput bool

type Envelope struct {
	OK         bool           `json:"ok"`
	APIVersion string         `json:"api_version"`
	Kind       string         `json:"kind"`
	Command    string         `json:"command"`
	RequestID  string         `json:"request_id"`
	Summary    map[string]any `json:"summary,omitempty"`
	Data       any            `json:"data,omitempty"`
	Page       map[string]any `json:"page,omitempty"`
	Stats      map[string]any `json:"stats,omitempty"`
	Artifacts  []any          `json:"artifacts"`
	Warnings   []any          `json:"warnings"`
	Error      *ErrorInfo     `json:"error,omitempty"`
}

type ErrorInfo struct {
	Code        string         `json:"code"`
	Message     string         `json:"message"`
	Hint        string         `json:"hint,omitempty"`
	Retryable   bool           `json:"retryable"`
	SafeToRetry bool           `json:"safe_to_retry,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
}

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
}

func (e *CLIError) Error() string { return e.Message }

func newRequestID() string {
	return fmt.Sprintf("req_%d", time.Now().UnixNano())
}

// secretKey matches credential-named field names. Bool flag fields are skipped
// (only string/number values redacted).
var secretKey = regexp.MustCompile(`(?i)(^|_)(passwd|password|passphrase|secret|token|private_key|secret_key|access_key|client_secret|api_?key|auth_token)$`)

// proseKey marks finding-prose JSON keys whose string values are review output,
// not credentials: token-like example text in rationale/patch must survive scrub.
var proseKey = regexp.MustCompile(`(?i)^(rationale|suggested_patch)$`)

// scrubOutput hardens a dynamic envelope field against credential leakage:
// credential-named values become "***" and password-bearing URLs/assignments are
// redacted. Finding-prose values (rationale, suggested_patch) are exempt.
func scrubOutput(v any) any {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // preserve integer precision across the round-trip
	var generic any
	if dec.Decode(&generic) != nil {
		return v
	}
	return scrubWalk(generic, false)
}

// scrubWalk recurses the decoded tree. inProse is true when the current value
// descends from a finding-prose key, in which case string redaction is skipped.
func scrubWalk(v any, inProse bool) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			childProse := inProse || proseKey.MatchString(k)
			if !childProse && secretKey.MatchString(k) {
				if s, ok := val.(string); ok {
					if s != "" {
						x[k] = "***"
					}
					continue
				}
				if _, ok := val.(json.Number); ok {
					x[k] = "***"
					continue
				}
			}
			x[k] = scrubWalk(val, childProse)
		}
		return x
	case []any:
		for i := range x {
			x[i] = scrubWalk(x[i], inProse)
		}
		return x
	case string:
		if inProse {
			return x
		}
		return config.RedactString(x)
	default:
		return v
	}
}

func writeJSON(w io.Writer, env Envelope) error {
	env.APIVersion = apiVersion
	if env.RequestID == "" {
		env.RequestID = newRequestID()
	}
	if env.Artifacts == nil {
		env.Artifacts = []any{}
	}
	if env.Warnings == nil {
		env.Warnings = []any{}
	}
	if m, ok := scrubOutput(env.Summary).(map[string]any); ok {
		env.Summary = m
	}
	env.Data = scrubOutput(env.Data)
	if m, ok := scrubOutput(env.Page).(map[string]any); ok {
		env.Page = m
	}
	if m, ok := scrubOutput(env.Stats).(map[string]any); ok {
		env.Stats = m
	}
	if s, ok := scrubOutput(env.Artifacts).([]any); ok {
		env.Artifacts = s
	}
	if s, ok := scrubOutput(env.Warnings).([]any); ok {
		env.Warnings = s
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if prettyOutput {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(env)
}

func writeSuccess(w io.Writer, command, kind string, data any, summary map[string]any) error {
	return writeJSON(w, Envelope{
		OK:        true,
		Kind:      kind,
		Command:   command,
		Summary:   summary,
		Data:      data,
		Artifacts: []any{},
		Warnings:  []any{},
	})
}

func writeError(w io.Writer, command string, err error) error {
	info := ErrorInfo{
		Code:      "internal.error",
		Message:   config.RedactString(err.Error()),
		Retryable: false,
	}
	var cliErr *CLIError
	if errors.As(err, &cliErr) {
		info.Code = cliErr.Code
		info.Message = config.RedactString(cliErr.Message)
		info.Hint = config.RedactString(cliErr.Hint)
		info.Details = redactDetails(cliErr.Details)
		info.Retryable = cliErr.Retry
		info.SafeToRetry = cliErr.SafeRetry
	}
	return writeJSON(w, Envelope{
		OK:        false,
		Kind:      "error",
		Command:   command,
		Error:     &info,
		Artifacts: []any{},
		Warnings:  []any{},
	})
}

func redactDetails(details map[string]any) map[string]any {
	if len(details) == 0 {
		return details
	}
	out := make(map[string]any, len(details))
	for key, value := range details {
		switch typed := value.(type) {
		case string:
			out[key] = config.RedactString(typed)
		default:
			out[key] = value
		}
	}
	return out
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var cliErr *CLIError
	if errors.As(err, &cliErr) && cliErr.Exit != 0 {
		return cliErr.Exit
	}
	return 1
}
