package symbolcontext

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/vanducng/miu-cr/internal/config"
	enginectx "github.com/vanducng/miu-cr/internal/engine/context"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
)

const (
	defaultLimit              = 12
	maxLimit                  = 50
	maxScanFileSize           = 1 << 20
	callWindow                = 220
	NoSymbolsDetectedMarker   = "(no symbols detected)"
	NoDependenciesFoundMarker = "(no dependencies found)"
)

type Definition struct {
	Name      string
	Kind      string
	File      string
	Line      int
	Signature string
}

type scanner struct {
	cfg    config.SymbolContext
	tc     Context
	limit  int
	files  []string
	loaded bool
}

func scan(ctx context.Context, cfg config.SymbolContext, tc Context, args Args) (string, error) {
	if tc.Runner == nil {
		tc.Runner = gitcmd.New()
	}
	s := &scanner{cfg: cfg, tc: tc, limit: normalizeLimit(args.Limit)}
	args.Symbol = strings.TrimSpace(args.Symbol)
	// Models routinely pass "path.go:42" in file; fold the suffix into line.
	// Caveat: a real path literally ending in :digits loses its suffix here.
	if rawFile, line := splitTrailingLine(args.File); line > 0 {
		args.File = rawFile
		if args.Line == 0 {
			args.Line = line
		}
	}
	file, err := cleanFilePath(args.File)
	if err != nil {
		return "", err
	}
	args.File = file
	switch strings.ToLower(args.Relation) {
	case "definition":
		if args.Symbol == "" {
			return "", fmt.Errorf("definition requires symbol")
		}
		defs, err := s.definitions(ctx, args.Symbol, args.File)
		if err != nil {
			return "", err
		}
		return formatDefinitions("Definitions for "+args.Symbol, defs, s.limit), nil
	case "implementations":
		if args.Symbol == "" {
			return "", fmt.Errorf("implementations requires symbol")
		}
		defs, err := s.definitions(ctx, args.Symbol, args.File)
		if err != nil {
			return "", err
		}
		return formatDefinitions("Implementation candidates for "+args.Symbol, defs, s.limit), nil
	case "references":
		if args.Symbol == "" && args.File != "" && args.Line > 0 {
			args.Symbol = s.symbolAtLine(ctx, args.File, args.Line)
		}
		if args.Symbol == "" {
			return "", fmt.Errorf("references requires symbol (or file + line to resolve the enclosing one)")
		}
		return s.grep(ctx, "References for "+args.Symbol, args.Symbol, args.File)
	case "incoming_calls":
		if args.Symbol == "" {
			return "", fmt.Errorf("incoming_calls requires symbol")
		}
		return s.grep(ctx, "Incoming calls for "+args.Symbol, args.Symbol+"(", args.File)
	case "outgoing_calls":
		if args.Symbol == "" {
			return "", fmt.Errorf("outgoing_calls requires symbol")
		}
		return s.outgoingCalls(ctx, args.Symbol, args.File)
	case "document_symbols":
		if args.File == "" {
			return "", fmt.Errorf("document_symbols requires file")
		}
		return s.documentSymbols(ctx, args.File)
	case "dependencies":
		return s.dependencies(ctx, args.Symbol, args.File)
	default:
		return "", fmt.Errorf("unsupported relation %q", args.Relation)
	}
}

func (s *scanner) definitions(ctx context.Context, symbol, file string) ([]Definition, error) {
	if file == "" && s.tc.Index != nil {
		if defs, ok := s.indexDefinitions(ctx, symbol); ok {
			return defs, nil
		}
	}
	paths, err := s.paths(ctx, file)
	if err != nil {
		return nil, err
	}
	if file != "" {
		return s.definitionsSequential(ctx, symbol, paths)
	}
	var scanPaths []string
	for _, path := range paths {
		if supportedSource(path) {
			scanPaths = append(scanPaths, path)
		}
	}
	var defs []Definition
	batchSize := s.scanBatchSize()
	for start := 0; start < len(scanPaths) && len(defs) < s.limit; start += batchSize {
		end := start + batchSize
		if end > len(scanPaths) {
			end = len(scanPaths)
		}
		for _, res := range s.readMany(ctx, scanPaths[start:end]) {
			if res.err != nil {
				continue
			}
			for _, d := range extractDefinitions(res.path, res.text) {
				if symbolMatches(d.Name, symbol, res.path) {
					defs = append(defs, d)
				}
			}
			if len(defs) >= s.limit {
				break
			}
		}
	}
	sortDefinitions(defs)
	return defs, nil
}

