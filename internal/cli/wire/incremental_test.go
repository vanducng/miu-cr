package wire

import (
	stdctx "context"
	"errors"
	"testing"

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

	prior, ok := skipUnchanged(ctx, st, prInfo("sha-1"), false, false)
	if !ok {
		t.Fatal("same head SHA must skip")
	}
	if prior.ID != "prior-1" || prior.HeadSHA != "sha-1" {
		t.Fatalf("prior = %+v, want id=prior-1 sha=sha-1", prior)
	}
}

// TestSkipUnchangedPostNeverSkips: --post NEVER skips, even at a head SHA that was
// already reviewed (same store record, same SHA). A re-run must re-enter
// publishReview so the single summary issue comment gets EDITED in place.
func TestSkipUnchangedPostNeverSkips(t *testing.T) {
	ctx := stdctx.Background()
	st := tempStore(t)
	if _, err := st.SaveReview(ctx, store.ReviewRecord{
		ID: "prior-1", Mode: "pr", Owner: "o", Repo: "r", Number: 7, HeadSHA: "sha-1",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, ok := skipUnchanged(ctx, st, prInfo("sha-1"), false, true); ok {
		t.Fatal("--post must re-enter (never skip) so the summary comment is edited")
	}
}

// TestSkipUnchangedChecksModeNeverSkips: --mode checks --post always publishes the
// (idempotent per-SHA) CheckRun, even at an already-reviewed SHA.
func TestSkipUnchangedChecksModeNeverSkips(t *testing.T) {
	ctx := stdctx.Background()
	st := tempStore(t)
	if _, err := st.SaveReview(ctx, store.ReviewRecord{
		ID: "prior-1", Mode: "pr", Owner: "o", Repo: "r", Number: 7, HeadSHA: "sha-1",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, ok := skipUnchanged(ctx, st, prInfo("sha-1"), false, true); ok {
		t.Fatal("--mode checks --post must always publish the CheckRun")
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
	if _, ok := skipUnchanged(ctx, st, prInfo("sha-2"), false, false); ok {
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
	if _, ok := skipUnchanged(ctx, st, prInfo("sha-1"), true, false); ok {
		t.Fatal("--force must bypass the skip")
	}
}

// TestSkipUnchangedNoPrior: a first review of a PR (no prior record) reviews.
func TestSkipUnchangedNoPrior(t *testing.T) {
	ctx := stdctx.Background()
	st := tempStore(t)
	if _, ok := skipUnchanged(ctx, st, prInfo("sha-1"), false, false); ok {
		t.Fatal("no prior review must NOT skip")
	}
}

// TestSkipUnchangedNilStore: history off / --no-save (nil store) reviews.
func TestSkipUnchangedNilStore(t *testing.T) {
	if _, ok := skipUnchanged(stdctx.Background(), nil, prInfo("sha-1"), false, false); ok {
		t.Fatal("a nil history store must NOT skip (degrade to always-review)")
	}
}

// TestSkipUnchangedReadErrorDegrades: a store read failure degrades to
// always-review (no skip), never blocking the review.
func TestSkipUnchangedReadErrorDegrades(t *testing.T) {
	st := errStore{err: errors.New("db locked")}
	if _, ok := skipUnchanged(stdctx.Background(), st, prInfo("sha-1"), false, false); ok {
		t.Fatal("a read error must degrade to always-review, not skip")
	}
}
