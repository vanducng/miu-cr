package agent

import (
	stdctx "context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	openai "github.com/openai/openai-go/v3"
	openaiopt "github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

// classifyOpenAIErr types a proven OpenAI-compatible API status into the stable
// taxonomy; an unrecognized error keeps the bare %w wrap so the ctx error chain
// survives to the review-layer errors.Is.
func classifyOpenAIErr(err error) error {
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		if c := classifyStatus(apiErr.StatusCode, err.Error(), hintLoginOpenAI, codeAuthFailed); c != nil {
			c.Cause = err // preserve the SDK error for errors.Is/As
			return c
		}
	}
	return fmt.Errorf("agent: chat.completions: %w", err)
}

// openaiClient is the subset of the OpenAI SDK the agent needs; satisfied by the
// real client and a fake in tests so the parse/tool loop runs without network.
type openaiClient interface {
	create(ctx stdctx.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error)
}

type sdkOpenAIClient struct{ sdk openai.Client }

func (c sdkOpenAIClient) create(ctx stdctx.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	return c.sdk.Chat.Completions.New(ctx, params)
}

// openaiAgent is an OpenAI-compatible (chat-completions + tool-use) Agent.
type openaiAgent struct {
	client      openaiClient
	model       string
	timeout     time.Duration
	temperature float64
	thinking    string
}

func newOpenAIAgent(creds Credentials, timeout time.Duration) *openaiAgent {
	baseURL := strings.TrimRight(creds.BaseURL, "/")
	if baseURL == "" {
		baseURL = config.DefaultOpenAIBaseURL
	}
	// Total wall clock is owned by the ctx deadline (WithTimeout in Review),
	// matching the Anthropic path; no per-request timeout so retries don't get
	// the full --timeout each and inflate the budget past --timeout.
	opts := []openaiopt.RequestOption{
		openaiopt.WithAPIKey(creds.APIKey),
		openaiopt.WithBaseURL(baseURL),
		openaiopt.WithMaxRetries(3),
	}
	sdk := openai.NewClient(opts...)
	return &openaiAgent{client: sdkOpenAIClient{sdk: sdk}, model: creds.Model, timeout: timeout, temperature: creds.Temperature, thinking: creds.Thinking}
}

// openAIReasoningEffort maps an effort level to the SDK constant (default medium).
func openAIReasoningEffort(effort string) shared.ReasoningEffort {
	switch effort {
	case "low":
		return shared.ReasoningEffortLow
	case "high":
		return shared.ReasoningEffortHigh
	default:
		return shared.ReasoningEffortMedium
	}
}

func openAITools() []openai.ChatCompletionToolUnionParam {
	fileReadParams := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file":  map[string]any{"type": "string", "description": "path to read"},
			"start": map[string]any{"type": "integer", "description": "1-based start line"},
			"end":   map[string]any{"type": "integer", "description": "1-based end line"},
		},
		"required": []string{"file"},
	}
	grepParams := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{"type": "string", "description": "fixed string to search for"},
			"file":    map[string]any{"type": "string", "description": "optional file path to limit the search"},
		},
		"required": []string{"pattern"},
	}
	return []openai.ChatCompletionToolUnionParam{
		openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        "file_read",
			Description: openai.String("Read a line range of a file at the reviewed revision."),
			Parameters:  shared.FunctionParameters(fileReadParams),
		}),
		openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        "grep",
			Description: openai.String("Search the reviewed revision for a fixed string."),
			Parameters:  shared.FunctionParameters(grepParams),
		}),
	}
}

// RepairPatch issues one tools-less, code-only chat completion and returns the
// fence-stripped, trimmed reply (lockstep with anthropicAgent.RepairPatch).
func (a *openaiAgent) RepairPatch(ctx stdctx.Context, rr RepairRequest) (string, error) {
	if a.timeout > 0 {
		var cancel stdctx.CancelFunc
		ctx, cancel = stdctx.WithTimeout(ctx, a.timeout)
		defer cancel()
	}
	repairParams := openai.ChatCompletionNewParams{
		Model: shared.ChatModel(a.model),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(repairSystemPrompt),
			openai.UserMessage(BuildRepairPrompt(rr)),
		},
	}
	if isOpenAIReasoningModel(a.model) {
		repairParams.MaxCompletionTokens = openai.Int(int64(repairMaxTokens))
	} else {
		repairParams.Temperature = openai.Float(a.temperature)
		repairParams.MaxTokens = openai.Int(int64(repairMaxTokens))
	}
	resp, err := a.client.create(ctx, repairParams)
	if err != nil {
		return "", classifyOpenAIErr(err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("agent: empty completion (no choices)")
	}
	return parseRepairReply(resp.Choices[0].Message.Content), nil
}

