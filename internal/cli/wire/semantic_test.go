package wire

import (
	stdctx "context"
	"errors"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/embed"
	mgithub "github.com/vanducng/miu-cr/internal/github"
	"github.com/vanducng/miu-cr/internal/store"
)

// captureEmbedder records the texts it was asked to embed so a test can assert
// the secret-scrub ran before anything left the box. It delegates vector
// generation to the deterministic fake.
type captureEmbedder struct {
	inner embed.Embedder
	seen  []string
	err   error
}

func newCaptureEmbedder() *captureEmbedder {
	return &captureEmbedder{inner: embed.NewFake("text-embedding-3-small", 8)}
}

func (c *captureEmbedder) Embed(ctx stdctx.Context, texts []string) ([][]float32, error) {
	c.seen = append(c.seen, texts...)
	if c.err != nil {
		return nil, c.err
	}
	return c.inner.Embed(ctx, texts)
}
func (c *captureEmbedder) Model() string { return c.inner.Model() }
func (c *captureEmbedder) Dim() int      { return c.inner.Dim() }

// fakeEmbeddingStore is an in-memory store.EmbeddingStore for tests: no PG, no
// network. SimilarFindings returns the canned hits; Upsert records rows.
type fakeEmbeddingStore struct {
	hits      []store.EmbeddingHit
	simErr    error
	upsertErr error
	upserted  []store.EmbeddingRow
	gotRepo   string
	gotModel  string
}

func (s *fakeEmbeddingStore) UpsertFindingEmbedding(_ stdctx.Context, row store.EmbeddingRow) error {
	if s.upsertErr != nil {
		return s.upsertErr
	}
	s.upserted = append(s.upserted, row)
	return nil
}

func (s *fakeEmbeddingStore) SimilarFindings(_ stdctx.Context, repo, model string, _ []float32, _ int) ([]store.EmbeddingHit, error) {
	s.gotRepo, s.gotModel = repo, model
	return s.hits, s.simErr
}

func TestRetrieverHitsRenderAdvisory(t *testing.T) {
	st := &fakeEmbeddingStore{hits: []store.EmbeddingHit{
		{Fingerprint: "fp1", Category: "bug", Rationale: "off-by-one", Distance: 0.1},
		{Fingerprint: "fp2", Category: "security", Rationale: "hardcoded key", Distance: 0.2},
	}}
	r := &retriever{emb: newCaptureEmbedder(), store: st, repo: "o/r"}
	got, err := r.Related(stdctx.Background(), []string{"x := 1"})
	if err != nil {
		t.Fatalf("Related: %v", err)
	}
	if !strings.Contains(got, "off-by-one") || !strings.Contains(got, "hardcoded key") {
		t.Fatalf("advisory missing hit rationale: %q", got)
	}
	if st.gotRepo != "o/r" || st.gotModel != "text-embedding-3-small" {
		t.Fatalf("query not scoped by repo+model: repo=%q model=%q", st.gotRepo, st.gotModel)
	}
}

func TestRetrieverZeroHitsEmpty(t *testing.T) {
	r := &retriever{emb: newCaptureEmbedder(), store: &fakeEmbeddingStore{}, repo: "o/r"}
	got, err := r.Related(stdctx.Background(), []string{"x := 1"})
	if err != nil || got != "" {
		t.Fatalf("zero hits must yield empty advisory, got err=%v out=%q", err, got)
	}
}

func TestRetrieverEmbedErrorEmpty(t *testing.T) {
	emb := newCaptureEmbedder()
	emb.err = errors.New("embedder timeout")
	r := &retriever{emb: emb, store: &fakeEmbeddingStore{}, repo: "o/r"}
	got, err := r.Related(stdctx.Background(), []string{"x := 1"})
	if err != nil || got != "" {
		t.Fatalf("embed error must degrade to empty (no error), got err=%v out=%q", err, got)
	}
}

