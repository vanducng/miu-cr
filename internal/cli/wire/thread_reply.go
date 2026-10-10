package wire

import (
	stdctx "context"
	"errors"
	"log/slog"
	"time"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine/agent"
	mgithub "github.com/vanducng/miu-cr/internal/github"
	"github.com/vanducng/miu-cr/internal/serve"
)

var errReplyBudget = errors.New("reply budget exhausted")

func handleServeThreadReply(ctx stdctx.Context, job serve.Job) error {
	if job.Reply == nil || job.Reply.CommentID <= 0 {
		return nil
	}
	return applyServeThreadReply(ctx, job)
}

func applyServeThreadReply(ctx stdctx.Context, job serve.Job) (err error) {
	client := newGitHubClient(job.Token)
	var info *mgithub.PRInfo
	defer func() {
		if err == nil || info == nil {
			return
		}
		if answerWebhookReply(ctx, client, info, job) {
			err = nil
		}
	}()
	ref, err := mgithub.ParseRef(job.Ref)
	if err != nil {
		return err
	}
	info = &mgithub.PRInfo{Owner: ref.Owner, Repo: ref.Repo, Number: ref.Number}
	stub := info
	err = retryTransient(ctx, maxGitHubAttempts, func() error {
		fetched, e := mgithub.FetchPR(ctx, client, ref)
		if e != nil {
			return e
		}
		info = fetched
		return nil
	})
	if err != nil {
		info = stub
		return err
	}
	review := serve.JobReviewOptions{}
	if job.Review != nil {
		review = *job.Review
	}
	creds, err := agent.Resolve(agent.ResolveInput{
		Ctx:           ctx,
		Provider:      review.Provider,
		APIKey:        review.APIKey,
		BaseURL:       review.BaseURL,
		AuthToken:     review.AuthToken,
		Model:         review.Model,
		OAuthResolver: oauthResolver(),
	})
	if err != nil {
		return err
	}
	llm, err := agent.New(creds, job.Timeout)
	if err != nil {
		return err
	}
	res, err := mgithub.ApplyThreadReply(ctx, client, info, mgithub.ThreadReplyRequest{
		CommentID:         job.Reply.CommentID,
		Body:              job.Reply.Body,
		UserLogin:         job.Reply.UserLogin,
		AuthorAssociation: job.Reply.AuthorAssociation,
		InReplyTo:         job.Reply.InReplyTo,
		Kind:              job.Reply.Kind,
		HostRetry:         job.Reply.HostRetry,
		AroundWrite: func(fn func() error) error {
			return serve.WithPRFlight(job.Ref, fn)
		},
	}, replyJudgeAdapter{llm: llm, retry: review.ProviderRetry, ref: job.Ref}, review.Approval, time.Now())
	attrs := []any{
		"repo", info.Owner + "/" + info.Repo, "pr", info.Number, "head_sha", shortSHA(info.HeadSHA),
		"comment_id", job.Reply.CommentID, "reason", res.Reason, "accepted", res.Accepted, "rejected", res.Rejected,
	}
	if res.Approved {
		attrs = append(attrs, "approved", true)
	} else if res.ApproveReason != "" {
		attrs = append(attrs, "approve_skipped", res.ApproveReason)
	}
	if err != nil {
		if errors.Is(err, errReplyBudget) {
			if answerWebhookReply(ctx, client, info, job) {
				return nil
			}
			serve.DeferReplyRetry(serve.ReplyRetryKey(job.Ref, job.Reply.CommentID), time.Now().Add(time.Hour))
			slog.Warn("thread reply skipped",
				"repo", info.Owner+"/"+info.Repo, "pr", info.Number, "head_sha", shortSHA(info.HeadSHA),
				"comment_id", job.Reply.CommentID, "reason", "reply_budget")
			return nil
		}
		if answerWebhookReply(ctx, client, info, job) {
			return nil
		}
		attrs = append(attrs, "error", config.RedactString(err.Error()))
		slog.Warn("thread reply failed", attrs...)
		return err
	}
	if res.CoolDown > 0 {
		if answerWebhookReply(ctx, client, info, job) {
			return nil
		}
		serve.DeferReplyRetry(serve.ReplyRetryKey(job.Ref, job.Reply.CommentID), time.Now().Add(res.CoolDown))
		slog.Info("thread reply waiting", attrs...)
		return nil
	}
	slog.Info("thread reply handled", attrs...)
	return nil
}

func answerWebhookReply(ctx stdctx.Context, client mgithub.Client, info *mgithub.PRInfo, job serve.Job) bool {
	if job.Reply == nil || job.Reply.HostRetry {
		return false
	}
	cctx, cancel := stdctx.WithTimeout(stdctx.WithoutCancel(ctx), reviewErrorSummaryTimeout)
	defer cancel()
	note := mgithub.ThreadReplyRequest{CommentID: job.Reply.CommentID, Kind: job.Reply.Kind}
	if err := mgithub.PostThreadReply(cctx, client, info, note, mgithub.ReplyFailureNote(job.Reply.CommentID)); err != nil {
		return false
	}
	slog.Warn("thread reply unanswered",
		"repo", info.Owner+"/"+info.Repo, "pr", info.Number, "head_sha", shortSHA(info.HeadSHA),
		"comment_id", job.Reply.CommentID, "reason", "webhook_no_retry")
	return true
}

type replyJudgeAdapter struct {
	llm   agent.Agent
	retry config.ProviderRetry
	ref   string
}

func (a replyJudgeAdapter) JudgeThreadReply(ctx stdctx.Context, in mgithub.ThreadReplyJudgeInput) (mgithub.ThreadReplyVerdict, error) {
	if !serve.AllowReplySpend(a.ref, time.Now()) {
		return mgithub.ThreadReplyVerdict{}, errReplyBudget
	}
	verdict, err := a.llm.JudgeReply(ctx, agent.ReplyJudgeRequest{
		Intent:        string(in.Intent),
		Path:          in.Path,
		Line:          in.Line,
		Title:         in.Title,
		Severity:      in.Severity,
		FindingBody:   in.FindingBody,
		Reply:         in.Reply,
		Reason:        in.Reason,
		CommitSHA:     in.CommitSHA,
		CommitPatch:   in.CommitPatch,
		LineInPatch:   in.LineInPatch,
		ProviderRetry: a.retry,
	})
	if errors.Is(err, agent.ErrReplyVerdictParse) {
		return mgithub.ThreadReplyVerdict{}, mgithub.ErrUnparseableVerdict
	}
	if err != nil {
		return mgithub.ThreadReplyVerdict{}, err
	}
	return mgithub.ThreadReplyVerdict{Accept: verdict.Accept, Explanation: verdict.Explanation}, nil
}
