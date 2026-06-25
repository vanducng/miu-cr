package wire

import (
	stdctx "context"
	"path/filepath"
	"testing"
	"time"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/store"
	"github.com/vanducng/miu-cr/internal/store/sqlite"
)

func boolPtr(b bool) *bool { return &b }

func tempHistoryStore(t *testing.T) *sqlite.Store {
	t.Helper()
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// History is on by default and off with --no-save or an explicit disable.
func TestNewHistoryStoreGating(t *testing.T) {
	withSeam := func(noSave bool, cfg config.Config) (store.Store, error) {
		s, _, err := newHistoryStore(stdctx.Background(), cfg, noSave)
		return s, err
	}

	if s, err := withSeam(true, config.Defaults()); err != nil || s != nil {
		t.Fatalf("--no-save must skip: s=%v err=%v", s, err)
	}
	off := config.Defaults()
	off.History.Enabled = boolPtr(false)
	if s, err := withSeam(false, off); err != nil || s != nil {
		t.Fatalf("enabled=false must skip: s=%v err=%v", s, err)
	}
	if !config.Defaults().History.On() {
		t.Fatal("default config must have history on")
	}
}

// openHistoryStore degrades to (nil,nil) when the seam errors, the review must
// never fail because the store could not open.
func TestOpenHistoryStoreDegradesOnError(t *testing.T) {
	restore := newHistoryStore
	newHistoryStore = func(stdctx.Context, config.Config, bool) (store.Store, func(), error) {
		return nil, nil, &cliErr{"store.unavailable"}
	}
	t.Cleanup(func() { newHistoryStore = restore })

	s, closeFn := openHistoryStore(stdctx.Background(), config.Defaults(), false)
	if s != nil || closeFn != nil {
		t.Fatal("open failure must degrade to no-save (nil store, nil closer)")
	}
}

type cliErr struct{ msg string }

func (e *cliErr) Error() string { return e.msg }

// pruneHistory trims to MaxRecords (oldest dropped) after a save; no cap is a
// no-op.
func TestPruneHistoryCapsOldest(t *testing.T) {
	s := tempHistoryStore(t)
	ctx := stdctx.Background()
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		if _, err := s.SaveReview(ctx, store.ReviewRecord{
			Mode: "staged", Status: "done", CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}

	noCap := config.Defaults()
	pruneHistory(ctx, s, noCap) // MaxRecords 0 => no-op
	if got, _ := s.ListReviews(ctx, store.ReviewFilter{}); len(got) != 5 {
		t.Fatalf("no cap must keep all 5, got %d", len(got))
	}

	cap2 := config.Defaults()
	cap2.History.MaxRecords = 2
	pruneHistory(ctx, s, cap2)
	left, err := s.ListReviews(ctx, store.ReviewFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(left) != 2 {
		t.Fatalf("cap=2 must leave 2 newest, got %d", len(left))
	}

	// pruneHistory(nil, …) must be a safe no-op.
	pruneHistory(ctx, nil, cap2)
}
