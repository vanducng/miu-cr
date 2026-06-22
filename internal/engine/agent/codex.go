package agent

import (
	"bytes"
	stdctx "context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

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
}

type codexItem struct {
	Type    string         `json:"type,omitempty"`
	Role    string         `json:"role,omitempty"`
	Content []codexContent `json:"content,omitempty"`
	// function_call_output items:
	CallID string `json:"call_id,omitempty"`
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
type codexResp struct {
	Output []struct {
		Type      string `json:"type"`
		Name      string `json:"name"`
		CallID    string `json:"call_id"`
		Arguments string `json:"arguments"`
		Content   []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
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
				},
				"required": []string{"pattern"},
			},
		},
	}
}

func (a *codexAgent) Review(ctx stdctx.Context, rc Context) ([]engine.Finding, error) {
	if a.timeout > 0 {
		var cancel stdctx.CancelFunc
		ctx, cancel = stdctx.WithTimeout(ctx, a.timeout)
		defer cancel()
	}
	if rc.Runner == nil {
		rc.Runner = gitcmd.New()
	}

	input := []codexItem{{
		Type: "message",
		Role: "user",
		Content: []codexContent{{
			Type: "input_text",
			Text: BuildUserPrompt(PromptParts{Rules: rc.Rules, SemanticContext: rc.SemanticContext, Diff: rc.Text}),
		}},
	}}

	emptyRounds := 0
	for turn := 0; turn < maxToolTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		req := codexReq{
			Model:        a.model,
			Instructions: systemPrompt,
			Input:        input,
			Tools:        codexTools(),
			Store:        false,
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
			return nil, err
		}

		toolItems, finalText := a.dispatch(ctx, rc, resp)
		if len(toolItems) == 0 {
			if findings, ok := parseFindings(finalText); ok {
				return findings, nil
			}
			emptyRounds++
			if emptyRounds >= maxEmptyRounds {
				return nil, fmt.Errorf("agent: model produced no tool calls and no parseable findings after %d rounds", emptyRounds)
			}
			input = append(input, codexItem{Type: "message", Role: "user",
				Content: []codexContent{{Type: "input_text", Text: "You did not call a tool and did not return valid findings JSON. Reply with ONLY the JSON object {\"findings\":[...]} as specified, no prose, no markdown fences."}}})
			continue
		}
		emptyRounds = 0
		input = append(input, toolItems...)
	}
	return nil, fmt.Errorf("agent: forced finalization produced no parseable findings after %d turns", maxToolTurns)
}

// dispatch runs every function_call in resp, returning function_call_output
// items (echoing back the assistant's function_call first, per the Responses
// protocol) and the concatenated assistant text (candidate final answer).
func (a *codexAgent) dispatch(ctx stdctx.Context, rc Context, resp *codexResp) ([]codexItem, string) {
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
			out, isErr := runTool(ctx, rc, o.Name, json.RawMessage(o.Arguments))
			if isErr {
				out = "ERROR: " + out
			}
			items = append(items,
				codexItem{Type: "function_call", CallID: o.CallID, Output: ""},
				codexItem{Type: "function_call_output", CallID: o.CallID, Output: out},
			)
		}
	}
	return items, text.String()
}

// post sends one Responses request, retrying once on a 401 after a token
// refresh. The response body is never logged; errors carry only a status code.
func (a *codexAgent) post(ctx stdctx.Context, body codexReq) (*codexResp, error) {
	resp, err := a.do(ctx, body, a.token)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized && a.refresh != nil {
		resp.Body.Close()
		tok, rerr := a.refresh(ctx)
		if rerr != nil {
			return nil, fmt.Errorf("agent: codex auth refresh failed: %w", rerr)
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
		return nil, fmt.Errorf("agent: codex backend status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out codexResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("agent: decode codex response: %w", err)
	}
	return &out, nil
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
	req.Header.Set("Accept", "application/json")
	// The codex backend gates which models a ChatGPT account may use by the
	// originator; without it every model is rejected as "not supported".
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("User-Agent", "miucr (+https://github.com/vanducng/miu-cr)")
	return a.httpClient.Do(req)
}
