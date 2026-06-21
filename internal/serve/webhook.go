package serve

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/google/go-github/v84/github"

	"github.com/vanducng/miu-cr/internal/config"
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
	if et := github.WebHookType(r); et != "pull_request" {
		s.log.Info("webhook ignored: non-pull_request event",
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
	event, err := github.ParseWebHook("pull_request", payload)
	if err != nil {
		s.log.Warn("webhook rejected: parse failed",
			"delivery", delivery, "error", config.RedactString(err.Error()))
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	pe, ok := event.(*github.PullRequestEvent)
	if !ok {
		s.log.Warn("webhook rejected: not a PullRequestEvent", "delivery", delivery)
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}

	action := pe.GetAction()
	owner := pe.GetRepo().GetOwner().GetLogin()
	repo := pe.GetRepo().GetName()
	number := pe.GetNumber()

	if _, acted := actedActions[action]; !acted {
		s.log.Info("webhook ignored: action not acted",
			"delivery", delivery, "repo", owner+"/"+repo, "number", number, "action", action)
		writeJSON(w, http.StatusOK, `{"status":"ignored"}`)
		return
	}
	// A PR opened as a draft carries no actionable review; skip until ready.
	if action == "opened" && pe.GetPullRequest().GetDraft() {
		s.log.Info("webhook ignored: draft on open",
			"delivery", delivery, "repo", owner+"/"+repo, "number", number)
		writeJSON(w, http.StatusOK, `{"status":"ignored"}`)
		return
	}
	if !s.allow.allows(owner, repo) {
		s.log.Info("webhook ignored: repo not in allowlist",
			"delivery", delivery, "repo", owner+"/"+repo, "number", number)
		writeJSON(w, http.StatusOK, `{"status":"ignored"}`)
		return
	}

	token, err := s.resolveToken()
	if err != nil {
		s.log.Error("webhook: token resolution failed",
			"delivery", delivery, "repo", owner+"/"+repo, "number", number,
			"error", config.RedactString(err.Error()))
		http.Error(w, "token unavailable", http.StatusInternalServerError)
		return
	}

	// Respond 200 BEFORE dispatch so the review never runs on the HTTP goroutine
	// and GitHub's delivery budget is never spent on the LLM.
	writeJSON(w, http.StatusOK, `{"status":"accepted"}`)

	job := Job{
		Key:     prKey{Owner: owner, Repo: repo, Number: number},
		Ref:     fmt.Sprintf("%s/%s#%d", owner, repo, number),
		Token:   token,
		Timeout: s.reviewTO,
	}
	if !s.dispatcher.Submit(job) {
		s.log.Error("webhook: job dropped, dispatch queue full",
			"delivery", delivery, "repo", owner+"/"+repo, "number", number, "action", action)
		return
	}
	s.log.Info("webhook accepted: review dispatched",
		"delivery", delivery, "repo", owner+"/"+repo, "number", number, "action", action)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, `{"status":"ok"}`)
}

func writeJSON(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}
