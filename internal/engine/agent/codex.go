package agent

import (
	"bufio"
	"bytes"
	stdctx "context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

// codexAgent speaks the codex backend (chatgpt.com/backend-api/codex) Responses
// protocol with an OAuth Bearer token, so reviews run on the user's ChatGPT
// plan. The request shape is reverse-engineered from the codex CLI and verified
// only against the fake in codex_test.go; the LIVE call is smoke-gated. It
// implements the same Agent interface and emits the identical findings contract
// as the Anthropic/OpenAI paths.
type codexAgent struct {
	httpClient *http.Client
	backendURL string // <BackendBaseURL>/responses
	token      string
	accountID  string
	model      string
	timeout    time.Duration
	refresh    func(ctx stdctx.Context) (string, error) // 401 -> new access token
}

// classifyCodexStatus types a proven codex backend status into the stable
// taxonomy (OAuth ⇒ auth_expired on 401/403). Returns nil for an unclassified
// status so the caller keeps its bare %w error. msg is redacted defensively.
func classifyCodexStatus(status int, msg string) *clierr.CLIError {
	return classifyStatus(status, msg, hintLoginOpenAI, codeAuthExpired)
}

func newCodexAgent(creds Credentials, timeout time.Duration) *codexAgent {
	hc := creds.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	return &codexAgent{
		httpClient: hc,
		backendURL: strings.TrimRight(creds.BaseURL, "/") + "/responses",
		token:      creds.OAuthToken,
		accountID:  creds.OAuthAccountID,
		model:      creds.Model,
		timeout:    timeout,
		refresh:    creds.OAuthRefresh,
	}
}

// codexReq is the Responses API request body. store:false keeps the call
// stateless (no server-side session); tools mirror the Anthropic/OpenAI loop.
type codexReq struct {
	Model        string      `json:"model"`
	Instructions string      `json:"instructions"`
	Input        []codexItem `json:"input"`
	Tools        []codexTool `json:"tools,omitempty"`
	Store        bool        `json:"store"`
	// The codex backend requires stream:true ("Stream must be set to true"); the
	// response is an SSE stream parsed in post().
	Stream bool `json:"stream"`
}

type codexItem struct {
	Type    string         `json:"type,omitempty"`
	Role    string         `json:"role,omitempty"`
	Content []codexContent `json:"content,omitempty"`
	// function_call echo (store:false ⇒ we resend the assistant's call so the
	// server has it): needs name + arguments, not just call_id.
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	// function_call_output items:
	Output string `json:"output,omitempty"`
}

type codexContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type codexTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// codexResp is the subset of the Responses output we parse: assistant text and
// function_call items (the tool loop).
type codexOutItem struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	CallID    string `json:"call_id"`
	Arguments string `json:"arguments"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

type codexResp struct {
	Status string `json:"status"`
	Error  *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
	Output []codexOutItem `json:"output"`
}

func codexTools() []codexTool {
	return []codexTool{
		{
			Type:        "function",
			Name:        "file_read",
			Description: "Read a line range of a file at the reviewed revision.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"file":  map[string]any{"type": "string", "description": "path to read"},
					"start": map[string]any{"type": "integer", "description": "1-based start line"},
					"end":   map[string]any{"type": "integer", "description": "1-based end line"},
				},
				"required": []string{"file"},
			},
		},
		{
			Type:        "function",
			Name:        "grep",
			Description: "Search the reviewed revision for a fixed string.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{"type": "string", "description": "fixed string to search for"},
					"file":    map[string]any{"type": "string", "description": "optional file path to limit the search"},
				},
				"required": []string{"pattern"},
			},
		},
	}
}

func (a *codexAgent) Review(ctx stdctx.Context, rc Context) (engine.ReviewOutput, error) {
	if a.timeout > 0 {
		var cancel stdctx.CancelFunc
		ctx, cancel = stdctx.WithTimeout(ctx, a.timeout)
		defer cancel()
	}
	if rc.Runner == nil {
		rc.Runner = gitcmd.New()
	}

	userPrompt := BuildUserPrompt(PromptParts{Rules: rc.Rules, SemanticContext: rc.SemanticContext, WantDiagram: rc.WantDiagram, Diff: rc.Text})
	rc.Trace.SetSystemPrompt(systemPrompt)
	rc.Trace.SetModel("codex", a.model)
	rc.Trace.SetPrompt(userPrompt)
	input := []codexItem{{
		Type: "message",
		Role: "user",
		Content: []codexContent{{
			Type: "input_text",
			Text: userPrompt,
		}},
	}}

	emptyRounds := 0
	for turn := 0; turn < maxToolTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return engine.ReviewOutput{}, err
		}
		rc.progress(fmt.Sprintf("thinking… (turn %d)", turn+1))
		req := codexReq{
			Model:        a.model,
			Instructions: systemPrompt,
			Input:        input,
			Tools:        codexTools(),
			Store:        false,
			Stream:       true,
		}
		// Final allowed turn: withdraw tools and force a finalize so a
		// budget-exhausted diff yields a real review, not a hard failure.
		if turn == maxToolTurns-1 {
			req.Tools = nil
			input = append(input, codexItem{Type: "message", Role: "user",
				Content: []codexContent{{Type: "input_text", Text: forceFinalizeNudge}}})
			req.Input = input
		}

		resp, err := a.post(ctx, req)
		if err != nil {
			return engine.ReviewOutput{}, err
		}

		toolItems, finalText := a.dispatch(ctx, rc, turn, resp)
		if len(toolItems) == 0 {
			if out, ok := parseFindings(finalText); ok {
				rc.Trace.SetFinalResponse(finalText)
				return out, nil
			}
			emptyRounds++
			if emptyRounds >= maxEmptyRounds {
				return engine.ReviewOutput{}, fmt.Errorf("agent: model produced no tool calls and no parseable findings after %d rounds", emptyRounds)
			}
			input = append(input, codexItem{Type: "message", Role: "user",
				Content: []codexContent{{Type: "input_text", Text: "You did not call a tool and did not return valid findings JSON. Reply with ONLY the JSON object {\"findings\":[...]} as specified, no prose, no markdown fences."}}})
			continue
		}
		emptyRounds = 0
		input = append(input, toolItems...)
	}
	return engine.ReviewOutput{}, fmt.Errorf("agent: forced finalization produced no parseable findings after %d turns", maxToolTurns)
}

// dispatch runs every function_call in resp, returning function_call_output
// items (echoing back the assistant's function_call first, per the Responses
// protocol) and the concatenated assistant text (candidate final answer).
func (a *codexAgent) dispatch(ctx stdctx.Context, rc Context, turn int, resp *codexResp) ([]codexItem, string) {
	var items []codexItem
	var text strings.Builder
	for _, o := range resp.Output {
		switch o.Type {
		case "message":
			for _, c := range o.Content {
				if c.Type == "output_text" || c.Type == "text" {
					text.WriteString(c.Text)
				}
			}
		case "function_call":
			out, isErr := runTool(ctx, rc, turn, o.Name, json.RawMessage(o.Arguments))
			if isErr {
				out = "ERROR: " + out
			}
			items = append(items,
				codexItem{Type: "function_call", CallID: o.CallID, Name: o.Name, Arguments: o.Arguments},
				codexItem{Type: "function_call_output", CallID: o.CallID, Output: out},
			)
		}
	}
	return items, text.String()
}

// post sends one Responses request with bounded jittered backoff on a transient
// failure (429/502/503/504 or a response.failed SSE event), matching the
// retry the SDK backends already get. The sleep is always gated on ctx so a
// cancel/deadline aborts promptly; on give-up a 429 surfaces the usage-cap reset
// window. The response body is never logged; errors carry only a status code.
func (a *codexAgent) post(ctx stdctx.Context, body codexReq) (*codexResp, error) {
	var lastTransient *codexRetryable
	for attempt := 1; attempt <= codexMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		resp, err := a.postOnce(ctx, body)
		if err == nil {
			return resp, nil
		}
		var tr *codexRetryable
		if !errors.As(err, &tr) {
			return nil, err // ctx error or a terminal typed/bare error
		}
		lastTransient = tr
		if attempt == codexMaxAttempts {
			break
		}
		var suggested time.Duration
		if tr.retryAfter > 0 {
			suggested = tr.retryAfter
		} else if tr.resetsIn > 0 {
			suggested = tr.resetsIn
		}
		if serr := sleepCtx(ctx, codexBackoff(attempt, suggested)); serr != nil {
			return nil, serr
		}
	}
	if lastTransient.status == http.StatusTooManyRequests {
		ce := lastTransient.rateLimitError()
		ce.Cause = lastTransient
		return nil, ce
	}
	if c := classifyCodexStatus(lastTransient.status, lastTransient.msg); c != nil {
		c.Cause = lastTransient // preserve the transient chain for errors.Is/As
		return nil, c
	}
	return nil, fmt.Errorf("agent: %s", lastTransient.msg)
}

// postOnce performs a single Responses request, retrying once on a 401 after a
// token refresh. A transient failure (retryable status or response.failed)
// returns a *codexRetryable for the post() loop; everything else is terminal.
func (a *codexAgent) postOnce(ctx stdctx.Context, body codexReq) (*codexResp, error) {
	resp, err := a.do(ctx, body, a.token)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized && a.refresh != nil {
		resp.Body.Close()
		tok, rerr := a.refresh(ctx)
		if rerr != nil {
			if isCtxErr(rerr) {
				return nil, fmt.Errorf("agent: codex auth refresh failed: %w", rerr)
			}
			// A transient refresh failure (network/DNS/5xx from the OAuth endpoint,
			// signaled via clierr.Retry) leaves the credential possibly valid — surface
			// it as retryable, not as a re-login prompt. A real rejection (invalid /
			// expired refresh token) stays auth_expired. Either way preserve rerr.
			var rce *clierr.CLIError
			if errors.As(rerr, &rce) && rce.Retry {
				return nil, &clierr.CLIError{
					Code:      codeUnavailable,
					Message:   config.RedactString("codex auth refresh failed: " + rerr.Error()),
					Hint:      "auth refresh temporarily failed — retry shortly",
					Exit:      1,
					Retry:     true,
					SafeRetry: true,
					Cause:     rerr,
				}
			}
			c := classifyCodexStatus(http.StatusUnauthorized, "codex auth refresh failed: "+rerr.Error())
			c.Cause = rerr
			return nil, c
		}
		a.token = tok
		resp, err = a.do(ctx, body, a.token)
		if err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		msg := config.RedactString(fmt.Sprintf("codex backend status %d: %s", resp.StatusCode, strings.TrimSpace(string(b))))
		if codexRetryStatus(resp.StatusCode) {
			return nil, &codexRetryable{
				status:     resp.StatusCode,
				resetsIn:   parseResetsIn(string(b)),
				retryAfter: parseRetryAfter(resp.Header),
				msg:        msg,
			}
		}
		if c := classifyCodexStatus(resp.StatusCode, msg); c != nil {
			return nil, c
		}
		return nil, fmt.Errorf("agent: %s", msg)
	}
	return parseCodexSSE(resp.Body)
}

// parseCodexSSE reads the Responses SSE stream and returns the final response
// object from the response.completed event. Deltas are ignored — only the
// terminal event carries the full output we parse. response.failed / an error
// event surfaces as a typed error.
func parseCodexSSE(r io.Reader) (*codexResp, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 32<<20) // events can be large (encrypted reasoning)
	var done []codexOutItem                     // accumulated from output_item.done events
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(line[len("data:"):])
		if data == "" || data == "[DONE]" {
			continue
		}
		var ev struct {
			Type     string        `json:"type"`
			Response *codexResp    `json:"response"`
			Item     *codexOutItem `json:"item"`
			Error    *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue // skip non-JSON keep-alives / partial frames
		}
		if ev.Error != nil {
			return nil, fmt.Errorf("agent: codex stream error: %s", ev.Error.Message)
		}
		switch ev.Type {
		case "response.output_item.done":
			if ev.Item != nil {
				done = append(done, *ev.Item)
			}
		case "response.completed", "response.incomplete":
			if ev.Response != nil {
				if ev.Response.Error != nil {
					return nil, fmt.Errorf("agent: codex response error: %s", ev.Response.Error.Message)
				}
				if len(ev.Response.Output) == 0 {
					ev.Response.Output = done // some streams deliver items only via output_item.done
				}
				return ev.Response, nil
			}
			return &codexResp{Output: done}, nil
		case "response.failed":
			msg := "codex response failed"
			var errType, errCode string
			if ev.Response != nil && ev.Response.Error != nil {
				errType, errCode = ev.Response.Error.Type, ev.Response.Error.Code
				msg = config.RedactString(fmt.Sprintf("codex response failed: %s", ev.Response.Error.Message))
			}
			// The Responses API emits response.failed for PERMANENT errors too
			// (invalid_request_error, content policy). Retrying those only wastes
			// attempts — surface them as terminal; only transient failures retry.
			if codexFailurePermanent(errType, errCode) {
				return nil, fmt.Errorf("agent: %s", msg)
			}
			return nil, &codexRetryable{failed: true, msg: msg}
		}
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, fmt.Errorf("agent: codex SSE event exceeded the read buffer (oversized reasoning content): %w", err)
		}
		return nil, fmt.Errorf("agent: read codex stream: %w", err)
	}
	return nil, fmt.Errorf("agent: codex stream ended without a completed response")
}

func (a *codexAgent) do(ctx stdctx.Context, body codexReq, token string) (*http.Response, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("agent: encode codex request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.backendURL, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("agent: build codex request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if a.accountID != "" {
		req.Header.Set("ChatGPT-Account-ID", a.accountID)
	}
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("Accept", "text/event-stream")
	// The codex backend gates which models a ChatGPT account may use by the
	// originator; without it every model is rejected as "not supported".
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("User-Agent", "miucr (+https://github.com/vanducng/miu-cr)")
	return a.httpClient.Do(req)
}
