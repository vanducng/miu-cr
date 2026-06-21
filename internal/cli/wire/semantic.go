package wire

import (
	stdctx "context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/embed"
	mgithub "github.com/vanducng/miu-cr/internal/github"
	"github.com/vanducng/miu-cr/internal/store"
	"github.com/vanducng/miu-cr/internal/store/postgres"
)

// newEmbedder builds the M7 Embedder from config, resolving the API key from the
// dedicated env override then the OpenAI default. Returns (nil, nil) when the
// semantic layer is disabled so callers degrade to byte-for-byte M6. The key is
// never persisted; it is held in-memory only.
var newEmbedder = func(cfg config.Embedding) (embed.Embedder, error) {
	cred := embed.Credential{APIKey: firstNonEmpty(os.Getenv("MIUCR_EMBED_API_KEY"), os.Getenv("OPENAI_API_KEY"))}
	return embed.New(cfg, cred)
}

// openEmbeddingStore opens the postgres EmbeddingStore for the configured DSN and
// embedding dim (pgvector stands up here). It is the postgres-only seam; sqlite
// omits it. Tests override this to inject a fake store with no network/PG.
var openEmbeddingStore = func(ctx stdctx.Context, cfg config.Config, dim int) (store.EmbeddingStore, func(), error) {
	dsn, err := requirePGDSN(cfg)
	if err != nil {
		return nil, nil, err
	}
	s, err := postgres.OpenWithEmbeddings(ctx, dsn, dim)
	if err != nil {
		return nil, nil, err
	}
	return s.Embedding(), func() { _ = s.Close() }, nil
}

// semanticEnabled reports whether the dual opt-in gate is on: [embedding].enabled
// AND backend=postgres. Off => no embedder, no Retriever, byte-for-byte M6.
func semanticEnabled(cfg config.Config) bool {
	return cfg.Embedding.Enabled && resolveBackend(cfg) == "postgres"
}

// buildSemantic builds the embedder + EmbeddingStore when the gate is on, else
// returns nils (=> M6). The returned closer (when non-nil) MUST be deferred by the
// caller. Best-effort: a build error is logged (redacted) and degrades to nils so
// the review still runs; the gate being explicitly on doesn't make a transient
// embedder/store failure fatal (the layer is advisory).
func buildSemantic(ctx stdctx.Context, cfg config.Config) (embed.Embedder, store.EmbeddingStore, func()) {
	if !semanticEnabled(cfg) {
		return nil, nil, nil
	}
	emb, err := newEmbedder(cfg.Embedding)
	if err != nil || emb == nil {
		if err != nil {
			slog.Warn("semantic layer disabled, embedder build failed: " + config.RedactString(err.Error()))
		}
		return nil, nil, nil
	}
	st, closeStore, err := openEmbeddingStore(ctx, cfg, emb.Dim())
	if err != nil {
		slog.Warn("semantic layer disabled, embedding store open failed: " + config.RedactString(err.Error()))
		return nil, nil, nil
	}
	return emb, st, closeStore
}

const (
	// semanticReadTimeout bounds the pre-agent read embed so a slow/hung embedder
	// degrades to empty + a stat rather than stalling the review. MVP (deferred):
	// the write path reuses this single bound — 8s is ample for the bounded batch
	// (~40 inline-capped findings); a dedicated write timeout is a future tweak.
	semanticReadTimeout = 8 * time.Second
	// semanticTopK is the number of prior cosine-near findings retrieved per review.
	semanticTopK = 5
	// semanticMaxLines caps how many changed-code lines are joined into the read
	// embed text, keeping the request bounded on a huge diff.
	semanticMaxLines = 400
)

// retriever is the engine.Retriever implementation for M7: it scrubs+embeds the
// current change's code anchors and returns advisory prose for the top-K prior
// cosine-near findings. Best-effort by construction — Related never returns a
// review-fatal error (engine treats any error as "no advisory"). The embedder and
// store are injected so tests drive it with a fakeEmbedder + fake store (keyless,
// serverless).
type retriever struct {
	emb   embed.Embedder
	store store.EmbeddingStore
	repo  string
}

var _ engine.Retriever = (*retriever)(nil)

// Related scrubs+embeds the changed code per hunk and queries the EmbeddingStore
// for cosine-near prior findings, rendering them as an advisory block. Each hunk
// is embedded as its own chunk with the SAME scrubCode the write path applies to
// a finding's QuotedCode, so read and write share one code-anchor representation
// and cosine search compares like with like. Short timeout; empty/error => "" so
// the engine emits the M6 prompt.
func (r *retriever) Related(ctx stdctx.Context, changedHunks [][]string) (string, error) {
	if r == nil || r.emb == nil || r.store == nil || len(changedHunks) == 0 {
		return "", nil
	}
	texts := make([]string, 0, len(changedHunks))
	for _, hunk := range changedHunks {
		t := scrubCode(hunk)
		if strings.TrimSpace(t) == "" {
			continue
		}
		texts = append(texts, t)
	}
	if len(texts) == 0 {
		return "", nil
	}

	cctx, cancel := stdctx.WithTimeout(ctx, semanticReadTimeout)
	defer cancel()

	vecs, err := r.emb.Embed(cctx, texts)
	if err != nil || len(vecs) == 0 {
		if err != nil {
			slog.Warn("semantic read embed failed: " + config.RedactString(err.Error()))
		}
		return "", nil
	}

	// One cosine query per hunk chunk; merge keeping the nearest distance per
	// fingerprint so a finding near several hunks is not double-counted.
	nearest := make(map[string]store.EmbeddingHit)
	for _, v := range vecs {
		hits, err := r.store.SimilarFindings(cctx, r.repo, r.emb.Model(), v, semanticTopK)
		if err != nil {
			slog.Warn("semantic similar-findings query failed: " + config.RedactString(err.Error()))
			return "", nil
		}
		for _, h := range hits {
			if cur, ok := nearest[h.Fingerprint]; !ok || h.Distance < cur.Distance {
				nearest[h.Fingerprint] = h
			}
		}
	}
	return renderAdvisory(topKHits(nearest, semanticTopK)), nil
}

