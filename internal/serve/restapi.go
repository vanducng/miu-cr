package serve

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/store"
)

// restCommand is the command label on REST envelopes.
const restCommand = "reviews"

// maxAPIBodyBytes caps the JSON request body of POST /v1/reviews. Small: the
// body is just {owner,repo,number}.
const maxAPIBodyBytes = 64 << 10 // 64KB

// ReviewStore is the subset of the store the REST handlers need: persist a
// pending record up front, flip it to done/failed, and read it back. It is the
// seam tests inject a temp store through.
type ReviewStore interface {
	UpsertReview(ctx context.Context, rec store.ReviewRecord) (string, error)
	GetReview(ctx context.Context, id string) (store.ReviewRecord, error)
}

// createReviewReq is the POST /v1/reviews body. number is the PR number.
type createReviewReq struct {
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
}

// requireAPIAuth gates the /v1 routes on the env-only API bearer. Order is
// security-critical: an empty configured token must NEVER authenticate (empty ==
// empty compares EQUAL under ConstantTimeCompare), so len==0 → 401 BEFORE the
// compare; the scheme is a strict case-insensitive "Bearer " prefix.
func (s *Server) requireAPIAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.apiToken) == 0 {
			writeEnvelopeError(w, http.StatusUnauthorized, restCommand, "auth.unauthorized", "unauthorized")
			return
		}
		token, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok || subtle.ConstantTimeCompare([]byte(token), s.apiToken) != 1 {
			writeEnvelopeError(w, http.StatusUnauthorized, restCommand, "auth.unauthorized", "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearerToken extracts the credential from a strict case-insensitive "Bearer "
// scheme. A missing/odd scheme yields ok=false (→ 401), never a partial match.
// The credential is returned VERBATIM, trimming would both alter the secret
// (" tok " vs "tok") and leak whitespace via the length fed to the constant-time
// compare; whitespace is part of the token.
func bearerToken(header string) (string, bool) {
	const scheme = "bearer "
	if len(header) < len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return "", false
	}
	return header[len(scheme):], true
}

// handleCreateReview is POST /v1/reviews. It validates + allowlist-checks the
// target, persists a pending record under a server-generated id (crypto/rand,
// never client-supplied), enqueues the Job (id threaded via Job.ReviewID), and
// returns 202 + the id. The body is capped; an oversized body maps to 413.
func (s *Server) handleCreateReview(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAPIBodyBytes)
	var req createReviewReq
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeEnvelopeError(w, http.StatusRequestEntityTooLarge, restCommand, "request.too_large", "payload too large")
			return
		}
		writeEnvelopeError(w, http.StatusBadRequest, restCommand, "request.invalid", "invalid JSON body")
		return
	}
	if req.Owner == "" || req.Repo == "" || req.Number <= 0 {
		writeEnvelopeError(w, http.StatusBadRequest, restCommand, "request.invalid", "owner, repo and a positive number are required")
		return
	}
	if !s.allow.allows(req.Owner, req.Repo) {
		writeEnvelopeError(w, http.StatusForbidden, restCommand, "repo.not_allowed", "repository is not in the allowlist")
		return
	}

	token, err := s.resolveToken()
	if err != nil {
		s.log.Error("rest: token resolution failed", "error", config.RedactString(err.Error()))
		writeEnvelopeError(w, http.StatusInternalServerError, restCommand, "token.unavailable", "token unavailable")
		return
	}

	id, err := newReviewID()
	if err != nil {
		writeEnvelopeError(w, http.StatusInternalServerError, restCommand, "internal.error", "could not generate id")
		return
	}
	pk := prKey{Owner: req.Owner, Repo: req.Repo, Number: req.Number}
	rec := store.ReviewRecord{
		ID:        id,
		RepoDir:   "",
		Mode:      "pr",
		Status:    "pending",
		CreatedAt: s.now().UTC(),
	}
	if _, err := s.reviewStore.UpsertReview(r.Context(), rec); err != nil {
		s.log.Error("rest: persist pending failed", "error", config.RedactString(err.Error()))
		writeEnvelopeError(w, http.StatusInternalServerError, restCommand, "store.unavailable", "could not persist review")
		return
	}

	job := Job{
		Key:      pk,
		Ref:      pk.String(),
		Token:    token,
		Timeout:  s.reviewTO,
		ReviewID: id,
	}
	if s.dispatcher.Submit(job) != SubmitQueued {
		// Queue full or coalesced: no worker will ever process this id, so a 202
		// would promise a result that never comes. Flip the record to failed and
		// return 503 so the client retries instead of polling an eternal pending.
		s.log.Error("rest: job not enqueued (queue full or coalesced)", "repo", req.Owner+"/"+req.Repo, "number", req.Number)
		rec.Status = "failed"
		if _, err := s.reviewStore.UpsertReview(r.Context(), rec); err != nil {
			s.log.Error("rest: mark rejected review failed", "error", config.RedactString(err.Error()))
		}
		writeEnvelopeError(w, http.StatusServiceUnavailable, restCommand, "queue.full", "server is busy, please retry later")
		return
	}
	writeEnvelopeSuccess(w, http.StatusAccepted, restCommand, "review.accepted", map[string]any{
		"id":     id,
		"status": "pending",
	})
}

// handleGetReview is GET /v1/reviews/{id}. It reads the persisted record and maps
// a WHITELIST (id, status, created_at, findings, stats) to the envelope, never
// RepoDir (the /tmp clone path = host info disclosure). A pending row older than
// reviewTO is lazily recovered to failed (a crashed worker leaves no eternal
// pending).
func (s *Server) handleGetReview(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rec, err := s.reviewStore.GetReview(r.Context(), id)
	if err != nil {
		writeEnvelopeError(w, http.StatusNotFound, restCommand, "review.not_found", "review not found")
		return
	}
	rec = s.recoverStuckPending(r.Context(), rec)
	writeEnvelopeSuccess(w, http.StatusOK, restCommand, "review.result", reviewView(rec))
}

// recoverStuckPending flips a pending record older than reviewTO to failed
// (idempotent) so a crashed worker can't leave an eternal pending. Best-effort:
// an upsert failure leaves the in-memory record returned as failed regardless.
func (s *Server) recoverStuckPending(ctx context.Context, rec store.ReviewRecord) store.ReviewRecord {
	if rec.Status != "pending" || s.reviewTO <= 0 {
		return rec
	}
	if s.now().UTC().Sub(rec.CreatedAt) <= s.reviewTO {
		return rec
	}
	rec.Status = "failed"
	if _, err := s.reviewStore.UpsertReview(ctx, rec); err != nil {
		s.log.Warn("rest: stuck-pending recovery upsert failed", "error", config.RedactString(err.Error()))
	}
	return rec
}

// reviewView is the GET whitelist: id, status, created_at, findings, stats. It
// deliberately omits RepoDir/Mode/HeadSHA paths that could leak host info.
func reviewView(rec store.ReviewRecord) map[string]any {
	findings := rec.Findings
	if findings == nil {
		findings = []engine.Finding{}
	}
	stats := rec.Stats
	if stats == nil {
		stats = map[string]any{}
	}
	return map[string]any{
		"id":         rec.ID,
		"status":     rec.Status,
		"created_at": rec.CreatedAt.UTC().Format(time.RFC3339Nano),
		"findings":   findings,
		"stats":      stats,
	}
}

// newReviewID returns a server-generated 128-bit hex id. It is NEVER taken from
// the request (no client-supplied ids).
func newReviewID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
