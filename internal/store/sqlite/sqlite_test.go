package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/store"
)

func tempStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func sampleRecord() store.ReviewRecord {
	return store.ReviewRecord{
		RepoDir: "/repo",
		Mode:    "staged",
		HeadSHA: "abc123",
		Findings: []engine.Finding{
			{File: "a.go", Line: 10, EndLine: 12, Severity: "high", Category: "bug",
				Rationale: "nil deref", SuggestedPatch: "if x != nil {", QuotedCode: "x.Foo()"},
			{File: "b.go", Line: 3, Severity: "low", Category: "style", Rationale: "rename"},
		},
		Stats: map[string]any{"files_reviewed": float64(2), "max_severity": "high"},
	}
}

func TestSaveGetRoundTrip(t *testing.T) {
	s := tempStore(t)
	ctx := context.Background()
	in := sampleRecord()

	id, err := s.SaveReview(ctx, in)
	if err != nil {
		t.Fatalf("SaveReview: %v", err)
	}
	if id == "" {
		t.Fatal("expected generated id")
	}

	got, err := s.GetReview(ctx, id)
	if err != nil {
		t.Fatalf("GetReview: %v", err)
	}
	if got.ID != id || got.RepoDir != in.RepoDir || got.Mode != in.Mode || got.HeadSHA != in.HeadSHA {
		t.Fatalf("scalar mismatch: %+v", got)
	}
	if !reflect.DeepEqual(got.Findings, in.Findings) {
		t.Fatalf("findings mismatch:\n got %+v\nwant %+v", got.Findings, in.Findings)
	}
	if !reflect.DeepEqual(got.Stats, in.Stats) {
		t.Fatalf("stats mismatch:\n got %+v\nwant %+v", got.Stats, in.Stats)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("expected created_at populated")
	}
}

// JSON has no int type: numeric stats round-trip back as float64. Engine builds
// stats as float64 so in-memory and persisted records agree.
func TestStatsNumericRoundTripIsFloat64(t *testing.T) {
	s := tempStore(t)
	ctx := context.Background()
	in := store.ReviewRecord{
		RepoDir: "/repo",
		Mode:    "staged",
		Stats:   map[string]any{"findings_total": 3, "findings_dropped": 1},
	}
	id, err := s.SaveReview(ctx, in)
	if err != nil {
		t.Fatalf("SaveReview: %v", err)
	}
	got, err := s.GetReview(ctx, id)
	if err != nil {
		t.Fatalf("GetReview: %v", err)
	}
	for _, k := range []string{"findings_total", "findings_dropped"} {
		if _, ok := got.Stats[k].(float64); !ok {
			t.Errorf("%s: want float64 after round-trip, got %T (%v)", k, got.Stats[k], got.Stats[k])
		}
	}
	if got.Stats["findings_total"].(float64) != 3 {
		t.Errorf("findings_total value: want 3, got %v", got.Stats["findings_total"])
	}
}

func TestSchemaIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	id, err := s1.SaveReview(context.Background(), sampleRecord())
	if err != nil {
		t.Fatalf("SaveReview: %v", err)
	}
	_ = s1.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen (re-migrate): %v", err)
	}
	defer func() { _ = s2.Close() }()
	if _, err := s2.GetReview(context.Background(), id); err != nil {
		t.Fatalf("GetReview after reopen: %v", err)
	}
}

func TestGetMissing(t *testing.T) {
	s := tempStore(t)
	if _, err := s.GetReview(context.Background(), "nope"); err == nil {
		t.Fatal("expected error for missing id")
	}
}

// A caller-supplied ID must be preserved (the no-generate branch).
func TestSaveReviewPreservesCallerID(t *testing.T) {
	s := tempStore(t)
	ctx := context.Background()
	in := sampleRecord()
	in.ID = "fixed-id-123"
	id, err := s.SaveReview(ctx, in)
	if err != nil {
		t.Fatalf("SaveReview: %v", err)
	}
	if id != "fixed-id-123" {
		t.Fatalf("caller id not preserved: got %q", id)
	}
}

