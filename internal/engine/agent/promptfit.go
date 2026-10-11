package agent

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/anthropics/anthropic-sdk-go"
)

// flattenedPromptRunes is the tools-less send cap. Some agent gateways flatten
// that turn into one prompt and reject it past 80 * 1024 runes. 72000 leaves
// room for the role prefixes the flattener adds.
const flattenedPromptRunes = 72000

const omittedToolOutput = "[earlier tool output omitted]"

func fitAnthropicPrompt(system string, messages []anthropic.MessageParam, limit int) []anthropic.MessageParam {
	if limit <= 0 || anthropicPromptRunes(system, messages) <= limit {
		return messages
	}
	fitted := cloneAnthropicMessages(messages)
	for i := range fitted {
		for j := range fitted[i].Content {
			block := &fitted[i].Content[j]
			if block.OfToolResult == nil || toolResultText(block) == omittedToolOutput {
				continue
			}
			setToolResultText(block, omittedToolOutput)
			if anthropicPromptRunes(system, fitted) <= limit {
				return fitted
			}
		}
	}
	for i := range fitted {
		for j := range fitted[i].Content {
			text := textPtr(&fitted[i].Content[j])
			if text == nil {
				continue
			}
			excess := anthropicPromptRunes(system, fitted) - limit
			if excess <= 0 {
				return fitted
			}
			shrinkText(text, excess)
		}
	}
	return fitted
}

func anthropicPromptRunes(system string, messages []anthropic.MessageParam) int {
	n := utf8.RuneCountInString(system)
	for _, message := range messages {
		n += 48
		for _, block := range message.Content {
			n += 48 + blockRunes(block)
		}
	}
	return n
}

func blockRunes(block anthropic.ContentBlockParamUnion) int {
	switch {
	case block.OfText != nil:
		return utf8.RuneCountInString(block.OfText.Text)
	case block.OfToolResult != nil:
		n := 0
		for _, part := range block.OfToolResult.Content {
			if part.OfText != nil {
				n += utf8.RuneCountInString(part.OfText.Text)
			}
		}
		return n
	case block.OfToolUse != nil:
		raw, err := json.Marshal(block.OfToolUse.Input)
		if err != nil {
			return 0
		}
		return utf8.RuneCount(raw)
	default:
		return 0
	}
}

func cloneAnthropicMessages(messages []anthropic.MessageParam) []anthropic.MessageParam {
	out := make([]anthropic.MessageParam, len(messages))
	for i, message := range messages {
		out[i] = message
		out[i].Content = make([]anthropic.ContentBlockParamUnion, len(message.Content))
		for j, block := range message.Content {
			out[i].Content[j] = cloneBlock(block)
		}
	}
	return out
}

func cloneBlock(block anthropic.ContentBlockParamUnion) anthropic.ContentBlockParamUnion {
	if block.OfText != nil {
		text := *block.OfText
		block.OfText = &text
	}
	if block.OfToolResult != nil {
		result := *block.OfToolResult
		result.Content = make([]anthropic.ToolResultBlockParamContentUnion, len(block.OfToolResult.Content))
		for i, part := range block.OfToolResult.Content {
			result.Content[i] = part
			if part.OfText != nil {
				text := *part.OfText
				result.Content[i].OfText = &text
			}
		}
		block.OfToolResult = &result
	}
	return block
}

func toolResultText(block *anthropic.ContentBlockParamUnion) string {
	if block == nil || block.OfToolResult == nil {
		return ""
	}
	var text string
	for _, part := range block.OfToolResult.Content {
		if part.OfText != nil {
			text += part.OfText.Text
		}
	}
	return text
}

func setToolResultText(block *anthropic.ContentBlockParamUnion, text string) {
	block.OfToolResult.Content = []anthropic.ToolResultBlockParamContentUnion{{
		OfText: &anthropic.TextBlockParam{Text: text},
	}}
}

func textPtr(block *anthropic.ContentBlockParamUnion) *string {
	if block == nil || block.OfText == nil {
		return nil
	}
	return &block.OfText.Text
}

func shrinkText(text *string, excess int) {
	const marker = "\n[truncated]"
	runes := []rune(*text)
	markerRunes := utf8.RuneCountInString(marker)
	keep := len(runes) - excess - markerRunes
	if keep < 0 {
		keep = 0
	}
	*text = string(runes[:keep]) + marker
}
