package agent

import (
	stdctx "context"
	"encoding/json"
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
	client  openaiClient
	model   string
	timeout time.Duration
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
	return &openaiAgent{client: sdkOpenAIClient{sdk: sdk}, model: creds.Model, timeout: timeout}
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

func (a *openaiAgent) Review(ctx stdctx.Context, rc Context) ([]engine.Finding, error) {
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

	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(systemPrompt),
		openai.UserMessage(BuildUserPrompt(rc.Text)),
	}
	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(a.model),
		Messages: messages,
		Tools:    openAITools(),
		// max_tokens (not max_completion_tokens) for the broadest compatibility
		// with OpenAI-compatible gateways, many of which lag the newer field.
		MaxTokens: openai.Int(int64(maxTokens)),
	}

	emptyRounds := 0
	for turn := 0; turn < maxToolTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		resp, err := a.client.create(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("agent: chat.completions: %w", err)
		}
		if len(resp.Choices) == 0 {
			return nil, fmt.Errorf("agent: empty completion (no choices)")
		}
		msg := resp.Choices[0].Message
		params.Messages = append(params.Messages, msg.ToParam())

		if len(msg.ToolCalls) == 0 {
			if findings, ok := parseFindings(msg.Content); ok {
				return findings, nil
			}
			emptyRounds++
			if emptyRounds >= maxEmptyRounds {
				return nil, fmt.Errorf("agent: model produced no tool calls and no parseable findings after %d rounds", emptyRounds)
			}
			params.Messages = append(params.Messages, openai.UserMessage(
				"You did not call a tool and did not return valid findings JSON. Reply with ONLY the JSON object {\"findings\":[...]} as specified, no prose, no markdown fences."))
			continue
		}
		emptyRounds = 0
		for _, tc := range msg.ToolCalls {
			fn := tc.Function
			out, isErr := runTool(ctx, rc, fn.Name, json.RawMessage(fn.Arguments))
			if isErr {
				out = "ERROR: " + out
			}
			params.Messages = append(params.Messages, openai.ToolMessage(out, tc.ID))
		}
	}
	return nil, fmt.Errorf("agent: exceeded maxToolTurns (%d) without final findings", maxToolTurns)
}
