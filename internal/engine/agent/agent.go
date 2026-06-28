// Package agent runs the single owned LLM review pass: it calls the Anthropic
// Messages API with read/grep tool-use over the deterministically assembled
// context and parses structured JSON findings. The pass sits behind the Agent
// interface so the pipeline is testable with a fake (no network, no API key).
package agent

import (
	stdctx "context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/vanducng/miu-cr/internal/engine"
	enginectx "github.com/vanducng/miu-cr/internal/engine/context"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

// classifyAnthropicErr types a proven Anthropic API status into the stable
// taxonomy; an unrecognized error keeps the bare %w wrap so the ctx error chain
// (DeadlineExceeded/Canceled) survives to the review-layer errors.Is.
func classifyAnthropicErr(err error) error {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		if c := classifyStatus(apiErr.StatusCode, err.Error(), hintLoginAnthropic, codeAuthFailed); c != nil {
			c.Cause = err // preserve the SDK error for errors.Is/As
			return c
		}
	}
	return fmt.Errorf("agent: messages.new: %w", err)
}

// Context is everything the review pass needs: the assembled prompt text plus
// the reviewed revision so the tool loop reads the SAME content the diff came
// from (rev=="" is the staged index).
type Context struct {
	Text  string
	Rules string // fenced rules section emitted before the diff in the USER turn
	// SemanticContext is the optional M7 advisory block. LOCKSTEP: mirror Rules,
	// it threads engine.AgentContext -> here -> PromptParts -> BuildUserPrompt in
	// BOTH agent.go and openai.go, or it is silently dropped.
	SemanticContext string
	// ProjectContext is optional deep project context. LOCKSTEP: mirror
	// SemanticContext through every backend or it is silently dropped.
	ProjectContext string
	// RelatedContext is optional hop-expanded related-file context. LOCKSTEP: mirror
	// ProjectContext through every backend or it is silently dropped.
	RelatedContext string
	// WantDiagram opts into the mermaid change diagram (rides the USER turn so OFF
	// is byte-identical and the prompt cache is preserved). LOCKSTEP: thread it from
	// engine.AgentContext into BuildUserPrompt in agent.go/openai.go/codex.go.
	WantDiagram bool
	// Instruction is the optional per-review developer steer. LOCKSTEP: mirror Rules,
	// thread it engine.AgentContext -> here -> PromptParts -> BuildUserPrompt in ALL
	// three backends (agent.go/openai.go/codex.go), or it is silently dropped.
	Instruction string
	// Conversation is the optional fetched PR conversation (UNTRUSTED). LOCKSTEP:
	// mirror Instruction in ALL three backends or it is silently dropped.
	Conversation string
	// OperatorPrompt is trusted host policy. LOCKSTEP: mirror Conversation in ALL backends.
	OperatorPrompt string
	RepoDir        string
	Rev            string
	Runner         *gitcmd.Runner
	Progress       func(string) // nil = silent; milestone/tool strings only, never secrets
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
// WITHOUT line numbers (the engine re-anchors from QuotedCode) plus the optional
// walkthrough/per-file digest the same pass may emit.
type Agent interface {
	Review(ctx stdctx.Context, rc Context) (engine.ReviewOutput, error)
	// RepairPatch runs a single tools-less, code-only completion for ONE span +
	// ONE problem and returns the minimal replacement (fence-stripped). The engine
	// re-validates the reply; "" means no usable replacement (the engine falls
	// back to the original finding).
	RepairPatch(ctx stdctx.Context, rr RepairRequest) (string, engine.Usage, error)
}

const (
	maxToolTurns   = 24
	maxEmptyRounds = 3
	maxTokens      = 8192

	// repairMaxTokens bounds the second-pass replacement: a single span's minimal
	// edit, never a full review.
	repairMaxTokens = 1024

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
	client      anthropicClient
	model       string
	timeout     time.Duration
	temperature float64
	thinking    string
}

// newAnthropicAgent builds the Anthropic-backed Agent (registered for
// config.KindAnthropic; see registry.go for the dispatch).
func newAnthropicAgent(creds Credentials, timeout time.Duration) *anthropicAgent {
	return &anthropicAgent{
		client:      sdkAnthropicClient{sdk: anthropic.NewClient(anthropicOptions(creds)...)},
		model:       creds.Model,
		timeout:     timeout,
		temperature: creds.Temperature,
		thinking:    creds.Thinking,
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
			"file":    map[string]any{"type": "string", "description": "optional file path to limit the search"},
		},
		Required: []string{"pattern"},
	}
	fileRead := anthropic.ToolUnionParamOfTool(fileReadSchema, "file_read")
	fileRead.OfTool.Description = anthropic.String("Read a line range of a file at the reviewed revision.")
	grep := anthropic.ToolUnionParamOfTool(grepSchema, "grep")
	grep.OfTool.Description = anthropic.String("Search the reviewed revision for a fixed string.")
	return []anthropic.ToolUnionParam{fileRead, grep}
}

