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
		return &CLIError{Code: "quota.exceeded", Message: "provider quota exhausted", Exit: 2}
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
	if serve.ReplyRetryReady(serve.ReplyRetryKey(job.Ref, 41), time.Now()) {
		t.Fatal("quota exhaustion should cool the comment down")
	}
}
