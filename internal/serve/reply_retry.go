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
}

// ReplyRetryReady reports whether a comment may be queued again.
func ReplyRetryReady(key string, now time.Time) bool {
	v, ok := replyRetry.Load(key)
	if !ok {
		return true
	}
	until, ok := v.(time.Time)
	if !ok || !now.Before(until) {
		replyRetry.Delete(key)
		return true
	}
	return false
}
