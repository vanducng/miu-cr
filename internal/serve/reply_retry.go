package serve

import (
	"strconv"
	"sync"
	"time"
)

var replyRetry sync.Map

// ReplyRetryKey identifies one comment on one pull request for the host poll.
func ReplyRetryKey(ref string, commentID int64) string {
	return ref + ":" + strconv.FormatInt(commentID, 10)
}

// DeferReplyRetry keeps the host poll from re-queuing a comment until until.
func DeferReplyRetry(key string, until time.Time) {
	replyRetry.Store(key, until)
	sweepReplyRetry(time.Now())
}

// ReplyRetryReady reports whether a comment may be queued again.
func ReplyRetryReady(key string, now time.Time) bool {
	sweepReplyRetry(now)
	_, ok := replyRetry.Load(key)
	return !ok
}

func sweepReplyRetry(now time.Time) {
	replyRetry.Range(func(k, v any) bool {
		until, ok := v.(time.Time)
		if !ok || !now.Before(until) {
			replyRetry.Delete(k)
		}
		return true
	})
}
