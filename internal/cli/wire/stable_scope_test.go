package wire

import (
	"strings"
	"testing"

	mgithub "github.com/vanducng/miu-cr/internal/github"
)

func TestFollowUpInstructionOnlyOnLaterReview(t *testing.T) {
	info := &mgithub.PRInfo{HeadSHA: "bbbbbbb", PriorLedger: []mgithub.LedgerEntry{{FP: "aa"}}}
	got := followUpInstruction("focus on auth", "aaaaaaa", info)
	if !strings.Contains(got, "already reviewed at aaaaaaa") || !strings.HasSuffix(got, "focus on auth") {
		t.Fatalf("follow-up instruction = %q", got)
	}
	if got := followUpInstruction("focus on auth", "", info); got != "focus on auth" {
		t.Fatalf("first review changed instruction: %q", got)
	}
	if got := followUpInstruction("focus on auth", "bbbbbbb", info); got != "focus on auth" {
		t.Fatalf("same head changed instruction: %q", got)
	}
	empty := &mgithub.PRInfo{HeadSHA: "bbbbbbb"}
	if got := followUpInstruction("focus on auth", "aaaaaaa", empty); got != "focus on auth" {
		t.Fatalf("no ledger changed instruction: %q", got)
	}
}
