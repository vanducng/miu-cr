package agent

import "strings"

// thinkingSetting parses the [review].thinking value into (wantOn, effort).
// "off" disables; "low"/"medium"/"high" force a level; "" or "auto" means
// auto-on at medium (still gated per backend on whether the model supports it).
func thinkingSetting(s string) (wantOn bool, effort string) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off":
		return false, ""
	case "low", "medium", "high":
		return true, strings.ToLower(strings.TrimSpace(s))
	default: // "" / "auto"
		return true, "medium"
	}
}

// anthropicThinkingBudget maps an effort level to Claude extended-thinking
// budget_tokens. The request's max_tokens must exceed this (the caller bumps it).
func anthropicThinkingBudget(effort string) int64 {
	switch effort {
	case "low":
		return 4096
	case "high":
		return 24000
	default: // medium
		return 10000
	}
}

// supportsAnthropicThinking reports whether the Claude model supports extended
// thinking. Deliberately conservative (a known thinking-capable Claude family),
// so a non-supporting model — or a non-Claude gateway routed through the
// anthropic kind, e.g. glm — never gets a thinking block it would reject.
func supportsAnthropicThinking(model string) bool {
	m := strings.ToLower(model)
	if !strings.Contains(m, "claude") {
		return false
	}
	for _, fam := range []string{"sonnet-4", "opus-4", "haiku-4", "3-7-sonnet", "claude-3-7"} {
		if strings.Contains(m, fam) {
			return true
		}
	}
	return false
}

// isOpenAIReasoningModel reports whether an openai-kind model is a reasoning
// model (o-series / gpt-5), which uses reasoning_effort and rejects
// temperature != 1 on Chat Completions.
func isOpenAIReasoningModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if strings.Contains(m, "gpt-5-chat") { // the non-reasoning ChatGPT variant
		return false
	}
	return strings.HasPrefix(m, "o1") || strings.HasPrefix(m, "o3") ||
		strings.HasPrefix(m, "o4") || strings.HasPrefix(m, "gpt-5")
}
