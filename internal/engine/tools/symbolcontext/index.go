package symbolcontext

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

// Index is a per-review snapshot of the reviewed revision's symbol surface:
// the scannable file list, definitions per file, a by-name lookup mirroring
// symbolMatches, and dbt refs per SQL file. It builds lazily exactly once on
// first use (same file caps and read machinery as a per-call scan) and is
// read-only afterwards, so parallel tool calls share one repo scan.
type Index struct {
	cfg config.SymbolContext
	tc  Context

	once       sync.Once
	ready      bool
	files      []string
	defsByPath map[string][]Definition
	defsByName map[string][]Definition
	refsByPath map[string][]string
}

func NewIndex(cfg config.SymbolContext, tc Context) *Index {
	tc.Index = nil // the index builds with a plain scanner, never through itself
	return &Index{cfg: cfg, tc: tc}
}

// ensure builds the index on first call; false means the build failed and the
// caller must fall back to per-call scanning (permanently, by design).
func (ix *Index) ensure(ctx context.Context) bool {
	if ix == nil {
		return false
	}
	ix.once.Do(func() { ix.build(ctx) })
	return ix.ready
}

func (ix *Index) build(ctx context.Context) {
	tc := ix.tc
	if tc.Runner == nil {
		tc.Runner = gitcmd.New()
	}
	s := &scanner{cfg: ix.cfg, tc: tc}
	paths, err := s.paths(ctx, "")
	if err != nil {
		return
	}
	defsByPath := make(map[string][]Definition, len(paths))
	defsByName := map[string][]Definition{}
	refsByPath := map[string][]string{}
	batchSize := s.scanBatchSize()
	for start := 0; start < len(paths); start += batchSize {
		// A cancelled read leaves empty results with nil errs; abort rather than
		// cache files as symbol-free.
		if ctx.Err() != nil {
			return
		}
		end := start + batchSize
		if end > len(paths) {
			end = len(paths)
		}
		for _, res := range s.readMany(ctx, paths[start:end]) {
			if res.err != nil {
				continue
			}
			defs := extractDefinitions(res.path, res.text)
			defsByPath[res.path] = defs
			for _, d := range defs {
				for _, key := range nameKeys(d.Name) {
					defsByName[key] = append(defsByName[key], d)
				}
			}
			if strings.ToLower(filepath.Ext(res.path)) == ".sql" {
				refsByPath[res.path] = extractDBTRefs(res.text)
			}
		}
	}
	if ctx.Err() != nil {
		return
	}
	ix.files = paths
	ix.defsByPath = defsByPath
	ix.defsByName = defsByName
	ix.refsByPath = refsByPath
	ix.ready = true
}

// Lookup returns the indexed definitions matching symbol under symbolMatches
// semantics (exact, case-insensitive, dot-suffix), sorted by file then line.
// nil when the index is unavailable or nothing matches.
func (ix *Index) Lookup(ctx context.Context, symbol string) []Definition {
	if !ix.ensure(ctx) {
		return nil
	}
	var out []Definition
	for _, d := range ix.defsByName[strings.ToLower(symbol)] {
		if symbolMatches(d.Name, symbol, d.File) {
			out = append(out, d)
		}
	}
	sortDefinitions(out)
	return out
}

// FileDefinitions returns the indexed definitions for one repo-relative path;
// ok is false when the index is unavailable or the path was not scanned (the
// caller must fall back to a per-call read).
func (ix *Index) FileDefinitions(ctx context.Context, path string) ([]Definition, bool) {
	if !ix.ensure(ctx) {
		return nil, false
	}
	defs, ok := ix.defsByPath[path]
	return defs, ok
}

// sqlRefs returns the indexed dbt refs for one .sql path; ok mirrors
// FileDefinitions.
func (ix *Index) sqlRefs(ctx context.Context, path string) ([]string, bool) {
	if !ix.ensure(ctx) {
		return nil, false
	}
	refs, ok := ix.refsByPath[path]
	return refs, ok
}

// filesList returns the capped scannable file list; ok mirrors FileDefinitions.
func (ix *Index) filesList(ctx context.Context) ([]string, bool) {
	if !ix.ensure(ctx) {
		return nil, false
	}
	return ix.files, true
}

// nameKeys lists the by-name lookup keys for a definition name: the lowercased
// name plus every dot-boundary suffix, mirroring symbolMatches.
func nameKeys(name string) []string {
	key := strings.ToLower(name)
	keys := []string{key}
	for {
		i := strings.IndexByte(key, '.')
		if i < 0 {
			return keys
		}
		key = key[i+1:]
		keys = append(keys, key)
	}
}
