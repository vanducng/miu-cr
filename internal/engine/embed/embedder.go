// Package embed provides the opt-in code-embedding layer for M7's semantic
// recall. Embedder is a small interface so wire can inject a real
// OpenAI-compatible client or a deterministic fake; the engine never imports it
// directly (it talks to an engine-local Retriever).
package embed

import (
	stdctx "context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"strings"

	openai "github.com/openai/openai-go/v3"
	openaiopt "github.com/openai/openai-go/v3/option"

	"github.com/vanducng/miu-cr/internal/config"
)

// Embedder turns text into fixed-dimension vectors for cosine recall. Embed
// returns one vector per input text, in order. Implementations must never log
// or persist the API key.
type Embedder interface {
	Embed(ctx stdctx.Context, texts []string) ([][]float32, error)
	Model() string
	Dim() int
}

// Credential is the resolved, in-memory-only key for the embedder, mirrored from
// the LLM credential chain by wire (env/flag/config). It is never persisted or
// placed in the envelope; see config.RedactString for redaction at the edges.
type Credential struct {
	APIKey string
}

// embeddingService is the slice of the OpenAI SDK the embedder needs; a fake
// satisfies it in tests so the cast/encode path runs without network.
type embeddingService interface {
	New(ctx stdctx.Context, body openai.EmbeddingNewParams, opts ...openaiopt.RequestOption) (*openai.CreateEmbeddingResponse, error)
}

type openaiEmbedder struct {
	svc   embeddingService
	model string
	dim   int
}

func (e *openaiEmbedder) Model() string { return e.model }
func (e *openaiEmbedder) Dim() int      { return e.dim }

func (e *openaiEmbedder) Embed(ctx stdctx.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	params := openai.EmbeddingNewParams{
		Model: e.model,
		Input: openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: texts},
	}
	if e.dim > 0 {
		params.Dimensions = openai.Int(int64(e.dim))
	}
	resp, err := e.svc.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("embed: embeddings request: %w", err)
	}
	if len(resp.Data) != len(texts) {
		return nil, fmt.Errorf("embed: response count %d != input count %d", len(resp.Data), len(texts))
	}
	// The API does not guarantee resp.Data is in input order; place each datum at
	// its own .Index so embeddings never get mis-assigned to the wrong text.
	out := make([][]float32, len(texts))
	for _, d := range resp.Data {
		idx := int(d.Index)
		if idx < 0 || idx >= len(out) {
			return nil, fmt.Errorf("embed: response index %d out of range [0,%d)", idx, len(out))
		}
		if out[idx] != nil {
			return nil, fmt.Errorf("embed: duplicate response index %d", idx)
		}
		out[idx] = toFloat32(d.Embedding)
	}
	for i := range out {
		if out[i] == nil {
			return nil, fmt.Errorf("embed: missing embedding for input %d", i)
		}
	}
	return out, nil
}

func toFloat32(in []float64) []float32 {
	out := make([]float32, len(in))
	for i, v := range in {
		out[i] = float32(v)
	}
	return out
}

// New builds an Embedder from config, returning (nil, nil) when the semantic
// layer is disabled so callers degrade to byte-for-byte M6. The credential is
// passed in (resolved by wire); it is never read from disk here. Only the
// OpenAI-compatible path is implemented; an unknown provider is a typed config
// error surfaced by the caller.
func New(cfg config.Embedding, cred Credential) (Embedder, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	model := cfg.Model
	if model == "" {
		model = config.DefaultEmbeddingModel
	}
	dim := cfg.Dim
	if dim == 0 {
		dim = config.DefaultEmbeddingDim
	}
	if dim < 1 || dim > config.MaxEmbeddingDim {
		return nil, fmt.Errorf("embed: invalid embedding dim %d (must be in [1,%d])", dim, config.MaxEmbeddingDim)
	}
	provider := strings.TrimSpace(cfg.Provider)
	if provider != "" && provider != string(config.KindOpenAI) {
		return nil, fmt.Errorf("embed: unsupported embedding provider %q (only %q)", provider, config.KindOpenAI)
	}

	opts := []openaiopt.RequestOption{
		openaiopt.WithAPIKey(cred.APIKey),
		openaiopt.WithMaxRetries(2),
	}
	if base := strings.TrimRight(cfg.BaseURL, "/"); base != "" {
		opts = append(opts, openaiopt.WithBaseURL(base))
	}
	sdk := openai.NewClient(opts...)
	return &openaiEmbedder{svc: &sdk.Embeddings, model: model, dim: dim}, nil
}

// fakeEmbedder is a deterministic, network-free Embedder for tests: each text
// maps to a stable unit vector derived from its SHA-256. Same text -> same
// vector; close texts are not guaranteed near (it is for plumbing/determinism
// tests, not semantic quality).
type fakeEmbedder struct {
	model string
	dim   int
}

// NewFake returns a deterministic Embedder for tests. dim<=0 falls back to the
// default embedding dim.
func NewFake(model string, dim int) Embedder {
	if dim <= 0 {
		dim = config.DefaultEmbeddingDim
	}
	if model == "" {
		model = config.DefaultEmbeddingModel
	}
	return &fakeEmbedder{model: model, dim: dim}
}

func (f *fakeEmbedder) Model() string { return f.model }
func (f *fakeEmbedder) Dim() int      { return f.dim }

func (f *fakeEmbedder) Embed(_ stdctx.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = f.vector(t)
	}
	return out, nil
}

func (f *fakeEmbedder) vector(text string) []float32 {
	v := make([]float32, f.dim)
	var sum float64
	for i := range v {
		seed := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", i, text)))
		u := binary.LittleEndian.Uint64(seed[:8])
		// Scale in float64: float32(math.MaxUint64) is +Inf, so dividing in float32
		// yields NaN/constant and collapses every text onto the same vector.
		x := float32(float64(u)/float64(math.MaxUint64)*2 - 1)
		v[i] = x
		sum += float64(x) * float64(x)
	}
	norm := float32(math.Sqrt(sum))
	if norm > 0 {
		for i := range v {
			v[i] /= norm
		}
	}
	return v
}