func (a *openaiAgent) Review(ctx stdctx.Context, rc Context) (engine.ReviewOutput, error) {
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

	userPrompt := BuildUserPrompt(PromptParts{Rules: rc.Rules, SemanticContext: rc.SemanticContext, ProjectContext: rc.ProjectContext, RelatedContext: rc.RelatedContext, WantDiagram: rc.WantDiagram, Instruction: rc.Instruction, Conversation: rc.Conversation, Diff: rc.Text})
	rc.Trace.SetSystemPrompt(systemPrompt)
	rc.Trace.SetModel(string(config.KindOpenAI), a.model)
	rc.Trace.SetPrompt(userPrompt)
	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(systemPrompt),
		openai.UserMessage(userPrompt),
	}
	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(a.model),
		Messages: messages,
		Tools:    openAITools(),
	}
	// Reasoning models (o-series/gpt-5) reject temperature != 1 AND max_tokens (they
	// require max_completion_tokens) on Chat Completions; drive depth via
	// reasoning_effort. Plain chat models take the configured temperature (0 by
	// default) and max_tokens (the newer field lags on some OpenAI-compatible gateways).
	if isOpenAIReasoningModel(a.model) {
		params.MaxCompletionTokens = openai.Int(int64(maxTokens))
		if wantOn, effort := thinkingSetting(a.thinking); wantOn {
			params.ReasoningEffort = openAIReasoningEffort(effort)
		}
	} else {
		params.Temperature = openai.Float(a.temperature)
		params.MaxTokens = openai.Int(int64(maxTokens))
	}

	emptyRounds := 0
	for turn := 0; turn < maxToolTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return engine.ReviewOutput{}, err
		}
		rc.progress(fmt.Sprintf("thinking… (turn %d)", turn+1))

		// Final allowed turn: withdraw the tools so the model can no longer keep
		// exploring and must answer, and append the finalize nudge so a
		// budget-exhausted large diff yields a real review, not a hard failure.
		if turn == maxToolTurns-1 {
			params.Tools = nil
			params.Messages = append(params.Messages, openai.UserMessage(forceFinalizeNudge))
		}

		resp, err := a.client.create(ctx, params)
		if err != nil {
			return engine.ReviewOutput{}, classifyOpenAIErr(err)
		}
		if len(resp.Choices) == 0 {
			return engine.ReviewOutput{}, fmt.Errorf("agent: empty completion (no choices)")
		}
		msg := resp.Choices[0].Message
		params.Messages = append(params.Messages, msg.ToParam())

		if len(msg.ToolCalls) == 0 {
			if out, ok := parseFindings(msg.Content); ok {
				rc.Trace.SetFinalResponse(msg.Content)
				return out, nil
			}
			emptyRounds++
			if emptyRounds >= maxEmptyRounds {
				return engine.ReviewOutput{}, fmt.Errorf("agent: model produced no tool calls and no parseable findings after %d rounds", emptyRounds)
			}
			params.Messages = append(params.Messages, openai.UserMessage(
				"You did not call a tool and did not return valid findings JSON. Reply with ONLY the JSON object {\"findings\":[...]} as specified, no prose, no markdown fences."))
			continue
		}
		emptyRounds = 0
		for _, tc := range msg.ToolCalls {
			fn := tc.Function
			out, isErr := runTool(ctx, rc, turn, fn.Name, json.RawMessage(fn.Arguments))
			if isErr {
				out = "ERROR: " + out
			}
			params.Messages = append(params.Messages, openai.ToolMessage(out, tc.ID))
		}
	}
	return engine.ReviewOutput{}, fmt.Errorf("agent: forced finalization produced no parseable findings after %d turns", maxToolTurns)
}