func (a *anthropicAgent) Review(ctx stdctx.Context, rc Context) (engine.ReviewOutput, error) {
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
	system := reviewSystemPrompt(rc.OperatorPrompt)
	rc.Trace.SetSystemPrompt(system)
	rc.Trace.SetModel("anthropic", a.model)
	rc.Trace.SetPrompt(userPrompt)
	params := anthropic.MessageNewParams{
		MaxTokens: maxTokens,
		Model:     anthropic.Model(a.model),
		System:    []anthropic.TextBlockParam{{Text: system}},
		Tools:     reviewTools(),
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(userPrompt)),
		},
	}
	// Extended thinking (when the model supports it) deepens analysis but REQUIRES
	// temperature unset (Claude rejects temperature with thinking), and max_tokens
	// must exceed the thinking budget. Otherwise apply the configured temperature.
	if wantOn, effort := thinkingSetting(a.thinking); wantOn && supportsAnthropicThinking(a.model) {
		budget := anthropicThinkingBudget(effort)
		params.Thinking = anthropic.ThinkingConfigParamOfEnabled(budget)
		params.MaxTokens = budget + maxTokens
	} else {
		params.Temperature = anthropic.Float(a.temperature)
	}

	emptyRounds := 0
	var usage engine.Usage
	for turn := 0; turn < maxToolTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return engine.ReviewOutput{}, err
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
			return engine.ReviewOutput{}, classifyAnthropicErr(err)
		}
		usage.InputTokens += msg.Usage.InputTokens
		usage.OutputTokens += msg.Usage.OutputTokens
		usage.CacheReadTokens += msg.Usage.CacheReadInputTokens
		usage.CacheCreationTokens += msg.Usage.CacheCreationInputTokens
		params.Messages = append(params.Messages, msg.ToParam())

		toolResults, finalText := a.dispatch(ctx, rc, turn, msg)
		if len(toolResults) == 0 {
			if out, ok := parseFindings(finalText); ok {
				out.Usage = usage
				rc.Trace.SetFinalResponse(finalText)
				return out, nil
			}
			emptyRounds++
			if emptyRounds >= maxEmptyRounds {
				return engine.ReviewOutput{}, fmt.Errorf("agent: model produced no tool calls and no parseable findings after %d rounds", emptyRounds)
			}
			params.Messages = append(params.Messages, anthropic.NewUserMessage(anthropic.NewTextBlock(
				"You did not call a tool and did not return valid findings JSON. Reply with ONLY the JSON object {\"findings\":[...]} as specified, no prose, no markdown fences.")))
			continue
		}
		emptyRounds = 0
		params.Messages = append(params.Messages, anthropic.NewUserMessage(toolResults...))
	}
	return engine.ReviewOutput{}, fmt.Errorf("agent: forced finalization produced no parseable findings after %d turns", maxToolTurns)
}

