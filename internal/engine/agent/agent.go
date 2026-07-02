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

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
	enginetools "github.com/vanducng/miu-cr/internal/engine/tools"
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
	// PromptFormat selects the prompt serialization: "xml" → XML-tagged; "" or "markdown"
	// → fenced. The product default is xml (Engine.Review resolves "" → "xml" before this,
	// so "" reaches here only in direct unit tests). LOCKSTEP: mirror in ALL backends.
	PromptFormat string
	// OperatorPrompt is trusted host policy. LOCKSTEP: mirror Conversation in ALL backends.
	OperatorPrompt string
	ProviderRetry  config.ProviderRetry
	Tools          config.ReviewTools
	SymbolContext  config.SymbolContext
	RepoDir        string
	Rev            string
	Runner         *gitcmd.Runner
	Progress       func(string) // nil = silent; milestone/tool strings only, never secrets
	// Trace, when non-nil, accumulates the raw prompt, per-turn tool calls/results, and
	// raw final response for persistence. nil = no capture (mirrors Progress).
	Trace *engine.ReviewTrace
	// CaptureReasoning, when true, records thinking blocks into Trace.Reasoning.
	// Only fires when [review].thinking produced blocks; off = byte-identical.
	CaptureReasoning bool
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
	defaultMaxToolTurns = 24
	maxEmptyRounds      = 3
	maxTokens           = 8192

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
	specs := enginetools.Specs()
	out := make([]anthropic.ToolUnionParam, 0, len(specs))
	for _, spec := range specs {
		schema := anthropic.ToolInputSchemaParam{Properties: spec.Properties, Required: spec.Required}
		tool := anthropic.ToolUnionParamOfTool(schema, spec.Name)
		tool.OfTool.Description = anthropic.String(spec.Description)
		out = append(out, tool)
	}
	return out
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

	userPrompt := BuildUserPrompt(PromptParts{Rules: rc.Rules, SemanticContext: rc.SemanticContext, ProjectContext: rc.ProjectContext, RelatedContext: rc.RelatedContext, WantDiagram: rc.WantDiagram, Instruction: rc.Instruction, Conversation: rc.Conversation, Diff: rc.Text, Format: rc.PromptFormat})
	system := reviewSystemPrompt(rc.PromptFormat, rc.OperatorPrompt)
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
	maxTurns := toolTurns(rc.Tools)
	// Loop past maxTurns by maxEmptyRounds: once tools are withdrawn on turn
	// maxTurns-1 the forced finalize gets the same bounded JSON-repair rounds an
	// interior turn gets (params.Tools stays nil), rather than discarding the whole
	// attempt to a full retry on one non-JSON reply.
	for turn := 0; turn < maxTurns+maxEmptyRounds; turn++ {
		if err := ctx.Err(); err != nil {
			return engine.ReviewOutput{}, err
		}
		rc.progress(fmt.Sprintf("thinking… (turn %d)", turn+1))

		// Final allowed turn: withdraw the tools so the model can no longer keep
		// exploring and must answer, and fold the finalize nudge into the trailing
		// user turn (the loop invariant guarantees the last message is user-role,
		// so this avoids an illegal consecutive user message).
		if turn == maxTurns-1 {
			params.Tools = nil
			last := len(params.Messages) - 1
			params.Messages[last].Content = append(params.Messages[last].Content, anthropic.NewTextBlock(forceFinalizeNudge))
		}

		msg, err := retryProviderCall(ctx, rc.ProviderRetry, rc.Progress, "anthropic.messages", func() (*anthropic.Message, error) {
			return a.client.newMessage(ctx, params)
		}, classifyAnthropicErr, anthropicProviderRetryable)
		if err != nil {
			return engine.ReviewOutput{}, err
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
	return engine.ReviewOutput{}, fmt.Errorf("agent: forced finalization produced no parseable findings after %d turns", maxTurns)
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
	msg, err := retryProviderCall(ctx, rr.ProviderRetry, nil, "anthropic.repair", func() (*anthropic.Message, error) {
		return a.client.newMessage(ctx, anthropic.MessageNewParams{
			MaxTokens:   repairMaxTokens,
			Temperature: anthropic.Float(a.temperature),
			Model:       anthropic.Model(a.model),
			System:      []anthropic.TextBlockParam{{Text: repairSystemPrompt}},
			Messages: []anthropic.MessageParam{
				anthropic.NewUserMessage(anthropic.NewTextBlock(BuildRepairPrompt(rr))),
			},
		})
	}, classifyAnthropicErr, anthropicProviderRetryable)
	if err != nil {
		return "", engine.Usage{}, err
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
	var thinkingText strings.Builder
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "thinking":
			if rc.CaptureReasoning {
				thinkingText.WriteString(block.Thinking)
			}
		case "tool_use":
			out, isErr := runTool(ctx, rc, turn, block.Name, block.Input)
			results = append(results, anthropic.NewToolResultBlock(block.ID, out, isErr))
		}
	}
	if rc.CaptureReasoning && thinkingText.Len() > 0 {
		rc.Trace.SetReasoning("anthropic", thinkingText.String(), 0)
	}
	// When tools were called, text is the model's prose about why — capture it
	// per turn (the loop discards it otherwise). A tools-free turn's text is the
	// final answer, handled by the caller, so it is not a turn reason.
	if len(results) > 0 && text.Len() > 0 {
		rc.Trace.RecordTurnReason(turn, text.String())
	}
	return results, text.String()
}