func (s *scanner) definitionsSequential(ctx context.Context, symbol string, paths []string) ([]Definition, error) {
	var defs []Definition
	for _, path := range paths {
		if !supportedSource(path) {
			continue
		}
		fileDefs, ok := s.indexFileDefinitions(ctx, path)
		if !ok {
			text, err := s.readText(ctx, path)
			if err != nil {
				return nil, err
			}
			fileDefs = extractDefinitions(path, text)
		}
		for _, d := range fileDefs {
			if symbolMatches(d.Name, symbol, path) {
				defs = append(defs, d)
			}
		}
		if len(defs) >= s.limit {
			break
		}
	}
	sortDefinitions(defs)
	return defs, nil
}

// indexDefinitions serves a repo-wide by-name lookup from the shared index;
// ok=false (index missing or failed to build) means scan per-call instead.
// Bounded by s.limit so both serving paths return the same shape.
func (s *scanner) indexDefinitions(ctx context.Context, symbol string) ([]Definition, bool) {
	ix := s.tc.Index
	if ix == nil || !ix.ensure(ctx) {
		return nil, false
	}
	defs := ix.Lookup(ctx, symbol)
	if len(defs) > s.limit {
		defs = defs[:s.limit]
	}
	return defs, true
}

// indexFileDefinitions serves one file's definitions from the shared index;
// ok=false means the path was not indexed (or no index) — read it per-call.
func (s *scanner) indexFileDefinitions(ctx context.Context, path string) ([]Definition, bool) {
	ix := s.tc.Index
	if ix == nil {
		return nil, false
	}
	return ix.FileDefinitions(ctx, path)
}

func (s *scanner) grep(ctx context.Context, title, pattern, file string) (string, error) {
	out, err := enginectx.Grep(ctx, s.tc.RepoDir, s.tc.Rev, pattern, s.tc.Runner, file)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return title + ":\n(no matches)", nil
	}
	return title + ":\n" + out, nil
}

func (s *scanner) outgoingCalls(ctx context.Context, symbol, file string) (string, error) {
	defs, err := s.definitions(ctx, symbol, file)
	if err != nil {
		return "", err
	}
	if len(defs) == 0 {
		return "Outgoing calls for " + symbol + ":\n(no definition found)", nil
	}
	d := defs[0]
	text, err := s.readText(ctx, d.File)
	if err != nil {
		return "", err
	}
	lines := strings.Split(text, "\n")
	start := d.Line - 1
	if start < 0 {
		start = 0
	}
	end := start + callWindow
	if end > len(lines) {
		end = len(lines)
	}
	calls := extractCalls(d.File, lines[start:end], symbol, s.limit)
	var sb strings.Builder
	fmt.Fprintf(&sb, "Outgoing calls for %s at %s:%d:\n", symbol, d.File, d.Line)
	if len(calls) == 0 {
		sb.WriteString("(no obvious call expressions found)")
		return sb.String(), nil
	}
	for _, c := range calls {
		fmt.Fprintf(&sb, "- %s\n", c)
	}
	return sb.String(), nil
}

