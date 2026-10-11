package agent

import (
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

func TestFitAnthropicPromptKeepsShortTranscript(t *testing.T) {
	messages := []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock("diff")),
	}
	fitted := fitAnthropicPrompt("sys", messages, 1000)
	if fitted[0].Content[0].OfText != messages[0].Content[0].OfText {
		t.Fatal("under the cap should keep the original blocks")
	}
}

func TestFitAnthropicPromptStubsOldestToolOutput(t *testing.T) {
	big := strings.Repeat("x", 5000)
	messages := []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock("diff")),
		anthropic.NewUserMessage(
			anthropic.NewToolResultBlock("old", big, false),
			anthropic.NewToolResultBlock("new", "recent", false),
		),
	}
	limit := anthropicPromptRunes("sys", messages) - 1000
	fitted := fitAnthropicPrompt("sys", messages, limit)

	if got := messages[1].Content[0].OfToolResult.Content[0].OfText.Text; got != big {
		t.Fatal("fitting should not rewrite the stored transcript")
	}
	if got := toolResultText(&fitted[1].Content[0]); got != omittedToolOutput {
		t.Fatalf("oldest tool output = %q", got)
	}
	if got := toolResultText(&fitted[1].Content[1]); got != "recent" {
		t.Fatalf("newest tool output = %q", got)
	}
	if anthropicPromptRunes("sys", fitted) > limit {
		t.Fatalf("fitted size %d over limit %d", anthropicPromptRunes("sys", fitted), limit)
	}
}

func TestFitAnthropicPromptTruncatesOversizedUserText(t *testing.T) {
	messages := []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock(strings.Repeat("y", 10000))),
	}
	const limit = 2000
	fitted := fitAnthropicPrompt("sys", messages, limit)
	if anthropicPromptRunes("sys", fitted) > limit {
		t.Fatalf("fitted size %d over limit %d", anthropicPromptRunes("sys", fitted), limit)
	}
	if !strings.HasSuffix(fitted[0].Content[0].OfText.Text, "[truncated]") {
		t.Fatal("oversized user text should be truncated")
	}
}
