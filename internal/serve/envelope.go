package serve

import (
	"encoding/json"
	"net/http"

	"github.com/vanducng/miu-cr/internal/config"
)

// apiVersion mirrors cli's miucr.cli/v1 envelope contract; cli's writer is
// unexported, so serve emits a small local writer of the same shape rather than
// exporting cli internals just for the REST handlers.
const apiVersion = "miucr.cli/v1"

type envelope struct {
	OK         bool           `json:"ok"`
	APIVersion string         `json:"api_version"`
	Kind       string         `json:"kind"`
	Command    string         `json:"command"`
	Data       any            `json:"data,omitempty"`
	Artifacts  []any          `json:"artifacts"`
	Warnings   []any          `json:"warnings"`
	Error      *envelopeError `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeEnvelopeSuccess emits a miucr.cli/v1 success envelope. data must already
// be a whitelist (no secrets, no host paths).
func writeEnvelopeSuccess(w http.ResponseWriter, code int, command, kind string, data any) {
	writeEnvelope(w, code, envelope{OK: true, Kind: kind, Command: command, Data: data})
}

// writeEnvelopeError emits a typed miucr.cli/v1 error envelope; the message is
// always funneled through config.RedactString so no secret can escape.
func writeEnvelopeError(w http.ResponseWriter, code int, command, errCode, msg string) {
	writeEnvelope(w, code, envelope{
		OK:      false,
		Kind:    "error",
		Command: command,
		Error:   &envelopeError{Code: errCode, Message: config.RedactString(msg)},
	})
}

func writeEnvelope(w http.ResponseWriter, code int, env envelope) {
	env.APIVersion = apiVersion
	env.Artifacts = []any{}
	env.Warnings = []any{}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	// Discard intentional: every caller returns immediately after this write, so a
	// broken connection mid-encode has no actionable downstream handling.
	_ = enc.Encode(env)
}