func (s *scanner) documentSymbols(ctx context.Context, file string) (string, error) {
	// Index-served path: a missing entry (directory, unindexed, or read-failed
	// path) falls through to the per-call read below, which keeps the directory
	// listing and error behavior identical.
	if defs, ok := s.indexFileDefinitions(ctx, file); ok {
		if len(defs) == 0 {
			return "Document symbols for " + file + ":\n" + NoSymbolsDetectedMarker, nil
		}
		return formatDefinitions("Document symbols for "+file, defs, s.limit), nil
	}
	text, err := s.readText(ctx, file)
	// A directory path used to waste the model's turn (raw git exit-128 when
	// staged, a useless "tree" blob at a rev); answer with the listing the next
	// call needs. git show renders a directory as "tree <rev>:<path>\n...".
	if err != nil || strings.HasPrefix(text, "tree ") {
		if listing := s.directoryListing(ctx, file); len(listing) > 0 {
			return "Document symbols for " + file + ":\n(path is a directory; pass one of its files)\n" + strings.Join(listing, "\n"), nil
		}
		if err == nil {
			return "", fmt.Errorf("%s is a directory with no scannable files; pass a file path", file)
		}
		return "", err
	}
	defs := extractDefinitions(file, text)
	if len(defs) == 0 {
		return "Document symbols for " + file + ":\n" + NoSymbolsDetectedMarker, nil
	}
	return formatDefinitions("Document symbols for "+file, defs, s.limit), nil
}

func (s *scanner) dependencies(ctx context.Context, symbol, file string) (string, error) {
	if file != "" {
		if ix := s.tc.Index; ix != nil {
			if refs, ok := ix.sqlRefs(ctx, file); ok {
				return formatDBTRefList(file, refs), nil
			}
		}
		text, err := s.readText(ctx, file)
		if err != nil {
			return "", err
		}
		return formatDBTFileDependencies(file, text), nil
	}
	if out, ok := s.indexDependencies(ctx, symbol); ok {
		return out, nil
	}
	paths, err := s.paths(ctx, "")
	if err != nil {
		return "", err
	}
	var sqlPaths []string
	for _, path := range paths {
		if strings.ToLower(filepath.Ext(path)) != ".sql" {
			continue
		}
		sqlPaths = append(sqlPaths, path)
	}
	var matches []string
	batchSize := s.scanBatchSize()
	for start := 0; start < len(sqlPaths) && len(matches) < s.limit; start += batchSize {
		end := start + batchSize
		if end > len(sqlPaths) {
			end = len(sqlPaths)
		}
		for _, res := range s.readMany(ctx, sqlPaths[start:end]) {
			if res.err != nil {
				continue
			}
			refs := extractDBTRefs(res.text)
			for _, ref := range refs {
				if symbol == "" || dbtRefMatches(ref, symbol) {
					matches = append(matches, fmt.Sprintf("- %s -> %s", res.path, formatDependencyRef(ref)))
					if len(matches) >= s.limit {
						break
					}
				}
			}
			if len(matches) >= s.limit {
				break
			}
		}
	}
	return formatDependencyMatches(symbol, matches), nil
}

// indexDependencies serves the repo-wide dbt-dependency scan from the shared
// index; ok=false means scan per-call instead. Read-failed .sql paths have no
// index entry, matching the per-call skip of failed reads.
func (s *scanner) indexDependencies(ctx context.Context, symbol string) (string, bool) {
	ix := s.tc.Index
	if ix == nil {
		return "", false
	}
	paths, ok := ix.filesList(ctx)
	if !ok {
		return "", false
	}
	var matches []string
	for _, path := range paths {
		if strings.ToLower(filepath.Ext(path)) != ".sql" {
			continue
		}
		refs, ok := ix.sqlRefs(ctx, path)
		if !ok {
			continue
		}
		for _, ref := range refs {
			if symbol == "" || dbtRefMatches(ref, symbol) {
				matches = append(matches, fmt.Sprintf("- %s -> %s", path, formatDependencyRef(ref)))
				if len(matches) >= s.limit {
					break
				}
			}
		}
		if len(matches) >= s.limit {
			break
		}
	}
	return formatDependencyMatches(symbol, matches), true
}

func formatDependencyMatches(symbol string, matches []string) string {
	title := "Dependencies"
	if symbol != "" {
		title += " for " + symbol
	}
	if len(matches) == 0 {
		return title + ":\n" + NoDependenciesFoundMarker
	}
	return title + ":\n" + strings.Join(matches, "\n")
}

