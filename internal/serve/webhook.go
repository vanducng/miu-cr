package serve

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/google/go-github/v84/github"

	"github.com/vanducng/miu-cr/internal/config"
	mgithub "github.com/vanducng/miu-cr/internal/github"
)

// actedActions are the PR webhook actions serve reviews on. Everything else
// (closed, labeled, edited, draft toggles, ...) is 200-ignored.
var actedActions = map[string]struct{}{
	"opened":           {},
	"synchronize":      {},
	"reopened":         {},
	"ready_for_review": {},
}

// handleWebhook is the /webhook handler. Order is security-critical: cap the body
// FIRST, guard the event type BEFORE ParseWebHook (which panics on unknown
// types), HMAC-verify, filter, then respond 200 BEFORE dispatch so GitHub's ~10s
// budget is never spent on the review.
func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	delivery := r.Header.Get("X-GitHub-Delivery")
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	// Guard the event type before ValidatePayload/ParseWebHook: ParseWebHook
	// panics on unregistered types, so unknown events get a cheap 200-ignore.
	et := github.WebHookType(r)
	switch et {
	case "pull_request", "pull_request_review_comment", "issue_comment":
	default:
		s.log.Info("webhook ignored: unsupported event",
			"delivery", delivery, "event", et)
		writeJSON(w, http.StatusOK, `{"status":"ignored"}`)
		return
	}

	payload, err := github.ValidatePayload(r, s.secret)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			s.log.Warn("webhook rejected: body too large", "delivery", delivery)
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		s.log.Warn("webhook rejected: signature validation failed",
			"delivery", delivery, "error", config.RedactString(err.Error()))
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	// ParseWebHook consumes only the validated []byte; r.Body is never re-read.
	event, err := github.ParseWebHook(et, payload)
	if err != nil {
		s.log.Warn("webhook rejected: parse failed",
			"delivery", delivery, "error", config.RedactString(err.Error()))
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	job, ignoreReason := webhookJob(event)
	if ignoreReason != "" {
		owner, repo, number := webhookRepo(event)
		s.log.Info("webhook ignored: "+ignoreReason,
			"delivery", delivery, "repo", owner+"/"+repo, "number", number, "event", et)
		writeJSON(w, http.StatusOK, `{"status":"ignored"}`)
		return
	}
	if !s.allow.allows(job.Key.Owner, job.Key.Repo) {
		s.log.Info("webhook ignored: repo not in allowlist",
			"delivery", delivery, "repo", job.Key.Owner+"/"+job.Key.Repo, "number", job.Key.Number)
		writeJSON(w, http.StatusOK, `{"status":"ignored"}`)
		return
	}

	token, err := s.resolveToken()
	if err != nil {
		s.log.Error("webhook: token resolution failed",
			"delivery", delivery, "repo", job.Key.Owner+"/"+job.Key.Repo, "number", job.Key.Number,
			"error", config.RedactString(err.Error()))
		http.Error(w, "token unavailable", http.StatusInternalServerError)
		return
	}
	job.Token = token
	job.Timeout = s.reviewTO
	job.StalledTimeout = s.stalledTO

	// Respond 200 BEFORE dispatch so the review never runs on the HTTP goroutine
	// and GitHub's delivery budget is never spent on the LLM.
	writeJSON(w, http.StatusOK, `{"status":"accepted"}`)

	if s.dispatcher.Submit(job) != SubmitQueued {
		s.log.Error("webhook: job dropped, dispatch queue full",
			"delivery", delivery, "repo", job.Key.Owner+"/"+job.Key.Repo, "number", job.Key.Number, "kind", job.Kind)
		return
	}
	s.log.Info("webhook accepted: job dispatched",
		"delivery", delivery, "repo", job.Key.Owner+"/"+job.Key.Repo, "number", job.Key.Number, "kind", job.Kind)
}

