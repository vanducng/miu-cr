package mcpserver

import (
	"encoding/json"
	"fmt"

	"github.com/vanducng/miu-cr/internal/config"
)

type SafetyError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *SafetyError) Error() string {
	if e.Message != "" {
		return e.Code + ": " + e.Message
	}
	return e.Code
}

type safetyPolicy struct {
	maxBytes int
}

func newSafetyPolicy(opts Options) safetyPolicy {
	return safetyPolicy{maxBytes: opts.MaxBytes}
}

func (p safetyPolicy) enforceBytes(value any) error {
	if p.maxBytes <= 0 {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) <= p.maxBytes {
		return nil
	}
	return &SafetyError{
		Code:    "review.output_too_large",
		Message: p.boundMessage(fmt.Sprintf("MCP tool output is %d bytes, over max %d bytes", len(data), p.maxBytes)),
	}
}

func (p safetyPolicy) toolErr(code string, err error) error {
	if err == nil {
		return nil
	}
	return &SafetyError{Code: code, Message: p.boundMessage(config.RedactString(err.Error()))}
}

func (p safetyPolicy) boundMessage(message string) string {
	if p.maxBytes <= 0 {
		return message
	}
	max := p.maxBytes / 2
	if max <= 0 {
		max = 1
	}
	if max > 512 {
		max = 512
	}
	if len(message) <= max {
		return message
	}
	if max <= len("...[truncated]") {
		return message[:max]
	}
	return message[:max-len("...[truncated]")] + "...[truncated]"
}
