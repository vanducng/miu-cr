package context

import (
	stdctx "context"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/vanducng/miu-cr/internal/engine/diff"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

const (
	defaultRelatedMaxFiles      = 20
	defaultRelatedMaxFileBytes  = 12 * 1024
	defaultRelatedMaxTotalBytes = 128 * 1024
	relatedReverseScanMaxFiles  = 800
)

type RelatedOptions struct {
	HopDepth      int // required; 0 disables related context
	MaxFiles      int
	MaxFileBytes  int
	MaxTotalBytes int
}

type RelatedResult struct {
	Text      string
	Files     []string
	Truncated bool
	Hops      int
}

func BuildRelatedContext(ctx stdctx.Context, repoDir, rev string, selected []diff.Diff, runner *gitcmd.Runner, opts RelatedOptions) RelatedResult {
	opts = normalizeRelatedOptions(opts)
	if opts.HopDepth <= 0 || opts.MaxFiles <= 0 || opts.MaxFileBytes <= 0 || opts.MaxTotalBytes <= 0 {
		return RelatedResult{}
	}
	if runner == nil {
		runner = gitcmd.New()
	}
	files, err := listRevisionFiles(ctx, repoDir, rev, runner)
	if err != nil || len(files) == 0 {
		return RelatedResult{}
	}
	g := newRelatedGraph(ctx, repoDir, rev, files, runner)
	roots := changedRootSet(selected)
	if len(roots) == 0 {
		return RelatedResult{}
	}

	type node struct {
		path  string
		depth int
	}
	queue := make([]node, 0, len(roots))
	seen := make(map[string]bool, len(roots))
	for root := range roots {
		if !g.has(root) {
			continue
		}
		seen[root] = true
		queue = append(queue, node{path: root})
	}
	sort.Slice(queue, func(i, j int) bool { return queue[i].path < queue[j].path })

	depthOf := map[string]int{}
	truncated := false
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth >= opts.HopDepth {
			continue
		}
		nextDepth := cur.depth + 1
		for _, nb := range g.neighbors(ctx, cur.path) {
			if roots[nb] || seen[nb] {
				continue
			}
			if len(depthOf) >= opts.MaxFiles {
				truncated = true
				continue
			}
			seen[nb] = true
			depthOf[nb] = nextDepth
			queue = append(queue, node{path: nb, depth: nextDepth})
		}
	}
	if len(depthOf) == 0 {
		return RelatedResult{Hops: opts.HopDepth, Truncated: truncated}
	}

	related := make([]string, 0, len(depthOf))
	for path := range depthOf {
		related = append(related, path)
	}
	sort.Slice(related, func(i, j int) bool {
		di, dj := depthOf[related[i]], depthOf[related[j]]
		if di != dj {
			return di < dj
		}
		return related[i] < related[j]
	})

	var sb strings.Builder
	total := 0
	rendered := make([]string, 0, len(related))
	for _, path := range related {
		blob, err := g.read(ctx, path)
		if err != nil {
			continue
		}
		if len(blob) > opts.MaxFileBytes {
			blob = truncateRelatedUTF8Bytes(blob, opts.MaxFileBytes)
			truncated = true
		}
		header := "--- related_file: " + path + " (hop " + strconv.Itoa(depthOf[path]) + ") ---\n"
		if total+len(header)+len(blob) > opts.MaxTotalBytes {
			remaining := opts.MaxTotalBytes - total - len(header)
			if remaining <= 0 {
				truncated = true
				break
			}
			blob = truncateRelatedUTF8Bytes(blob, remaining)
			truncated = true
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(header)
		sb.Write(blob)
		total += len(header) + len(blob)
		rendered = append(rendered, path)
		if total >= opts.MaxTotalBytes {
			break
		}
	}
	return RelatedResult{Text: sb.String(), Files: rendered, Truncated: truncated, Hops: opts.HopDepth}
}

func normalizeRelatedOptions(opts RelatedOptions) RelatedOptions {
	if opts.MaxFiles == 0 {
		opts.MaxFiles = defaultRelatedMaxFiles
	}
	if opts.MaxFileBytes == 0 {
		opts.MaxFileBytes = defaultRelatedMaxFileBytes
	}
	if opts.MaxTotalBytes == 0 {
		opts.MaxTotalBytes = defaultRelatedMaxTotalBytes
	}
	return opts
}

func listRevisionFiles(ctx stdctx.Context, repoDir, rev string, runner *gitcmd.Runner) ([]string, error) {
	var out []byte
	var err error
	if rev == "" {
		out, err = runner.Output(ctx, repoDir, "ls-files", "-z")
	} else {
		out, err = runner.Output(ctx, repoDir, "ls-tree", "-rz", "--name-only", rev)
	}
	if err != nil {
		return nil, err
	}
	parts := strings.Split(string(out), "\x00")
	files := make([]string, 0, len(parts))
	for _, p := range parts {
		p = cleanRepoPath(p)
		if p != "" {
			files = append(files, p)
		}
	}
	sort.Strings(files)
	return files, nil
}

func changedRootSet(selected []diff.Diff) map[string]bool {
	roots := map[string]bool{}
	for _, d := range selected {
		if d.IsDeleted || d.IsBinary {
			continue
		}
		p := cleanRepoPath(d.NewPath)
		if p != "" {
			roots[p] = true
		}
	}
	return roots
}

type relatedGraph struct {
	repoDir   string
	rev       string
	runner    *gitcmd.Runner
	fileSet   map[string]bool
	goByDir   map[string][]string
	reverseGo map[string][]string
	module    string
	blobCache map[string][]byte
}

func newRelatedGraph(ctx stdctx.Context, repoDir, rev string, files []string, runner *gitcmd.Runner) *relatedGraph {
	g := &relatedGraph{
		repoDir:   repoDir,
		rev:       rev,
		runner:    runner,
		fileSet:   map[string]bool{},
		goByDir:   map[string][]string{},
		reverseGo: map[string][]string{},
		blobCache: map[string][]byte{},
	}
	for _, p := range files {
		g.fileSet[p] = true
		if isGoSource(p) {
			dir := filepath.ToSlash(filepath.Dir(p))
			if dir == "." {
				dir = ""
			}
			g.goByDir[dir] = append(g.goByDir[dir], p)
		}
	}
	for dir := range g.goByDir {
		sort.Strings(g.goByDir[dir])
	}
	g.module = g.readModulePath(ctx)
	if len(g.goFiles()) <= relatedReverseScanMaxFiles {
		g.buildReverseGo(ctx)
	}
	return g
}

func (g *relatedGraph) has(path string) bool { return g.fileSet[path] }

func (g *relatedGraph) read(ctx stdctx.Context, path string) ([]byte, error) {
	if b, ok := g.blobCache[path]; ok {
		return b, nil
	}
	blob, err := g.runner.ShowBlob(ctx, g.repoDir, g.rev, path)
	if err != nil {
		return nil, err
	}
	g.blobCache[path] = blob
	return blob, nil
}

func (g *relatedGraph) neighbors(ctx stdctx.Context, path string) []string {
	out := map[string]bool{}
	if isGoSource(path) {
		g.addGoNeighbors(ctx, path, out)
	}
	g.addRelativeImportNeighbors(ctx, path, out)
	delete(out, path)
	neighbors := make([]string, 0, len(out))
	for p := range out {
		if g.fileSet[p] {
			neighbors = append(neighbors, p)
		}
	}
	sort.Strings(neighbors)
	return neighbors
}

func (g *relatedGraph) addGoNeighbors(ctx stdctx.Context, path string, out map[string]bool) {
	dir := filepath.ToSlash(filepath.Dir(path))
	if dir == "." {
		dir = ""
	}
	for _, p := range g.goByDir[dir] {
		if !strings.HasSuffix(p, "_test.go") {
			out[p] = true
		}
	}
	for _, imp := range g.goImports(ctx, path) {
		if impDir, ok := g.importDir(imp); ok {
			for _, p := range g.goByDir[impDir] {
				if !strings.HasSuffix(p, "_test.go") {
					out[p] = true
				}
			}
		}
	}
	for _, p := range g.reverseGo[dir] {
		if !strings.HasSuffix(p, "_test.go") {
			out[p] = true
		}
	}
}

func (g *relatedGraph) buildReverseGo(ctx stdctx.Context) {
	for _, path := range g.goFiles() {
		for _, imp := range g.goImports(ctx, path) {
			if dir, ok := g.importDir(imp); ok {
				g.reverseGo[dir] = append(g.reverseGo[dir], path)
			}
		}
	}
	for dir := range g.reverseGo {
		sort.Strings(g.reverseGo[dir])
	}
}

func (g *relatedGraph) goFiles() []string {
	var files []string
	for _, fs := range g.goByDir {
		files = append(files, fs...)
	}
	sort.Strings(files)
	return files
}

func (g *relatedGraph) goImports(ctx stdctx.Context, path string) []string {
	blob, err := g.read(ctx, path)
	if err != nil {
		return nil
	}
	f, err := parser.ParseFile(token.NewFileSet(), path, blob, parser.ImportsOnly)
	if err != nil {
		return nil
	}
	imports := make([]string, 0, len(f.Imports))
	for _, spec := range f.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err == nil {
			imports = append(imports, p)
		}
	}
	return imports
}

