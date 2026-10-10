package serve

import (
	"testing"
	"time"
)

func TestAllowReplySpendCapsPerPullRequest(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	ref := "acme/app#9"
	for i := 0; i < ReplySpendPerHour; i++ {
		if !AllowReplySpend(ref, now) {
			t.Fatalf("spend %d should be allowed", i+1)
		}
	}
	if AllowReplySpend(ref, now) {
		t.Fatal("spend past the hourly cap should wait")
	}
	if !AllowReplySpend("acme/app#10", now) {
		t.Fatal("a different pull request has its own cap")
	}
	if !AllowReplySpend(ref, now.Add(time.Hour)) {
		t.Fatal("the cap should reset after an hour")
	}
}