// RepairPatch issues one tools-less, code-only completion (system =
// repairSystemPrompt, user = BuildRepairPrompt) and returns the fence-stripped,
// trimmed reply. Reuses the same creds/client as Review; the ctx deadline owns
// the wall clock.
func (a *anthropicAgent) RepairPatch(ctx stdctx.Context, rr RepairRequest) (string, engine.Usage, error) {
	if a.timeout > 0 {
		var cancel stdctx.CancelFunc
		ctx, cancel = stdctx.WithTimeout(ctx, a.timeout)
		defer cancel()
	}
	msg, err := a.client.newMessage(ctx, anthropic.MessageNewParams{
		MaxTokens:   repairMaxTokens,
		Temperature: anthropic.Float(a.temperature),
		Model:       anthropic.Model(a.model),
		System:      []anthropic.TextBlockParam{{Text: repairSystemPrompt}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(BuildRepairPrompt(rr))),
		},
	})
	if err != nil {
		return "", engine.Usage{}, classifyAnthropicErr(err)
	}
	var text strings.Builder
	for _, block := range msg.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	u := engine.Usage{
		InputTokens:         msg.Usage.InputTokens,
		OutputTokens:        msg.Usage.OutputTokens,
		CacheReadTokens:     msg.Usage.CacheReadInputTokens,
		CacheCreationTokens: msg.Usage.CacheCreationInputTokens,
	}
	return parseRepairReply(text.String()), u, nil
}

// parseRepairReply fence-strips the model reply and trims it consistently with
// isCleanReplacement's own trimming so the re-validation gate sees identical
// bytes. Empty after strip => no usable replacement.
func parseRepairReply(reply string) string {
	return strings.TrimRight(stripMarkdownFences(reply), "\r")
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
// (range omitted when unset). Paths/line numbers only, never secrets.
func fileReadLabel(a fileReadArgs) string {
	if a.Start <= 0 {
		return a.File // no usable start (incl. line 0, which doesn't exist) → just the path
	}
	if a.End <= 0 {
		return fmt.Sprintf("%s:%d", a.File, a.Start)
	}
	return fmt.Sprintf("%s:%d-%d", a.File, a.Start, a.End)
}

func grepLabel(a grepArgs) string {
	if strings.TrimSpace(a.File) == "" {
		return a.Pattern
	}
	return fmt.Sprintf("%s in %s", a.Pattern, a.File)
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
		label := grepLabel(args)
		rc.progress("→ grep " + label)
		rc.Trace.RecordTool(turn, "grep", label)
		out, err := enginectx.Grep(ctx, rc.RepoDir, rc.Rev, args.Pattern, rc.Runner, args.File)
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

// parseFindings strips markdown fences and unmarshals the model's JSON into a
// ReviewOutput: findings (carrying severity/category/quoted-code and NO line
// numbers) plus the optional, length-capped walkthrough/file-digest. Untrusted
// text is preserved verbatim here; escaping happens at render, not at parse.
func parseFindings(text string) (engine.ReviewOutput, bool) {
	body := stripMarkdownFences(text)
	if body == "" {
		return engine.ReviewOutput{}, false
	}
	var raw rawFindings
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return engine.ReviewOutput{}, false
	}
	findings := make([]engine.Finding, 0, len(raw.Findings))
	for _, r := range raw.Findings {
		findings = append(findings, engine.Finding{
			File:           r.File,
			Title:          capRunes(r.Title, maxTitleLen),
			Rule:           capRunes(strings.TrimSpace(r.Rule), maxRuleLen),
			Severity:       r.Severity,
			Category:       r.Category,
			Rationale:      r.Rationale,
			SuggestedPatch: r.SuggestedPatch,
			QuotedCode:     r.ExistingCode,
		})
	}
	out := engine.ReviewOutput{
		Findings:         findings,
		Walkthrough:      capProse(raw.Walkthrough, maxWalkthroughLen),
		Diagram:          capRunes(raw.Diagram, maxDiagramLen),
		Confidence:       clampConfidence(raw.Confidence),
		ConfidenceReason: capRunes(raw.ConfidenceReason, maxConfidenceLen),
	}
	if len(raw.FileSummaries) > 0 {
		// Sort before truncating: Go map iteration order is randomized, so an
		// unsorted cap at maxFileSummaryKeys would keep a different subset each
		// run, breaking idempotent re-reviews.
		keys := make([]string, 0, len(raw.FileSummaries))
		for k := range raw.FileSummaries {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) > maxFileSummaryKeys {
			keys = keys[:maxFileSummaryKeys]
		}
		fs := make(map[string]string, len(keys))
		for _, k := range keys {
			fs[k] = capRunes(raw.FileSummaries[k], maxFileSummaryLen)
		}
		out.FileSummaries = fs
	}
	return out, true
}
