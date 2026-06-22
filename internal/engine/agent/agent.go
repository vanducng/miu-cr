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
	Text  string
	Rules string // fenced rules section emitted before the diff in the USER turn
	// SemanticContext is the optional M7 advisory block. LOCKSTEP: mirror Rules —
	// it threads engine.AgentContext -> here -> PromptParts -> BuildUserPrompt in
	// BOTH agent.go and openai.go, or it is silently dropped.
	SemanticContext string
	RepoDir         string
	Rev             string
	Runner          *gitcmd.Runner
	Progress        func(string) // nil = silent; milestone/tool strings only, never secrets
	// Trace, when non-nil, accumulates the raw prompt, per-turn tool calls, and
	// raw final response for persistence. nil = no capture (mirrors Progress).
	Trace *engine.ReviewTrace
}

// progress invokes the sink when set; a nil sink is a silent no-op.
func (c Context) progress(msg string) {
	if c.Progress != nil {
		c.Progress(msg)
	}
}

// Agent runs one review pass over the assembled context and returns findings
// WITHOUT line numbers (the engine re-anchors from QuotedCode).
type Agent interface {
	Review(ctx stdctx.Context, rc Context) ([]engine.Finding, error)
}

const (
	maxToolTurns   = 24
	maxEmptyRounds = 3
	maxTokens      = 8192

	// Injected on the final tool turn (with tools withdrawn) so a budget-exhausted
	// large diff is forced to finalize into a real review, not a hard failure.
	forceFinalizeNudge = "Tool budget reached. Reply now with ONLY the findings JSON {\"findings\":[...]} — no tools, no prose."
)

// anthropicClient is the subset of the Anthropic SDK the agent needs; satisfied
// by the real client and a fake in tests so the tool/parse loop runs offline.
type anthropicClient interface {
	newMessage(ctx stdctx.Context, params anthropic.MessageNewParams) (*anthropic.Message, error)
}

type sdkAnthropicClient struct{ sdk anthropic.Client }

func (c sdkAnthropicClient) newMessage(ctx stdctx.Context, params anthropic.MessageNewParams) (*anthropic.Message, error) {
	return c.sdk.Messages.New(ctx, params)
}

// anthropicAgent is the production Agent backed by the Anthropic Messages API.
type anthropicAgent struct {
	client  anthropicClient
	model   string
	timeout time.Duration
}

// newAnthropicAgent builds the Anthropic-backed Agent (registered for
// config.KindAnthropic; see registry.go for the dispatch).
func newAnthropicAgent(creds Credentials, timeout time.Duration) *anthropicAgent {
	return &anthropicAgent{
		client:  sdkAnthropicClient{sdk: anthropic.NewClient(anthropicOptions(creds)...)},
		model:   creds.Model,
		timeout: timeout,
	}
}

// anthropicOptions builds SDK request options from resolved credentials,
// supporting Anthropic-compatible gateways via base URL + Bearer auth token.
// When AuthToken is set it is sent as Authorization (and x-api-key is dropped);
// otherwise APIKey goes via x-api-key.
func anthropicOptions(creds Credentials) []option.RequestOption {
	var opts []option.RequestOption
	if creds.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(creds.BaseURL))
	}
	if creds.AuthToken != "" {
		opts = append(opts, option.WithHeaderDel("X-Api-Key"), option.WithAuthToken(creds.AuthToken))
	} else {
		opts = append(opts, option.WithAPIKey(creds.APIKey))
	}
	return opts
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
	// The ctx deadline (below) owns the wall clock; each turn checks ctx.Err()
	// rather than tracking a parallel manual deadline.
	if a.timeout > 0 {
		var cancel stdctx.CancelFunc
		ctx, cancel = stdctx.WithTimeout(ctx, a.timeout)
		defer cancel()
	}
	if rc.Runner == nil {
		rc.Runner = gitcmd.New()
	}

	userPrompt := BuildUserPrompt(PromptParts{Rules: rc.Rules, SemanticContext: rc.SemanticContext, Diff: rc.Text})
	rc.Trace.SetPrompt(userPrompt)
	params := anthropic.MessageNewParams{
		MaxTokens: maxTokens,
		Model:     anthropic.Model(a.model),
		System:    []anthropic.TextBlockParam{{Text: systemPrompt}},
		Tools:     reviewTools(),
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(userPrompt)),
		},
	}

	emptyRounds := 0
	for turn := 0; turn < maxToolTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rc.progress(fmt.Sprintf("thinking… (turn %d)", turn+1))

		// Final allowed turn: withdraw the tools so the model can no longer keep
		// exploring and must answer, and fold the finalize nudge into the trailing
		// user turn (the loop invariant guarantees the last message is user-role,
		// so this avoids an illegal consecutive user message).
		if turn == maxToolTurns-1 {
			params.Tools = nil
			last := len(params.Messages) - 1
			params.Messages[last].Content = append(params.Messages[last].Content, anthropic.NewTextBlock(forceFinalizeNudge))
		}

		msg, err := a.client.newMessage(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("agent: messages.new: %w", err)
		}
		params.Messages = append(params.Messages, msg.ToParam())

		toolResults, finalText := a.dispatch(ctx, rc, turn, msg)
		if len(toolResults) == 0 {
			if findings, ok := parseFindings(finalText); ok {
				rc.Trace.SetFinalResponse(finalText)
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
	return nil, fmt.Errorf("agent: forced finalization produced no parseable findings after %d turns", maxToolTurns)
}

// dispatch executes every tool_use block in msg, returning the tool_result
// blocks and the concatenated assistant text (the candidate final answer).
func (a *anthropicAgent) dispatch(ctx stdctx.Context, rc Context, turn int, msg *anthropic.Message) ([]anthropic.ContentBlockParamUnion, string) {
	var results []anthropic.ContentBlockParamUnion
	var text strings.Builder
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "tool_use":
			out, isErr := runTool(ctx, rc, turn, block.Name, block.Input)
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

// fileReadLabel renders a short "path:start-end" label for the progress sink
// (range omitted when unset). Paths/line numbers only — never secrets.
func fileReadLabel(a fileReadArgs) string {
	if a.Start <= 0 {
		return a.File // no usable start (incl. line 0, which doesn't exist) → just the path
	}
	if a.End <= 0 {
		return fmt.Sprintf("%s:%d", a.File, a.Start)
	}
	return fmt.Sprintf("%s:%d-%d", a.File, a.Start, a.End)
}

// runTool executes one tool against the reviewed revision. Provider-agnostic so
// all agent loops share it (and record the dispatch into the trace). Returns
// (content, isError).
func runTool(ctx stdctx.Context, rc Context, turn int, name string, input json.RawMessage) (string, bool) {
	switch name {
	case "file_read":
		var args fileReadArgs
		_ = json.Unmarshal(input, &args)
		if strings.TrimSpace(args.File) == "" {
			return "file_read requires a non-empty \"file\"", true
		}
		rc.progress("→ file_read " + fileReadLabel(args))
		rc.Trace.RecordTool(turn, "file_read", fileReadLabel(args))
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
		rc.progress("→ grep " + args.Pattern)
		rc.Trace.RecordTool(turn, "grep", args.Pattern)
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