// EngineStore adapts engine.PersistRecord <-> store.ReviewRecord without loss.
func TestEngineStoreRoundTrip(t *testing.T) {
	s := tempStore(t)
	es := EngineStore{S: s}
	ctx := context.Background()
	in := engine.PersistRecord{
		RepoDir:  "/repo",
		Mode:     "commit",
		HeadSHA:  "deadbeef",
		Findings: []engine.Finding{{File: "x.go", Line: 1, Severity: "low", Category: "style"}},
		Stats:    map[string]any{"n": float64(1)},
	}
	id, err := es.SaveReview(ctx, in)
	if err != nil {
		t.Fatalf("EngineStore.SaveReview: %v", err)
	}
	got, err := es.GetReview(ctx, id)
	if err != nil {
		t.Fatalf("EngineStore.GetReview: %v", err)
	}
	if got.HeadSHA != in.HeadSHA || got.Mode != in.Mode || got.RepoDir != in.RepoDir {
		t.Fatalf("scalar mismatch: %+v", got)
	}
	if !reflect.DeepEqual(got.Findings, in.Findings) {
		t.Fatalf("findings mismatch: %+v vs %+v", got.Findings, in.Findings)
	}
}

// GetReview on a missing id wraps sql.ErrNoRows into a not-found message.
func TestGetReviewNotFoundMessage(t *testing.T) {
	s := tempStore(t)
	_, err := s.GetReview(context.Background(), "absent")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not-found error, got %v", err)
	}
}

// DefaultPath ends in miucr/state.db under the user config dir.
func TestDefaultPath(t *testing.T) {
	p, err := DefaultPath()
	if err != nil {
		t.Skipf("UserConfigDir unavailable: %v", err)
	}
	if filepath.Base(p) != "state.db" || filepath.Base(filepath.Dir(p)) != "miucr" {
		t.Fatalf("DefaultPath = %q, want .../miucr/state.db", p)
	}
}

// Open fails when the parent path can't be created (a file occupies a path segment).
func TestOpenBadPath(t *testing.T) {
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if _, err := Open(filepath.Join(file, "state.db")); err == nil {
		t.Fatal("expected Open to fail when a file blocks the parent dir")
	}
}

// nil Findings/Stats persist as empty (not NULL) and round-trip to non-nil.
func TestSaveReviewNilFindingsAndStats(t *testing.T) {
	s := tempStore(t)
	ctx := context.Background()
	id, err := s.SaveReview(ctx, store.ReviewRecord{RepoDir: "/r", Mode: "staged"})
	if err != nil {
		t.Fatalf("SaveReview: %v", err)
	}
	got, err := s.GetReview(ctx, id)
	if err != nil {
		t.Fatalf("GetReview: %v", err)
	}
	if got.Findings == nil || len(got.Findings) != 0 {
		t.Errorf("Findings = %v, want empty non-nil", got.Findings)
	}
	if got.Stats == nil || len(got.Stats) != 0 {
		t.Errorf("Stats = %v, want empty non-nil", got.Stats)
	}
}

func TestNoCredentialColumns(t *testing.T) {
	s := tempStore(t)
	ctx := context.Background()
	id, err := s.SaveReview(ctx, sampleRecord())
	if err != nil {
		t.Fatalf("SaveReview: %v", err)
	}

	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info(reviews)")
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		lower := strings.ToLower(name)
		for _, banned := range []string{"key", "token", "secret", "credential", "password", "auth"} {
			if strings.Contains(lower, banned) {
				t.Fatalf("credential-like column present: %s", name)
			}
		}
	}

	var blob string
	if err := s.db.QueryRowContext(ctx,
		"SELECT findings_json || stats_json FROM reviews WHERE id = ?", id).Scan(&blob); err != nil {
		t.Fatalf("read blob: %v", err)
	}
	for _, banned := range []string{"api_key", "ANTHROPIC_API_KEY", "sk-ant"} {
		if strings.Contains(blob, banned) {
			t.Fatalf("credential-like value persisted: %s", banned)
		}
	}
}
