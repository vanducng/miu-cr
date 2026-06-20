// Package agent runs the single owned LLM review pass: it calls the Anthropic
// Messages API with read/grep tool-use over the deterministically assembled
// context and parses structured JSON findings. The pass sits behind the Agent
// interface so the pipeline is testable with a fake (no network, no API key).
package agent

import (
	stdctx "context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/vanducng/miu-cr/internal/engine"
	enginectx "github.com/vanducng/miu-cr/internal/engine/context"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

// Context is everything the review pass needs: the assembled prompt text plus
// the reviewed revision so the tool loop reads the SAME content the diff came
// from (rev=="" is the staged index).
type Context struct {
	Text    string
	RepoDir string
	Rev     string
	Runner  *gitcmd.Runner
}

// Agent runs one review pass over the assembled context and returns findings
// WITHOUT line numbers (the engine re-anchors from QuotedCode).
type Agent interface {
	Review(ctx stdctx.Context, rc Context) ([]engine.Finding, error)
}

const (
	maxToolTurns   = 16
	maxEmptyRounds = 3
	maxTokens      = 8192
)

// anthropicAgent is the production Agent backed by the Anthropic Messages API.
type anthropicAgent struct {
	client  anthropic.Client
	model   string
	timeout time.Duration
}

// New returns an Anthropic-backed Agent. timeout (the global --timeout) bounds
// both the request context deadline and the tool-loop wall clock; <=0 disables
// the agent-imposed cap (the caller's ctx still applies).
func New(creds Credentials, timeout time.Duration) Agent {
	return &anthropicAgent{
		client:  anthropic.NewClient(option.WithAPIKey(creds.APIKey)),
		model:   creds.Model,
		timeout: timeout,
	}
}

func reviewTools() []anthropic.ToolUnionParam {
	fileReadSchema := anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"file":  map[string]any{"type": "string", "description": "path to read"},
			"start": map[string]any{"type": "integer", "description": "1-based start line"},
			"end":   map[string]any{"type": "integer", "description": "1-based end line"},
		},
		Required: []string{"file"},
	}
	grepSchema := anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"pattern": map[string]any{"type": "string", "description": "fixed string to search for"},
		},
		Required: []string{"pattern"},
	}
	fileRead := anthropic.ToolUnionParamOfTool(fileReadSchema, "file_read")
	fileRead.OfTool.Description = anthropic.String("Read a line range of a file at the reviewed revision.")
	grep := anthropic.ToolUnionParamOfTool(grepSchema, "grep")
	grep.OfTool.Description = anthropic.String("Search the reviewed revision for a fixed string.")
	return []anthropic.ToolUnionParam{fileRead, grep}
}

func (a *anthropicAgent) Review(ctx stdctx.Context, rc Context) ([]engine.Finding, error) {
	if a.timeout > 0 {
		var cancel stdctx.CancelFunc
		ctx, cancel = stdctx.WithTimeout(ctx, a.timeout)
		defer cancel()
	}
	deadline := time.Time{}
	if a.timeout > 0 {
		deadline = time.Now().Add(a.timeout)
	}
	if rc.Runner == nil {
		rc.Runner = gitcmd.New()
	}

	params := anthropic.MessageNewParams{
		MaxTokens: maxTokens,
		Model:     anthropic.Model(a.model),
		System:    []anthropic.TextBlockParam{{Text: systemPrompt}},
		Tools:     reviewTools(),
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(BuildUserPrompt(rc.Text))),
		},
	}

	emptyRounds := 0
	for turn := 0; turn < maxToolTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return nil, fmt.Errorf("agent: tool loop exceeded wall-clock cap of %s", a.timeout)
		}

		msg, err := a.client.Messages.New(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("agent: messages.new: %w", err)
		}
		params.Messages = append(params.Messages, msg.ToParam())

		toolResults, finalText := a.dispatch(ctx, rc, msg)
		if len(toolResults) == 0 {
			if findings, ok := parseFindings(finalText); ok {
				return findings, nil
			}
			emptyRounds++
			if emptyRounds >= maxEmptyRounds {
				return nil, fmt.Errorf("agent: model produced no tool calls and no parseable findings after %d rounds", emptyRounds)
			}
			params.Messages = append(params.Messages, anthropic.NewUserMessage(anthropic.NewTextBlock(
				"You did not call a tool and did not return valid findings JSON. Reply with ONLY the JSON object {\"findings\":[...]} as specified, no prose, no markdown fences.")))
			continue
		}
		emptyRounds = 0
		params.Messages = append(params.Messages, anthropic.NewUserMessage(toolResults...))
	}
	return nil, fmt.Errorf("agent: exceeded maxToolTurns (%d) without final findings", maxToolTurns)
}

// dispatch executes every tool_use block in msg, returning the tool_result
// blocks and the concatenated assistant text (the candidate final answer).
func (a *anthropicAgent) dispatch(ctx stdctx.Context, rc Context, msg *anthropic.Message) ([]anthropic.ContentBlockParamUnion, string) {
	var results []anthropic.ContentBlockParamUnion
	var text strings.Builder
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "tool_use":
			out, isErr := a.runTool(ctx, rc, block.Name, block.Input)
			results = append(results, anthropic.NewToolResultBlock(block.ID, out, isErr))
		}
	}
	return results, text.String()
}

type fileReadArgs struct {
	File  string `json:"file"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

type grepArgs struct {
	Pattern string `json:"pattern"`
	File    string `json:"file"`
}

// runTool executes one tool against the reviewed revision. Arg decoding is
// tolerant: a missing file is injected from grep/single-file fallbacks where it
// makes sense. Returns (content, isError).
func (a *anthropicAgent) runTool(ctx stdctx.Context, rc Context, name string, input json.RawMessage) (string, bool) {
	switch name {
	case "file_read":
		var args fileReadArgs
		_ = json.Unmarshal(input, &args)
		if strings.TrimSpace(args.File) == "" {
			return "file_read requires a non-empty \"file\"", true
		}
		out, err := enginectx.ReadRange(ctx, rc.RepoDir, rc.Rev, args.File, args.Start, args.End, rc.Runner)
		if err != nil {
			return fmt.Sprintf("file_read failed: %v", err), true
		}
		if out == "" {
			return "(no lines in range)", false
		}
		return out, false
	case "grep":
		var args grepArgs
		_ = json.Unmarshal(input, &args)
		if strings.TrimSpace(args.Pattern) == "" {
			return "grep requires a non-empty \"pattern\"", true
		}
		out, err := enginectx.Grep(ctx, rc.RepoDir, rc.Rev, args.Pattern, rc.Runner)
		if err != nil {
			return fmt.Sprintf("grep failed: %v", err), true
		}
		if out == "" {
			return "(no matches)", false
		}
		return out, false
	default:
		return fmt.Sprintf("unknown tool %q", name), true
	}
}

// parseFindings strips markdown fences and unmarshals the model's JSON into
// engine.Findings carrying severity/category/quoted-code and NO line numbers.
func parseFindings(text string) ([]engine.Finding, bool) {
	body := stripMarkdownFences(text)
	if body == "" {
		return nil, false
	}
	var raw rawFindings
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return nil, false
	}
	findings := make([]engine.Finding, 0, len(raw.Findings))
	for _, r := range raw.Findings {
		findings = append(findings, engine.Finding{
			File:           r.File,
			Severity:       r.Severity,
			Category:       r.Category,
			Rationale:      r.Rationale,
			SuggestedPatch: r.SuggestedPatch,
			QuotedCode:     r.ExistingCode,
		})
	}
	return findings, true
}
