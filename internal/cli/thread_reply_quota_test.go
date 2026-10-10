package cli

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/vanducng/miu-cr/internal/serve"
)

func TestThreadReplyQuotaCoolsDownInsteadOfRetrying(t *testing.T) {
	SetServeThreadReply(func(context.Context, serve.Job) error {
		return &CLIError{Code: "quota.exceeded", Message: "provider quota exhausted", Exit: 2, Details: map[string]any{"resets_in_seconds": 7200}}
	})
	t.Cleanup(func() { SetServeThreadReply(nil) })
	fn := buildServeReviewFn(slog.New(slog.NewTextHandler(io.Discard, nil)), "high", nil, nil, false)
	job := serve.Job{
		Kind:  serve.JobKindThreadReply,
		Ref:   "acme/app#1",
		Reply: &serve.ThreadReply{CommentID: 41, Body: "Deferred: tracked in #42 because this is safe"},
	}
	if err := fn(job); err != nil {
		t.Fatal(err)
	}
	if serve.ReplyRetryReady(serve.ReplyRetryKey(job.Ref, 41), time.Now().Add(90*time.Minute)) {
		t.Fatal("quota cooldown should follow resets_in_seconds, not one hour")
	}
	if !serve.ReplyRetryReady(serve.ReplyRetryKey(job.Ref, 41), time.Now().Add(3*time.Hour)) {
		t.Fatal("quota cooldown should end after the reset window")
	}
}

func TestThreadReplyErrorCoolsDown(t *testing.T) {
	SetServeThreadReply(func(context.Context, serve.Job) error {
		return &CLIError{Code: "github.thread_reply_failed", Message: "posting reply failed", Exit: 1}
	})
	t.Cleanup(func() { SetServeThreadReply(nil) })
	fn := buildServeReviewFn(slog.New(slog.NewTextHandler(io.Discard, nil)), "high", nil, nil, false)
	job := serve.Job{
		Kind:  serve.JobKindThreadReply,
		Ref:   "acme/app#2",
		Reply: &serve.ThreadReply{CommentID: 42, Body: "Deferred: tracked in #42 because this is safe"},
	}
	if err := fn(job); err == nil {
		t.Fatal("expected the reply error to surface")
	}
	if serve.ReplyRetryReady(serve.ReplyRetryKey(job.Ref, 42), time.Now()) {
		t.Fatal("a failed reply should cool down before the next poll")
	}
}