// runTool executes one tool against the reviewed revision. Provider-agnostic so
// all agent loops share it (and record the dispatch into the trace). Returns
// (content, isError).
var executeTool = enginetools.Execute

func runTool(ctx stdctx.Context, rc Context, turn int, name string, input json.RawMessage) (string, bool) {
	attempts := toolAttempts(rc.Tools)
	backoff := toolRetryBackoff(rc.Tools)
	var last string
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err.Error(), true
		}
		out, isErr := executeTool(ctx, rc.SymbolContext, toolContext(rc), turn, name, input)
		if err := ctx.Err(); err != nil {
			return err.Error(), true
		}
		if !isErr || !retryToolError(out) || attempt == attempts {
			return out, isErr
		}
		last = out
		if rc.Progress != nil {
			rc.Progress(fmt.Sprintf("→ %s retry %d/%d after transient error", name, attempt, attempts-1))
		}
		if err := sleepCtx(ctx, backoffForAttempt(backoff, attempt)); err != nil {
			return err.Error(), true
		}
	}
	return last, true
}

func toolAttempts(tools config.ReviewTools) int {
	retries := 2
	if tools.MaxRetries != nil {
		retries = *tools.MaxRetries
	}
	if retries < 0 {
		retries = 0
	}
	if retries > 5 {
		retries = 5
	}
	return retries + 1
}

func toolTurns(tools config.ReviewTools) int {
	if tools.MaxTurns == nil || *tools.MaxTurns <= 0 {
		return defaultMaxToolTurns
	}
	return *tools.MaxTurns
}

func toolRetryBackoff(tools config.ReviewTools) time.Duration {
	if tools.RetryBackoff != "" {
		if d, err := time.ParseDuration(tools.RetryBackoff); err == nil && d >= 0 {
			return d
		}
	}
	return 250 * time.Millisecond
}

func backoffForAttempt(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		return 0
	}
	d := base << (attempt - 1)
	if d > 2*time.Second {
		return 2 * time.Second
	}
	return d
}

func retryToolError(out string) bool {
	s := strings.ToLower(out)
	if strings.Contains(s, "invalid arguments") ||
		strings.Contains(s, "requires a non-empty") ||
		strings.Contains(s, "unknown tool") ||
		strings.Contains(s, "not a directory") ||
		strings.Contains(s, "does not exist") ||
		strings.Contains(s, "no such file") ||
		strings.Contains(s, "bad revision") ||
		strings.Contains(s, "ambiguous argument") {
		return false
	}
	for _, marker := range []string{
		"temporary failure",
		"resource temporarily unavailable",
		"i/o timeout",
		"operation timed out",
		"connection reset",
		"connection refused",
		"broken pipe",
		"device or resource busy",
		"text file busy",
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

func toolContext(rc Context) enginetools.Context {
	return enginetools.Context{
		RepoDir:  rc.RepoDir,
		Rev:      rc.Rev,
		Runner:   rc.Runner,
		Progress: rc.Progress,
		Trace:    rc.Trace,
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
	if len(raw.Findings) > maxFindings {
		raw.Findings = raw.Findings[:maxFindings]
	}
	findings := make([]engine.Finding, 0, len(raw.Findings))
	for _, r := range raw.Findings {
		findings = append(findings, engine.Finding{
			File:           capRunes(r.File, maxFilePathLen),
			Title:          capRunes(r.Title, maxTitleLen),
			Rule:           capRunes(strings.TrimSpace(r.Rule), maxRuleLen),
			Severity:       capRunes(r.Severity, maxSeverityLen),
			Category:       capRunes(r.Category, maxCategoryLen),
			Rationale:      capProse(r.Rationale, maxRationaleLen),
			SuggestedPatch: capRunes(r.SuggestedPatch, maxPatchLen),
			QuotedCode:     capRunes(r.ExistingCode, maxQuotedCodeLen),
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
