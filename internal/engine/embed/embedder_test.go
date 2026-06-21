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
		data[i] = openai.Embedding{Embedding: vec}
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
