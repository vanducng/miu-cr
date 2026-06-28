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

type definition struct {
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
		if args.Symbol == "" {
			return "", fmt.Errorf("references requires symbol")
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

func (s *scanner) definitions(ctx context.Context, symbol, file string) ([]definition, error) {
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
	var defs []definition
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

func (s *scanner) definitionsSequential(ctx context.Context, symbol string, paths []string) ([]definition, error) {
	var defs []definition
	for _, path := range paths {
		if !supportedSource(path) {
			continue
		}
		text, err := s.readText(ctx, path)
		if err != nil {
			return nil, err
		}
		for _, d := range extractDefinitions(path, text) {
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
	text, err := s.readText(ctx, file)
	if err != nil {
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
		text, err := s.readText(ctx, file)
		if err != nil {
			return "", err
		}
		return formatDBTFileDependencies(file, text), nil
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
	title := "Dependencies"
	if symbol != "" {
		title += " for " + symbol
	}
	if len(matches) == 0 {
		return title + ":\n" + NoDependenciesFoundMarker, nil
	}
	return title + ":\n" + strings.Join(matches, "\n"), nil
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

func formatDefinitions(title string, defs []definition, limit int) string {
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

func sortDefinitions(defs []definition) {
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
	refs := extractDBTRefs(text)
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