func TestRetrieverSimilarErrorEmpty(t *testing.T) {
	r := &retriever{emb: newCaptureEmbedder(), store: &fakeEmbeddingStore{simErr: errors.New("pg down")}, repo: "o/r"}
	got, err := r.Related(stdctx.Background(), []string{"x := 1"})
	if err != nil || got != "" {
		t.Fatalf("similar-findings error must degrade to empty, got err=%v out=%q", err, got)
	}
}

func TestRetrieverScrubsSecretBeforeEmbed(t *testing.T) {
	emb := newCaptureEmbedder()
	r := &retriever{emb: emb, store: &fakeEmbeddingStore{}, repo: "o/r"}
	secret := "api_key=sk-supersecret1234567890"
	if _, err := r.Related(stdctx.Background(), []string{"line one", secret}); err != nil {
		t.Fatalf("Related: %v", err)
	}
	if len(emb.seen) == 0 {
		t.Fatal("embedder never called")
	}
	for _, s := range emb.seen {
		if strings.Contains(s, "sk-supersecret1234567890") {
			t.Fatalf("planted secret leaked into embed text: %q", s)
		}
	}
}

func TestWriteFindingEmbeddingsEmbedsPostedAnchors(t *testing.T) {
	emb := newCaptureEmbedder()
	st := &fakeEmbeddingStore{}
	current := []engine.Finding{
		{File: "a.go", Category: "bug", Rationale: "off-by-one", QuotedCode: "i <= n"},
		{File: "b.go", Category: "security", Rationale: "leak", QuotedCode: "api_key=sk-leak1234567890"},
	}
	// Only the first finding is posted; the join recovers its QuotedCode.
	posted := []mgithub.PostedFinding{{Fingerprint: mgithub.Fingerprint(current[0]), Path: "a.go"}}
	stats := map[string]any{}
	writeFindingEmbeddings(stdctx.Background(), emb, st, "o/r", posted, current, stats)

	if len(st.upserted) != 1 {
		t.Fatalf("want 1 upsert for the single posted finding, got %d", len(st.upserted))
	}
	row := st.upserted[0]
	if row.Repo != "o/r" || row.Model != "text-embedding-3-small" || row.Category != "bug" {
		t.Fatalf("row metadata wrong: %+v", row)
	}
	if len(row.Vec) != 8 {
		t.Fatalf("vector dim wrong: %d", len(row.Vec))
	}
	if v, _ := stats["semantic_write"].(string); v != "upserted=1" {
		t.Fatalf("semantic_write stat: want upserted=1, got %v", stats["semantic_write"])
	}
}

func TestWriteFindingEmbeddingsScrubsSecret(t *testing.T) {
	emb := newCaptureEmbedder()
	st := &fakeEmbeddingStore{}
	current := []engine.Finding{{File: "b.go", Category: "security", QuotedCode: "token=sk-leak1234567890abc"}}
	posted := []mgithub.PostedFinding{{Fingerprint: mgithub.Fingerprint(current[0]), Path: "b.go"}}
	writeFindingEmbeddings(stdctx.Background(), emb, st, "o/r", posted, current, map[string]any{})
	for _, s := range emb.seen {
		if strings.Contains(s, "sk-leak1234567890abc") {
			t.Fatalf("write path leaked a secret into embed text: %q", s)
		}
	}
}

func TestWriteFindingEmbeddingsUpsertErrorBestEffort(t *testing.T) {
	emb := newCaptureEmbedder()
	st := &fakeEmbeddingStore{upsertErr: errors.New("pg write failed")}
	current := []engine.Finding{{File: "a.go", Category: "bug", QuotedCode: "i <= n"}}
	posted := []mgithub.PostedFinding{{Fingerprint: mgithub.Fingerprint(current[0]), Path: "a.go"}}
	stats := map[string]any{}
	// Must not panic / must not propagate (best-effort).
	writeFindingEmbeddings(stdctx.Background(), emb, st, "o/r", posted, current, stats)
	if v, _ := stats["semantic_write"].(string); v != "upserted=0" {
		t.Fatalf("upsert error stat: want upserted=0, got %v", stats["semantic_write"])
	}
}

