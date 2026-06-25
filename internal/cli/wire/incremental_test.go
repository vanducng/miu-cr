package wire

import (
	stdctx "context"
	"errors"
	"testing"

	gh "github.com/google/go-github/v84/github"

	mgithub "github.com/vanducng/miu-cr/internal/github"
	"github.com/vanducng/miu-cr/internal/store"
)

// errStore is a store.Store whose LatestReviewForPR fails, proving the
// incremental check degrades to "always review" on a read error.
type errStore struct {
	store.Store
	err error
}

func (e errStore) LatestReviewForPR(stdctx.Context, store.PRKey) (store.LatestReview, bool, error) {
	return store.LatestReview{}, false, e.err
}

// prInfo builds a PRInfo for the skip tests.
func prInfo(headSHA string) *mgithub.PRInfo {
	return &mgithub.PRInfo{Owner: "o", Repo: "r", Number: 7, HeadSHA: headSHA, BaseSHA: "base", BaseBranch: "main"}
}

// TestSkipUnchangedSameSHA: a prior review of the same PR + same head SHA (no
// --force) short-circuits, surfacing the prior review id.
func TestSkipUnchangedSameSHA(t *testing.T) {
	ctx := stdctx.Background()
	st := tempStore(t)
	if _, err := st.SaveReview(ctx, store.ReviewRecord{
		ID: "prior-1", Mode: "pr", Owner: "o", Repo: "r", Number: 7, HeadSHA: "sha-1",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	prior, ok := skipUnchanged(ctx, st, &fakeGitHub{}, prInfo("sha-1"), false, false, "review")
	if !ok {
		t.Fatal("same head SHA must skip")
	}
	if prior.ID != "prior-1" || prior.HeadSHA != "sha-1" {
		t.Fatalf("prior = %+v, want id=prior-1 sha=sha-1", prior)
	}
}

// postedReviewClient is a fakeGitHub preloaded with a miucr-authored review (marker
// in the body) at the given commit SHA, so AlreadyPostedAtSHA matches.
func postedReviewClient(sha string) *fakeGitHub {
	return &fakeGitHub{reviews: []*gh.PullRequestReview{
		{CommitID: gh.Ptr(sha), Body: gh.Ptr(mgithub.ReviewMarker + "\n## Code Review")},
	}}
}

// TestSkipUnchangedPostSkipsWhenAlreadyPosted: --post on a head SHA we ALREADY
// posted a review for (Codex per-commit model) skips — a second review would be a
// duplicate (reviews aren't editable). A reviewed-but-unposted prior (no matching
// review on the API) still posts.
func TestSkipUnchangedPostSkipsWhenAlreadyPosted(t *testing.T) {
	ctx := stdctx.Background()
	st := tempStore(t)

	// No review on the API yet → --post must publish (reviewed-but-unposted prior).
	if _, ok := skipUnchanged(ctx, st, &fakeGitHub{}, prInfo("sha-1"), false, true, "review"); ok {
		t.Fatal("--post must publish when no review was posted for this SHA yet")
	}
	// A miucr review already exists at this SHA → skip the duplicate.
	if _, ok := skipUnchanged(ctx, st, postedReviewClient("sha-1"), prInfo("sha-1"), false, true, "review"); !ok {
		t.Fatal("--post must skip when a review was already posted for this head SHA")
	}
	// An already-posted review at a DIFFERENT SHA → still publish.
	if _, ok := skipUnchanged(ctx, st, postedReviewClient("old"), prInfo("sha-1"), false, true, "review"); ok {
		t.Fatal("--post must publish when the only posted review is at a different SHA")
	}
}

// TestSkipUnchangedChecksModeNeverPostedSkip: a prior miucr REVIEW at this SHA must
// NOT skip a `--mode checks --post` run — CheckRuns are idempotent per commit and the
// review-marker posted-SHA detection is review-only.
func TestSkipUnchangedChecksModeNeverPostedSkip(t *testing.T) {
	ctx := stdctx.Background()
	st := tempStore(t)
	if _, ok := skipUnchanged(ctx, st, postedReviewClient("sha-1"), prInfo("sha-1"), false, true, "checks"); ok {
		t.Fatal("--mode checks --post must publish the CheckRun even when a prior review exists at this SHA")
	}
}

// TestSkipUnchangedDifferentSHA: a new commit (changed head SHA) always reviews.
func TestSkipUnchangedDifferentSHA(t *testing.T) {
	ctx := stdctx.Background()
	st := tempStore(t)
	if _, err := st.SaveReview(ctx, store.ReviewRecord{
		ID: "prior-1", Mode: "pr", Owner: "o", Repo: "r", Number: 7, HeadSHA: "sha-1",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, ok := skipUnchanged(ctx, st, &fakeGitHub{}, prInfo("sha-2"), false, false, "review"); ok {
		t.Fatal("a changed head SHA must NOT skip")
	}
}

// TestSkipUnchangedForce: --force re-reviews even on an unchanged head SHA.
func TestSkipUnchangedForce(t *testing.T) {
	ctx := stdctx.Background()
	st := tempStore(t)
	if _, err := st.SaveReview(ctx, store.ReviewRecord{
		ID: "prior-1", Mode: "pr", Owner: "o", Repo: "r", Number: 7, HeadSHA: "sha-1",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, ok := skipUnchanged(ctx, st, &fakeGitHub{}, prInfo("sha-1"), true, false, "review"); ok {
		t.Fatal("--force must bypass the skip")
	}
}

// TestSkipUnchangedNoPrior: a first review of a PR (no prior record) reviews.
func TestSkipUnchangedNoPrior(t *testing.T) {
	ctx := stdctx.Background()
	st := tempStore(t)
	if _, ok := skipUnchanged(ctx, st, &fakeGitHub{}, prInfo("sha-1"), false, false, "review"); ok {
		t.Fatal("no prior review must NOT skip")
	}
}

// TestSkipUnchangedNilStore: history off / --no-save (nil store) reviews.
func TestSkipUnchangedNilStore(t *testing.T) {
	if _, ok := skipUnchanged(stdctx.Background(), nil, &fakeGitHub{}, prInfo("sha-1"), false, false, "review"); ok {
		t.Fatal("a nil history store must NOT skip (degrade to always-review)")
	}
}

// TestSkipUnchangedReadErrorDegrades: a store read failure degrades to
// always-review (no skip), never blocking the review.
func TestSkipUnchangedReadErrorDegrades(t *testing.T) {
	st := errStore{err: errors.New("db locked")}
	if _, ok := skipUnchanged(stdctx.Background(), st, &fakeGitHub{}, prInfo("sha-1"), false, false, "review"); ok {
		t.Fatal("a read error must degrade to always-review, not skip")
	}
}
