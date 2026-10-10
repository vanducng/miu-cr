package serve

import (
	"sync"
	"time"
)

// ReplySpendPerHour is the most reply judgments one pull request may start in an hour.
const ReplySpendPerHour = 24

type replyWindow struct {
	start time.Time
	n     int
}

var (
	replyBudgetMu sync.Mutex
	replyBudgets  = map[string]*replyWindow{}
)

// AllowReplySpend reports whether this pull request may start another reply judgment.
func AllowReplySpend(ref string, now time.Time) bool {
	if ref == "" {
		return false
	}
	replyBudgetMu.Lock()
	defer replyBudgetMu.Unlock()
	for k, v := range replyBudgets {
		if now.Sub(v.start) >= 2*time.Hour {
			delete(replyBudgets, k)
		}
	}
	b := replyBudgets[ref]
	if b == nil || now.Sub(b.start) >= time.Hour {
		replyBudgets[ref] = &replyWindow{start: now, n: 1}
		return true
	}
	if b.n >= ReplySpendPerHour {
		return false
	}
	b.n++
	return true
}