func (s *scanner) paths(ctx context.Context, file string) ([]string, error) {
	if file != "" {
		return []string{file}, nil
	}
	if s.loaded {
		return s.files, nil
	}
	args := []string{"ls-files", "-z"}
	if s.tc.Rev != "" {
		args = []string{"ls-tree", "-rz", "--name-only", "--full-tree", s.tc.Rev}
	}
	out, err := s.tc.Runner.Output(ctx, s.tc.RepoDir, args...)
	if err != nil {
		return nil, err
	}
	maxFiles := s.cfg.MaxFilesOrDefault()
	for _, part := range strings.Split(string(out), "\x00") {
		path := strings.TrimSpace(part)
		if path == "" {
			continue
		}
		if !supportedSource(path) && strings.ToLower(filepath.Ext(path)) != ".sql" {
			continue
		}
		s.files = append(s.files, path)
		if maxFiles > 0 && len(s.files) >= maxFiles {
			break
		}
	}
	s.loaded = true
	return s.files, nil
}

func (s *scanner) readText(ctx context.Context, file string) (string, error) {
	path, err := cleanFilePath(file)
	if err != nil {
		return "", err
	}
	data, err := s.tc.Runner.ShowBlob(ctx, s.tc.RepoDir, s.tc.Rev, path)
	if err != nil {
		return "", err
	}
	if len(data) > maxScanFileSize {
		data = truncateUTF8Bytes(data, maxScanFileSize)
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("%s is not valid UTF-8 text", path)
	}
	return string(data), nil
}

type pathReadResult struct {
	path string
	text string
	err  error
}

func (s *scanner) readMany(ctx context.Context, paths []string) []pathReadResult {
	results := make([]pathReadResult, len(paths))
	for i, path := range paths {
		results[i].path = path
	}
	if len(paths) == 0 {
		return results
	}
	maxParallel := s.cfg.MaxParallelOrDefault()
	if maxParallel <= 1 || len(paths) < maxParallel*2 {
		for i, path := range paths {
			text, err := s.readText(ctx, path)
			results[i] = pathReadResult{path: path, text: text, err: err}
		}
		return results
	}
	if maxParallel > len(paths) {
		maxParallel = len(paths)
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range maxParallel {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				text, err := s.readText(ctx, paths[i])
				results[i] = pathReadResult{path: paths[i], text: text, err: err}
			}
		}()
	}
send:
	for i := range paths {
		select {
		case <-ctx.Done():
			break send
		case jobs <- i:
		}
	}
	close(jobs)
	wg.Wait()
	return results
}

func truncateUTF8Bytes(data []byte, max int) []byte {
	if max <= 0 {
		return nil
	}
	if len(data) <= max {
		return data
	}
	cut := max
	for cut > 0 && cut < len(data) && !utf8.RuneStart(data[cut]) {
		cut--
	}
	return data[:cut]
}

func (s *scanner) scanBatchSize() int {
	n := s.cfg.MaxParallelOrDefault() * 4
	if n < 8 {
		return 8
	}
	if n > 64 {
		return 64
	}
	return n
}

func normalizeLimit(n int) int {
	if n <= 0 {
		return defaultLimit
	}
	if n > maxLimit {
		return maxLimit
	}
	return n
}

func cleanFilePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("file must be repo-relative")
	}
	path = filepath.ToSlash(filepath.Clean(path))
	path = strings.TrimPrefix(path, "./")
	if path == "." {
		return "", nil
	}
	if path == ".." || strings.HasPrefix(path, "../") {
		return "", fmt.Errorf("file must stay inside the repo")
	}
	return path, nil
}