// topKHits flattens the merged nearest-per-fingerprint map into the K nearest
// hits, ascending by cosine distance (ties broken by fingerprint for stability).
func topKHits(m map[string]store.EmbeddingHit, k int) []store.EmbeddingHit {
	out := make([]store.EmbeddingHit, 0, len(m))
	for _, h := range m {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Distance != out[j].Distance {
			return out[i].Distance < out[j].Distance
		}
		return out[i].Fingerprint < out[j].Fingerprint
	})
	if k > 0 && len(out) > k {
		out = out[:k]
	}
	return out
}

// renderAdvisory formats retrieved hits into a compact advisory list (nearest
// first). Empty hits => "" so the engine degrades to the M6 prompt.
func renderAdvisory(hits []store.EmbeddingHit) string {
	if len(hits) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, h := range hits {
		cat := strings.TrimSpace(h.Category)
		if cat == "" {
			cat = "finding"
		}
		rationale := strings.TrimSpace(h.Rationale)
		sb.WriteString("- [")
		sb.WriteString(cat)
		sb.WriteString("] ")
		sb.WriteString(rationale)
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// scrubCode joins one chunk's code lines (capped) and runs the existing credential
// redaction so no secret leaves the box in the embedded text. Shared by the read
// path (per hunk) and the write path (per finding's QuotedCode) so both embed the
// same code-anchor representation.
func scrubCode(lines []string) string {
	if len(lines) > semanticMaxLines {
		lines = lines[:semanticMaxLines]
	}
	return config.RedactString(strings.Join(lines, "\n"))
}

// embedWriter carries the embedder + store + repo into the post-publish write
// seam. A zero value (nil emb/store, off-gate) makes write() a no-op so the
// default path stays byte-for-byte M6.
type embedWriter struct {
	emb   embed.Embedder
	store store.EmbeddingStore
	repo  string
}

// write embeds + upserts the posted findings' code anchors, best-effort. Stats
// receives a semantic_write entry when an embed actually runs.
func (w embedWriter) write(ctx stdctx.Context, posted []mgithub.PostedFinding, current []engine.Finding, stats map[string]any) {
	writeFindingEmbeddings(ctx, w.emb, w.store, w.repo, posted, current, stats)
}

// writeFindingEmbeddings embeds each POSTED finding's secret-scrubbed code anchor
// and upserts it. posted carries only (Fingerprint, Path); the code anchor lives
// on current []engine.Finding, joined via mgithub.Fingerprint. Best-effort: any
// error is logged (redacted) and a stat is set on out; it never fails the review.
func writeFindingEmbeddings(ctx stdctx.Context, emb embed.Embedder, st store.EmbeddingStore, repo string, posted []mgithub.PostedFinding, current []engine.Finding, stats map[string]any) {
	if emb == nil || st == nil || len(posted) == 0 || len(current) == 0 {
		return
	}
	byFP := make(map[string]engine.Finding, len(current))
	for _, f := range current {
		byFP[mgithub.Fingerprint(f)] = f
	}

	type job struct {
		fp   string
		f    engine.Finding
		text string
	}
	jobs := make([]job, 0, len(posted))
	texts := make([]string, 0, len(posted))
	for _, pf := range posted {
		f, ok := byFP[pf.Fingerprint]
		if !ok {
			continue
		}
		text := scrubCode([]string{f.QuotedCode})
		if strings.TrimSpace(text) == "" {
			continue
		}
		jobs = append(jobs, job{fp: pf.Fingerprint, f: f, text: text})
		texts = append(texts, text)
	}
	if len(jobs) == 0 {
		return
	}

	cctx, cancel := stdctx.WithTimeout(ctx, semanticReadTimeout)
	defer cancel()

	vecs, err := emb.Embed(cctx, texts)
	if err != nil || len(vecs) != len(jobs) {
		if err != nil {
			slog.Warn("semantic write embed failed: " + config.RedactString(err.Error()))
		}
		setStat(stats, "semantic_write", "error")
		return
	}

	// MVP (deferred): one upsert per posted finding (N+1) — bounded by the ~40
	// inline-comment cap, so a batch INSERT is a future write-path opt (YAGNI).
	written := 0
	for i, j := range jobs {
		row := store.EmbeddingRow{
			Repo:        repo,
			Fingerprint: j.fp,
			Model:       emb.Model(),
			Category:    j.f.Category,
			Rationale:   j.f.Rationale,
			ContentHash: contentHash(j.text),
			Vec:         vecs[i],
		}
		if uerr := st.UpsertFindingEmbedding(cctx, row); uerr != nil {
			slog.Warn("semantic upsert failed: " + config.RedactString(uerr.Error()))
			continue
		}
		written++
	}
	setStat(stats, "semantic_write", fmt.Sprintf("upserted=%d", written))
}

func contentHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func setStat(stats map[string]any, key, val string) {
	if stats != nil {
		stats[key] = val
	}
}

// repoKey is a stable repo identity for embedding rows: owner/repo. Kept here so
// the read and write paths key identically. Plain concat is unambiguous for
// GitHub (owner/repo cannot contain '/'); deferred for any future non-GitHub host.
func repoKey(owner, repo string) string {
	return owner + "/" + repo
}
