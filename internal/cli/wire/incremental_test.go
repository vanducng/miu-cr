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

// TestSkipUnchangedPostNeverSkips: --post on an unchanged head SHA must NOT skip.
// The history store has no "posted" column, so a skip could silently drop a
// publish the user asked for (incl. when the prior run was a dry-run that never
// posted). Dropping the skip lets the review+publish proceed; the per-comment
// fingerprint dedupe + idempotent sentinel summary prevent duplicate comments.
func TestSkipUnchangedPostNeverSkips(t *testing.T) {
	ctx := stdctx.Background()
	st := tempStore(t)
	if _, err := st.SaveReview(ctx, store.ReviewRecord{
		ID: "prior-1", Mode: "pr", Owner: "o", Repo: "r", Number: 7, HeadSHA: "sha-1",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, ok := skipUnchanged(ctx, st, prInfo("sha-1"), false, true); ok {
		t.Fatal("--post must NOT skip an unchanged head SHA (would silently drop the publish)")
	}
	// Dry-run on the same SHA still skips — the perf optimization holds when nothing
	// is published.
	if _, ok := skipUnchanged(ctx, st, prInfo("sha-1"), false, false); !ok {
		t.Fatal("dry-run on an unchanged head SHA must still skip")
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