func webhookJob(event any) (Job, string) {
	switch pe := event.(type) {
	case *github.PullRequestEvent:
		action := pe.GetAction()
		if _, acted := actedActions[action]; !acted {
			return Job{}, "action not acted"
		}
		if action == "opened" && pe.GetPullRequest().GetDraft() {
			return Job{}, "draft on open"
		}
		owner := pe.GetRepo().GetOwner().GetLogin()
		repo := pe.GetRepo().GetName()
		number := pe.GetNumber()
		pk := prKey{Owner: owner, Repo: repo, Number: number}
		return Job{Key: pk, Ref: pk.String()}, ""
	case *github.PullRequestReviewCommentEvent:
		if pe.GetAction() != "created" {
			return Job{}, "action not acted"
		}
		body := pe.GetComment().GetBody()
		if mgithub.IsBotBody(body) {
			return Job{}, "bot comment"
		}
		if mgithub.ParseThreadReply(body).Intent == "" {
			return Job{}, "unrecognized reply"
		}
		commentLogin := pe.GetComment().GetUser().GetLogin()
		association := pe.GetComment().GetAuthorAssociation()
		if !mgithub.ReplyAuthorized(commentLogin, association, pe.GetPullRequest().GetUser().GetLogin()) {
			return Job{}, "untrusted reply"
		}
		owner := pe.GetRepo().GetOwner().GetLogin()
		repo := pe.GetRepo().GetName()
		number := pe.GetPullRequest().GetNumber()
		id := pe.GetComment().GetID()
		return Job{
			Key:  prKey{Owner: owner, Repo: repo, Number: number, CommentID: id},
			Ref:  fmt.Sprintf("%s/%s#%d", owner, repo, number),
			Kind: JobKindThreadReply,
			Reply: &ThreadReply{
				CommentID:         id,
				Body:              body,
				UserLogin:         commentLogin,
				AuthorAssociation: association,
				InReplyTo:         pe.GetComment().GetInReplyTo(),
				Kind:              "review_comment",
			},
		}, ""
	case *github.IssueCommentEvent:
		if pe.GetAction() != "created" || !pe.GetIssue().IsPullRequest() {
			return Job{}, "action not acted"
		}
		body := pe.GetComment().GetBody()
		if mgithub.IsBotBody(body) {
			return Job{}, "bot comment"
		}
		if mgithub.ParseThreadReply(body).Intent == "" {
			return Job{}, "unrecognized reply"
		}
		commentLogin := pe.GetComment().GetUser().GetLogin()
		association := pe.GetComment().GetAuthorAssociation()
		if !mgithub.ReplyAuthorized(commentLogin, association, pe.GetIssue().GetUser().GetLogin()) {
			return Job{}, "untrusted reply"
		}
		owner := pe.GetRepo().GetOwner().GetLogin()
		repo := pe.GetRepo().GetName()
		number := pe.GetIssue().GetNumber()
		id := pe.GetComment().GetID()
		return Job{
			Key:  prKey{Owner: owner, Repo: repo, Number: number, CommentID: id},
			Ref:  fmt.Sprintf("%s/%s#%d", owner, repo, number),
			Kind: JobKindThreadReply,
			Reply: &ThreadReply{
				CommentID:         id,
				Body:              body,
				UserLogin:         commentLogin,
				AuthorAssociation: association,
				Kind:              "issue_comment",
			},
		}, ""
	default:
		return Job{}, "unsupported payload"
	}
}

func webhookRepo(event any) (string, string, int) {
	switch pe := event.(type) {
	case *github.PullRequestEvent:
		return pe.GetRepo().GetOwner().GetLogin(), pe.GetRepo().GetName(), pe.GetNumber()
	case *github.PullRequestReviewCommentEvent:
		return pe.GetRepo().GetOwner().GetLogin(), pe.GetRepo().GetName(), pe.GetPullRequest().GetNumber()
	case *github.IssueCommentEvent:
		return pe.GetRepo().GetOwner().GetLogin(), pe.GetRepo().GetName(), pe.GetIssue().GetNumber()
	default:
		return "", "", 0
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, `{"status":"ok"}`)
}

func writeJSON(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}