func (g *relatedGraph) importDir(imp string) (string, bool) {
	if g.module == "" {
		return "", false
	}
	if imp == g.module {
		return "", true
	}
	prefix := g.module + "/"
	if !strings.HasPrefix(imp, prefix) {
		return "", false
	}
	dir := strings.TrimPrefix(imp, prefix)
	if _, ok := g.goByDir[dir]; !ok {
		return "", false
	}
	return dir, true
}

func (g *relatedGraph) readModulePath(ctx stdctx.Context) string {
	blob, err := g.read(ctx, "go.mod")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(blob), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

var relativeImportRe = regexp.MustCompile(`(?:from\s+|import\s+|require\(\s*)["'](\.{1,2}/[^"']+)["']`)

var pythonRelativeImportRe = regexp.MustCompile(`(?m)^\s*from\s+(\.{1,2})([A-Za-z0-9_\.]*)\s+import\s+([A-Za-z0-9_, \t]+)`)

func (g *relatedGraph) addRelativeImportNeighbors(ctx stdctx.Context, path string, out map[string]bool) {
	if !isScriptSource(path) {
		return
	}
	blob, err := g.read(ctx, path)
	if err != nil {
		return
	}
	dir := filepath.ToSlash(filepath.Dir(path))
	if dir == "." {
		dir = ""
	}
	matches := relativeImportRe.FindAllStringSubmatch(string(blob), -1)
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		if resolved := g.resolveRelativeImport(dir, filepath.ToSlash(m[1]), path); resolved != "" {
			out[resolved] = true
		}
	}
	if strings.EqualFold(filepath.Ext(path), ".py") {
		for _, resolved := range g.pythonRelativeImports(dir, string(blob)) {
			out[resolved] = true
		}
	}
}

