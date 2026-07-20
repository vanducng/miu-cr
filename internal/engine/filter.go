package engine

import (
	"path"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/vanducng/miu-cr/internal/engine/diff"
)

// FilterOptions selects which diffs are reviewable before any LLM runs.
type FilterOptions struct {
	Extensions []string // allowlist of file extensions (with or without leading dot); empty = allow all
	Include    []string // doublestar globs; if non-empty a path must match one
	Exclude    []string // doublestar globs; matching paths are dropped
}

// SelectFiles returns the reviewable diffs: drops binary files, applies the
// extension allowlist, then doublestar include/exclude globs.
func SelectFiles(diffs []diff.Diff, opts FilterOptions) []diff.Diff {
	exts := normalizeExtensions(opts.Extensions)
	out := make([]diff.Diff, 0, len(diffs))
	for _, d := range diffs {
		if d.IsBinary {
			continue
		}
		p := d.ReviewPath()
		if p == "" || p == "/dev/null" {
			continue
		}
		if !matchesExtension(p, exts) {
			continue
		}
		if !matchesGlobs(p, opts.Include, opts.Exclude) {
			continue
		}
		out = append(out, d)
	}
	return out
}

func normalizeExtensions(in []string) map[string]bool {
	if len(in) == 0 {
		return nil
	}
	m := make(map[string]bool, len(in))
	for _, e := range in {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		e = strings.ToLower(strings.TrimPrefix(e, "."))
		m[e] = true
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

func matchesExtension(p string, exts map[string]bool) bool {
	if exts == nil {
		return true
	}
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(p), "."))
	return exts[ext]
}

func matchesGlobs(p string, include, exclude []string) bool {
	for _, g := range exclude {
		if ok, _ := doublestar.Match(g, p); ok {
			return false
		}
	}
	if len(include) == 0 {
		return true
	}
	for _, g := range include {
		if ok, _ := doublestar.Match(g, p); ok {
			return true
		}
	}
	return false
}
