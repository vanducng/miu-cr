package agent

import (
	"strings"
	"testing"
)

func TestParseReplyVerdict(t *testing.T) {
	got, err := ParseReplyVerdict("```json\n{\"accept\":true,\"explanation\":\"the bound is fixed\"}\n```")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Accept || got.Explanation != "the bound is fixed" {
		t.Fatalf("verdict = %+v", got)
	}
	if _, err := ParseReplyVerdict("not json"); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestBuildReplyJudgePromptStaysScoped(t *testing.T) {
	prompt := BuildReplyJudgePrompt(ReplyJudgeRequest{
		Intent: "defer", Path: "a.go", Line: 4, Title: "bounds", Reply: "Deferred: tracked in #4",
	})
	if strings.Contains(prompt, "findings") && strings.Contains(prompt, "walkthrough") {
		t.Fatal("prompt should judge one reply, not request a full review")
	}
	if !strings.Contains(prompt, "a.go") || !strings.Contains(prompt, "tracked in #4") {
		t.Fatalf("prompt = %s", prompt)
	}
}