func TestSemanticEnabledGate(t *testing.T) {
	cases := []struct {
		name    string
		enabled bool
		backend string
		want    bool
	}{
		{"off by default", false, "sqlite", false},
		{"enabled but sqlite", true, "sqlite", false},
		{"enabled but unset backend", true, "", false},
		{"enabled + postgres", true, "postgres", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MIUCR_STORE_BACKEND", "")
			cfg := config.Config{
				Embedding: config.Embedding{Enabled: tc.enabled},
				Store:     config.Store{Backend: tc.backend},
			}
			if got := semanticEnabled(cfg); got != tc.want {
				t.Fatalf("semanticEnabled=%v want %v", got, tc.want)
			}
		})
	}
}

func TestBuildSemanticGateOffReturnsNils(t *testing.T) {
	t.Setenv("MIUCR_STORE_BACKEND", "")
	cfg := config.Config{Embedding: config.Embedding{Enabled: false}, Store: config.Store{Backend: "sqlite"}}
	emb, st, closer := buildSemantic(stdctx.Background(), cfg)
	if emb != nil || st != nil || closer != nil {
		t.Fatalf("gate off must return all-nils, got emb=%v st=%v closer=%v", emb, st, closer != nil)
	}
}

func TestBuildSemanticEmbedderBuildFailureDegrades(t *testing.T) {
	t.Setenv("MIUCR_STORE_BACKEND", "")
	orig := newEmbedder
	newEmbedder = func(config.Embedding) (embed.Embedder, error) { return nil, errors.New("bad provider") }
	defer func() { newEmbedder = orig }()

	cfg := config.Config{Embedding: config.Embedding{Enabled: true}, Store: config.Store{Backend: "postgres"}}
	emb, st, closer := buildSemantic(stdctx.Background(), cfg)
	if emb != nil || st != nil || closer != nil {
		t.Fatalf("embedder build failure must degrade to nils, got emb=%v st=%v closer=%v", emb, st, closer != nil)
	}
}

func TestBuildSemanticStoreOpenFailureDegrades(t *testing.T) {
	t.Setenv("MIUCR_STORE_BACKEND", "")
	origE, origS := newEmbedder, openEmbeddingStore
	newEmbedder = func(config.Embedding) (embed.Embedder, error) { return embed.NewFake("m", 8), nil }
	openEmbeddingStore = func(stdctx.Context, config.Config, int) (store.EmbeddingStore, func(), error) {
		return nil, nil, errors.New("pg unavailable")
	}
	defer func() { newEmbedder, openEmbeddingStore = origE, origS }()

	cfg := config.Config{Embedding: config.Embedding{Enabled: true}, Store: config.Store{Backend: "postgres"}}
	emb, st, closer := buildSemantic(stdctx.Background(), cfg)
	if emb != nil || st != nil || closer != nil {
		t.Fatalf("store open failure must degrade to nils, got emb=%v st=%v closer=%v", emb, st, closer != nil)
	}
}

func TestBuildSemanticHappyPath(t *testing.T) {
	t.Setenv("MIUCR_STORE_BACKEND", "")
	origE, origS := newEmbedder, openEmbeddingStore
	fakeSt := &fakeEmbeddingStore{}
	closed := false
	newEmbedder = func(config.Embedding) (embed.Embedder, error) { return embed.NewFake("m", 8), nil }
	openEmbeddingStore = func(stdctx.Context, config.Config, int) (store.EmbeddingStore, func(), error) {
		return fakeSt, func() { closed = true }, nil
	}
	defer func() { newEmbedder, openEmbeddingStore = origE, origS }()

	cfg := config.Config{Embedding: config.Embedding{Enabled: true}, Store: config.Store{Backend: "postgres"}}
	emb, st, closer := buildSemantic(stdctx.Background(), cfg)
	if emb == nil || st == nil || closer == nil {
		t.Fatalf("happy path must return non-nils, got emb=%v st=%v closer=%v", emb, st, closer != nil)
	}
	closer()
	if !closed {
		t.Fatal("closer did not close the store")
	}
}