func (g *relatedGraph) resolveRelativeImport(dir, rel, source string) string {
	base := cleanRepoPath(filepath.ToSlash(filepath.Join(dir, rel)))
	if base == "" {
		return ""
	}
	ext := filepath.Ext(source)
	candidates := []string{base}
	if ext != "" {
		candidates = append(candidates, base+ext)
	}
	candidates = append(candidates,
		base+".ts", base+".tsx", base+".js", base+".jsx", base+".mjs", base+".cjs", base+".py",
		base+"/index.ts", base+"/index.tsx", base+"/index.js", base+"/index.jsx", base+"/index.py",
	)
	for _, c := range candidates {
		c = cleanRepoPath(c)
		if g.fileSet[c] {
			return c
		}
	}
	return ""
}

func (g *relatedGraph) pythonRelativeImports(dir, body string) []string {
	matches := pythonRelativeImportRe.FindAllStringSubmatch(body, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) < 4 {
			continue
		}
		up := len(m[1])
		baseDir := dir
		for i := 1; i < up; i++ {
			baseDir = filepath.ToSlash(filepath.Dir(baseDir))
			if baseDir == "." {
				baseDir = ""
			}
		}
		module := strings.Trim(m[2], ".")
		rel := strings.ReplaceAll(module, ".", "/")
		if rel != "" {
			if resolved := g.resolveRelativeImport(baseDir, "./"+rel, "x.py"); resolved != "" {
				out = append(out, resolved)
			}
		}
		for _, name := range strings.Split(m[3], ",") {
			name = strings.TrimSpace(strings.Split(name, " as ")[0])
			if name == "" || name == "*" {
				continue
			}
			candidate := name
			if rel != "" {
				candidate = rel + "/" + name
			}
			if resolved := g.resolveRelativeImport(baseDir, "./"+candidate, "x.py"); resolved != "" {
				out = append(out, resolved)
			}
		}
	}
	sort.Strings(out)
	return out
}

func truncateRelatedUTF8Bytes(blob []byte, n int) []byte {
	if len(blob) <= n {
		return blob
	}
	cut := n
	for cut > 0 && cut < len(blob) && !utf8.RuneStart(blob[cut]) {
		cut--
	}
	return blob[:cut]
}

func isGoSource(path string) bool {
	return strings.HasSuffix(path, ".go")
}

func isScriptSource(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".py":
		return true
	default:
		return false
	}
}

func cleanRepoPath(p string) string {
	p = strings.TrimSpace(filepath.ToSlash(p))
	p = strings.TrimPrefix(p, "./")
	if p == "." || p == "/" || strings.HasPrefix(p, "../") {
		return ""
	}
	return p
}
