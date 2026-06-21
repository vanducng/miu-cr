package embed

import (
	stdctx "context"
	"math"
	"testing"

	openai "github.com/openai/openai-go/v3"
	openaiopt "github.com/openai/openai-go/v3/option"

	"github.com/vanducng/miu-cr/internal/config"
)

func TestNewReturnsNilWhenDisabled(t *testing.T) {
	e, err := New(config.Embedding{Enabled: false}, Credential{APIKey: "ignored"})
	if err != nil {
		t.Fatalf("New disabled: unexpected err %v", err)
	}
	if e != nil {
		t.Fatalf("New must return nil Embedder when disabled, got %#v", e)
	}
}

func TestNewDefaultsModelAndDim(t *testing.T) {
	e, err := New(config.Embedding{Enabled: true}, Credential{APIKey: "k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e == nil {
		t.Fatal("New enabled must return an Embedder")
	}
	if e.Model() != config.DefaultEmbeddingModel {
		t.Fatalf("model default: want %q got %q", config.DefaultEmbeddingModel, e.Model())
	}
	if e.Dim() != config.DefaultEmbeddingDim {
		t.Fatalf("dim default: want %d got %d", config.DefaultEmbeddingDim, e.Dim())
	}
}

func TestNewSurfacesConfiguredModelDimBaseURL(t *testing.T) {
	cfg := config.Embedding{Enabled: true, Model: "text-embedding-3-large", Dim: 256, BaseURL: "https://gw.example/v1"}
	e, err := New(cfg, Credential{APIKey: "k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e.Model() != "text-embedding-3-large" {
		t.Fatalf("model: got %q", e.Model())
	}
	if e.Dim() != 256 {
		t.Fatalf("dim: got %d", e.Dim())
	}
}

func TestNewRejectsUnsupportedProvider(t *testing.T) {
	_, err := New(config.Embedding{Enabled: true, Provider: "anthropic"}, Credential{APIKey: "k"})
	if err == nil {
		t.Fatal("expected error for unsupported embedding provider")
	}
}

func TestFakeEmbedderDeterministic(t *testing.T) {
	f := NewFake("m", 16)
	if f.Model() != "m" || f.Dim() != 16 {
		t.Fatalf("fake surface: model=%q dim=%d", f.Model(), f.Dim())
	}
	a, err := f.Embed(stdctx.Background(), []string{"func add(a,b int) int", "func add(a,b int) int", "different"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(a) != 3 {
		t.Fatalf("want 3 vectors, got %d", len(a))
	}
	for i, v := range a {
		if len(v) != 16 {
			t.Fatalf("vec %d dim: want 16 got %d", i, len(v))
		}
	}
	// Same text -> identical vector.
	for i := range a[0] {
		if a[0][i] != a[1][i] {
			t.Fatalf("same input must yield identical vector at %d: %v vs %v", i, a[0][i], a[1][i])
		}
	}
	// Different text -> different vector.
	same := true
	for i := range a[0] {
		if a[0][i] != a[2][i] {
			same = false
			break
		}
	}
	if same {
		t.Fatal("different input produced identical vector")
	}
	// Unit-normalized.
	var sum float64
	for _, x := range a[0] {
		sum += float64(x) * float64(x)
	}
	if math.Abs(math.Sqrt(sum)-1) > 1e-4 {
		t.Fatalf("vector not unit-normalized: norm=%v", math.Sqrt(sum))
	}
}

// fakeService records the request and returns deterministic float64 vectors, so
// the openaiEmbedder cast/encode path is exercised without network.
type fakeService struct {
	gotModel string
	gotDim   int64
	gotInput []string
	dim      int
}

func (s *fakeService) New(_ stdctx.Context, body openai.EmbeddingNewParams, _ ...openaiopt.RequestOption) (*openai.CreateEmbeddingResponse, error) {
	s.gotModel = body.Model
	s.gotDim = body.Dimensions.Or(0)
	s.gotInput = body.Input.OfArrayOfStrings
	data := make([]openai.Embedding, len(s.gotInput))
	for i := range s.gotInput {
		vec := make([]float64, s.dim)
		for j := range vec {
			vec[j] = float64(i*s.dim + j)
		}
		data[i] = openai.Embedding{Index: int64(i), Embedding: vec}
	}
	return &openai.CreateEmbeddingResponse{Data: data}, nil
}

func TestOpenAIEmbedderCastsFloat64ToFloat32(t *testing.T) {
	svc := &fakeService{dim: 4}
	e := &openaiEmbedder{svc: svc, model: "text-embedding-3-small", dim: 4}
	out, err := e.Embed(stdctx.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if svc.gotModel != "text-embedding-3-small" {
		t.Fatalf("model not plumbed: %q", svc.gotModel)
	}
	if svc.gotDim != 4 {
		t.Fatalf("dimensions not plumbed: %d", svc.gotDim)
	}
	if len(out) != 2 || len(out[1]) != 4 {
		t.Fatalf("unexpected shape %v", out)
	}
	if out[1][0] != 4 || out[1][3] != 7 {
		t.Fatalf("float64->float32 cast wrong: %v", out[1])
	}
}

func TestOpenAIEmbedderEmptyInput(t *testing.T) {
	e := &openaiEmbedder{svc: &fakeService{dim: 4}, model: "m", dim: 4}
	out, err := e.Embed(stdctx.Background(), nil)
	if err != nil || out != nil {
		t.Fatalf("empty input: out=%v err=%v", out, err)
	}
}

// reorderService returns resp.Data in REVERSE input order, each tagged with its
// true .Index and a component value that encodes that index, so a position-based
// assembler would mis-map embeddings and the test catches it.
type reorderService struct{ dim int }

func (s *reorderService) New(_ stdctx.Context, body openai.EmbeddingNewParams, _ ...openaiopt.RequestOption) (*openai.CreateEmbeddingResponse, error) {
	in := body.Input.OfArrayOfStrings
	data := make([]openai.Embedding, len(in))
	for i := range in {
		vec := make([]float64, s.dim)
		for j := range vec {
			vec[j] = float64(i) // every component encodes the input index
		}
		data[len(in)-1-i] = openai.Embedding{Index: int64(i), Embedding: vec} // reversed slot, true Index
	}
	return &openai.CreateEmbeddingResponse{Data: data}, nil
}

func TestOpenAIEmbedderReassemblesByIndex(t *testing.T) {
	e := &openaiEmbedder{svc: &reorderService{dim: 3}, model: "m", dim: 3}
	out, err := e.Embed(stdctx.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	for i := range out {
		if out[i][0] != float32(i) {
			t.Fatalf("input %d mapped to vector tagged %v: assembly used position, not .Index", i, out[i][0])
		}
	}
}

// badIndexService returns an Index outside [0,len) so the embedder must error
// rather than panic or silently drop the input.
type badIndexService struct{ dim int }

func (s *badIndexService) New(_ stdctx.Context, body openai.EmbeddingNewParams, _ ...openaiopt.RequestOption) (*openai.CreateEmbeddingResponse, error) {
	in := body.Input.OfArrayOfStrings
	data := make([]openai.Embedding, len(in))
	for i := range in {
		data[i] = openai.Embedding{Index: int64(len(in) + i), Embedding: make([]float64, s.dim)}
	}
	return &openai.CreateEmbeddingResponse{Data: data}, nil
}

func TestOpenAIEmbedderRejectsOutOfRangeIndex(t *testing.T) {
	e := &openaiEmbedder{svc: &badIndexService{dim: 3}, model: "m", dim: 3}
	if _, err := e.Embed(stdctx.Background(), []string{"a", "b"}); err == nil {
		t.Fatal("out-of-range response index must error")
	}
}

func TestNewRejectsInvalidDim(t *testing.T) {
	for _, dim := range []int{-1, config.MaxEmbeddingDim + 1} {
		if _, err := New(config.Embedding{Enabled: true, Dim: dim}, Credential{APIKey: "k"}); err == nil {
			t.Fatalf("dim %d must be rejected", dim)
		}
	}
	// dim 0 is the "inherit default" sentinel, not invalid.
	if _, err := New(config.Embedding{Enabled: true, Dim: 0}, Credential{APIKey: "k"}); err != nil {
		t.Fatalf("dim 0 must default, got %v", err)
	}
}

func TestFakeEmbedderComponentsFiniteAndUnit(t *testing.T) {
	f := NewFake("m", 64)
	for _, text := range []string{"", "a", "func foo() {}", "x := 1 / 0"} {
		out, err := f.Embed(stdctx.Background(), []string{text})
		if err != nil {
			t.Fatalf("Embed(%q): %v", text, err)
		}
		var sum float64
		for i, c := range out[0] {
			if math.IsInf(float64(c), 0) || math.IsNaN(float64(c)) {
				t.Fatalf("text %q component %d not finite: %v", text, i, c)
			}
			sum += float64(c) * float64(c)
		}
		if math.Abs(math.Sqrt(sum)-1) > 1e-4 {
			t.Fatalf("text %q not unit-norm: %v", text, math.Sqrt(sum))
		}
	}
}
