package wire

import (
	stdctx "context"
	"log/slog"
	"strings"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/diff"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
	mgithub "github.com/vanducng/miu-cr/internal/github"
	"github.com/vanducng/miu-cr/internal/store"
)

func priorReviewSHA(info *mgithub.PRInfo) string {
	if info == nil {
		return ""
	}
	if s := strings.TrimSpace(info.PriorPublishedHeadSHA); s != "" {
		return s
	}
	return strings.TrimSpace(info.PriorSummaryHeadSHA)
}

// sameReviewedCommit reports whether prior names head. The summary footer stores
// a short SHA, which never equals the full head.
func sameReviewedCommit(prior, head string) bool {
	prior = strings.TrimSpace(prior)
	head = strings.TrimSpace(head)
	if len(prior) < 7 || head == "" {
		return false
	}
	return strings.HasPrefix(strings.ToLower(head), strings.ToLower(prior))
}

// applyStableScope drops findings whose quoted code the commits since the last
// trusted review did not add or delete, and records which prior lines stayed in place.
// An untrusted ledger or a missing delta keeps the findings and leaves the stable maps nil.
func applyStableScope(ctx stdctx.Context, runner *gitcmd.Runner, dir string, info *mgithub.PRInfo, findings []engine.Finding, stats *map[string]any) ([]engine.Finding, map[string]bool) {
	if info == nil || !info.LedgerTrusted || len(info.PriorLedger) == 0 {
		return findings, nil
	}
	prior := priorReviewSHA(info)
	if prior == "" || sameReviewedCommit(prior, info.HeadSHA) {
		return findings, nil
	}
	delta, err := diff.GetDiff(ctx, diff.ModeRange, dir, prior, info.HeadSHA, "", runner)
	if err != nil {
		slog.Warn("review: stable scope unavailable",
			"repo", info.Owner+"/"+info.Repo, "pr", info.Number,
			"head_sha", shortSHA(info.HeadSHA), "prior_sha", shortSHA(prior),
			"reason_code", "delta_unavailable", "error", config.RedactString(err.Error()))
		return findings, nil
	}
	// An empty diff parses as nil. Nil means the scope is unknown, so a clean
	// delta must be an empty slice or omitted findings resolve under the old rule.
	if delta == nil {
		delta = []diff.Diff{}
	}
	info.StableFPs = mgithub.UntouchedAnchors(info.PriorLedger, delta)
	info.StableLines = mgithub.ShiftedLines(info.PriorLedger, delta)
	kept, dropped := mgithub.SelectStableFindings(findings, info.PriorLedger, delta)
	if stats != nil {
		if *stats == nil {
			*stats = map[string]any{}
		}
		(*stats)["stable_scope_dropped"] = dropped
	}
	slog.Info("review: stable scope",
		"repo", info.Owner+"/"+info.Repo, "pr", info.Number,
		"head_sha", shortSHA(info.HeadSHA), "prior_sha", shortSHA(prior),
		"reason_code", "stable_scope", "dropped", dropped, "kept", len(kept))
	return kept, mgithub.StablePaths(info.Files, info.PriorLedger, delta)
}

// syncFilteredReview writes the post-filter findings over the engine's pre-filter save.
// A store error is logged and does not fail the review.
func syncFilteredReview(ctx stdctx.Context, hist store.Store, id string, findings []engine.Finding, stats map[string]any) {
	if hist == nil || id == "" {
		return
	}
	rec, err := hist.GetReview(ctx, id)
	if err != nil {
		slog.Warn("review: stable scope history sync failed",
			"review_id", id, "reason_code", "history_sync_failed", "error", config.RedactString(err.Error()))
		return
	}
	rec.Findings = findings
	rec.Stats = stats
	if _, err := hist.UpsertReview(ctx, rec); err != nil {
		slog.Warn("review: stable scope history sync failed",
			"review_id", id, "reason_code", "history_sync_failed", "error", config.RedactString(err.Error()))
	}
}