func formatDefinitions(title string, defs []Definition, limit int) string {
	if len(defs) == 0 {
		return title + ":\n" + NoSymbolsFoundMarker
	}
	if limit > 0 && len(defs) > limit {
		defs = defs[:limit]
	}
	var sb strings.Builder
	sb.WriteString(title)
	sb.WriteString(":\n")
	for _, d := range defs {
		fmt.Fprintf(&sb, "- %s:%d [%s] %s\n", d.File, d.Line, d.Kind, d.Signature)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func sortDefinitions(defs []Definition) {
	sort.Slice(defs, func(i, j int) bool {
		if defs[i].File == defs[j].File {
			return defs[i].Line < defs[j].Line
		}
		return defs[i].File < defs[j].File
	})
}

func symbolMatches(name, symbol, file string) bool {
	if name == symbol || strings.EqualFold(name, symbol) {
		return true
	}
	if strings.HasSuffix(strings.ToLower(name), "."+strings.ToLower(symbol)) {
		return true
	}
	return false
}

func formatDBTFileDependencies(file, text string) string {
	return formatDBTRefList(file, extractDBTRefs(text))
}

func formatDBTRefList(file string, refs []string) string {
	if len(refs) == 0 {
		return "Dependencies for " + file + ":\n" + NoDependenciesFoundMarker
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Dependencies for %s:\n", file)
	for _, ref := range refs {
		fmt.Fprintf(&sb, "- %s\n", formatDependencyRef(ref))
	}
	return strings.TrimRight(sb.String(), "\n")
}

func formatDependencyRef(ref string) string {
	if strings.HasPrefix(ref, "ref:") {
		return "dbt.ref: " + strings.TrimPrefix(ref, "ref:")
	}
	if strings.HasPrefix(ref, "source:") {
		return "dbt.source: " + strings.TrimPrefix(ref, "source:")
	}
	return ref
}

func dbtRefMatches(ref, symbol string) bool {
	ref = strings.ToLower(ref)
	symbol = strings.ToLower(symbol)
	return ref == "ref:"+symbol || strings.HasSuffix(ref, "."+symbol) || strings.Contains(ref, ":"+symbol)
}

var dbtRefRe = regexp.MustCompile(`\bref\s*\(\s*['"]([^'"]+)['"]\s*\)`)
var dbtSourceRe = regexp.MustCompile(`\bsource\s*\(\s*['"]([^'"]+)['"]\s*,\s*['"]([^'"]+)['"]\s*\)`)

func extractDBTRefs(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range dbtRefRe.FindAllStringSubmatch(text, -1) {
		ref := "ref:" + m[1]
		if !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	for _, m := range dbtSourceRe.FindAllStringSubmatch(text, -1) {
		ref := "source:" + m[1] + "." + m[2]
		if !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	return out
}

var trailingLineRe = regexp.MustCompile(`^(.+):(\d{1,7})$`)

// splitTrailingLine separates a "path:42"-style file argument into path + line.
func splitTrailingLine(file string) (string, int) {
	m := trailingLineRe.FindStringSubmatch(strings.TrimSpace(file))
	if m == nil {
		return file, 0
	}
	n := 0
	for _, r := range m[2] {
		n = n*10 + int(r-'0')
	}
	return m[1], n
}

// symbolAtLine names the definition enclosing (or nearest before, else first
// after) the given line, so "references file:line" works without a symbol.
func (s *scanner) symbolAtLine(ctx context.Context, file string, line int) string {
	text, err := s.readText(ctx, file)
	if err != nil {
		return ""
	}
	var before, after *Definition
	for _, d := range extractDefinitions(file, text) {
		d := d
		if d.Line <= line {
			if before == nil || d.Line > before.Line {
				before = &d
			}
		} else if after == nil || d.Line < after.Line {
			after = &d
		}
	}
	if before != nil {
		return before.Name
	}
	if after != nil {
		return after.Name
	}
	return ""
}

// directoryListing returns the tracked files under dir at the reviewed
// revision (empty when dir is not a directory), capped for prompt budget.
func (s *scanner) directoryListing(ctx context.Context, dir string) []string {
	dir = strings.TrimRight(dir, "/")
	if dir == "" {
		return nil
	}
	args := []string{"ls-files", "-z", "--", dir + "/"}
	if s.tc.Rev != "" {
		args = []string{"ls-tree", "-rz", "--name-only", "--full-tree", s.tc.Rev, "--", dir + "/"}
	}
	out, err := s.tc.Runner.Output(ctx, s.tc.RepoDir, args...)
	if err != nil {
		return nil
	}
	var files []string
	for _, part := range strings.Split(string(out), "\x00") {
		if path := strings.TrimSpace(part); path != "" {
			files = append(files, path)
			if len(files) >= 20 {
				break
			}
		}
	}
	return files
}
