package symbolcontext

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type defPattern struct {
	exts []string
	kind string
	re   *regexp.Regexp
}

var sourceExts = map[string]bool{
	".astro": true, ".c": true, ".cc": true, ".cpp": true, ".cs": true, ".go": true, ".h": true, ".hh": true, ".hpp": true,
	".java": true, ".js": true, ".jsx": true, ".php": true, ".py": true, ".rs": true, ".sql": true, ".svelte": true, ".ts": true,
	".tsx": true, ".vue": true,
}

var scriptExts = []string{".astro", ".js", ".jsx", ".svelte", ".ts", ".tsx", ".vue"}

var defPatterns = []defPattern{
	{[]string{".go"}, "function", regexp.MustCompile(`^\s*func\s+(?:\([^)]+\)\s*)?([A-Za-z_]\w*)\s*\(`)},
	{[]string{".go"}, "type", regexp.MustCompile(`^\s*type\s+([A-Za-z_]\w*)\s+(?:struct|interface|func|\w+)`)},
	{[]string{".go"}, "value", regexp.MustCompile(`^\s*(?:var|const)\s+([A-Za-z_]\w*)\b`)},
	{[]string{".py"}, "function", regexp.MustCompile(`^\s*(?:async\s+)?def\s+([A-Za-z_]\w*)\s*\(`)},
	{[]string{".py"}, "class", regexp.MustCompile(`^\s*class\s+([A-Za-z_]\w*)\b`)},
	{scriptExts, "function", regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s+([A-Za-z_$][\w$]*)\s*\(`)},
	{scriptExts, "function", regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*(?:async\s*)?(?:\([^)]*\)|[A-Za-z_$][\w$]*)\s*=>`)},
	{scriptExts, "type", regexp.MustCompile(`^\s*(?:export\s+)?(?:class|interface|type|enum)\s+([A-Za-z_$][\w$]*)\b`)},
	{scriptExts, "component", regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:const|let)\s+([A-Z][A-Za-z0-9_$]*[a-z][A-Za-z0-9_$]*)\s*=`)},
	{scriptExts, "value", regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=`)},
	{[]string{".php"}, "type", regexp.MustCompile(`^\s*(?:final\s+|abstract\s+)?(?:class|interface|trait|enum)\s+([A-Za-z_]\w*)\b`)},
	{[]string{".php"}, "function", regexp.MustCompile(`^\s*(?:(?:public|protected|private|static|final|abstract)\s+)*function\s+([A-Za-z_]\w*)\s*\(`)},
	{[]string{".sql"}, "sql", regexp.MustCompile(`(?i)^\s*(?:create\s+(?:or\s+replace\s+)?)?(?:table|view|materialized\s+view|function|procedure)\s+(?:if\s+not\s+exists\s+)?["` + "`" + `\[]?([\w.]+)`)},
	{[]string{".rs"}, "function", regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?fn\s+([A-Za-z_]\w*)\s*\(`)},
	{[]string{".rs"}, "type", regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?(?:struct|enum|trait|mod)\s+([A-Za-z_]\w*)\b`)},
	{[]string{".java", ".cs"}, "type", regexp.MustCompile(`^\s*(?:(?:public|private|protected|internal|static|final|sealed|abstract|async|partial|readonly)\s+)*(?:class|interface|enum|record|struct)\s+([A-Za-z_]\w*)\b`)},
	{[]string{".java", ".cs"}, "function", regexp.MustCompile(`^\s*(?:(?:public|private|protected|internal|static|final|sealed|abstract|async|partial|virtual|override|readonly)\s+)+[\w<>\[\],?]+\s+([A-Za-z_]\w*)\s*\(`)},
	{[]string{".c", ".h", ".cc", ".cpp", ".hh", ".hpp"}, "function", regexp.MustCompile(`^\s*(?:static\s+|inline\s+|extern\s+)?[\w:*&<>\s]+\s+([A-Za-z_]\w*)\s*\([^;]*\)\s*\{?\s*$`)},
}

var callRe = regexp.MustCompile(`\b([A-Za-z_$][\w$]*)\s*\(`)

var callSkip = map[string]bool{
	"and": true, "catch": true, "class": true, "def": true, "elif": true, "else": true, "for": true, "func": true, "function": true,
	"if": true, "interface": true, "new": true, "return": true, "sizeof": true, "switch": true, "type": true, "while": true,
}

func supportedSource(path string) bool {
	return sourceExts[strings.ToLower(filepath.Ext(path))]
}

func extractDefinitions(file, text string) []Definition {
	ext := strings.ToLower(filepath.Ext(file))
	lines := strings.Split(text, "\n")
	var defs []Definition
	if isSingleFileComponent(ext) {
		name := strings.TrimSuffix(filepath.Base(file), ext)
		defs = append(defs, Definition{Name: name, Kind: "component", File: file, Line: 1, Signature: "component " + name})
	}
	patterns := patternsForExt(ext)
	for i, line := range lines {
		for _, p := range patterns {
			m := p.re.FindStringSubmatch(line)
			if len(m) < 2 {
				continue
			}
			defs = append(defs, Definition{
				Name:      strings.TrimSpace(m[1]),
				Kind:      p.kind,
				File:      file,
				Line:      i + 1,
				Signature: strings.TrimSpace(line),
			})
			break
		}
	}
	if ext == ".sql" {
		model := strings.TrimSuffix(filepath.Base(file), ext)
		if model != "" {
			defs = append(defs, Definition{Name: model, Kind: "dbt-model", File: file, Line: 1, Signature: "dbt model " + model})
		}
	}
	sortDefinitions(defs)
	return defs
}

func isSingleFileComponent(ext string) bool {
	return ext == ".astro" || ext == ".svelte" || ext == ".vue"
}

func extractCalls(file string, lines []string, current string, limit int) []string {
	seen := map[string]bool{}
	var out []string
	ext := strings.ToLower(filepath.Ext(file))
	patterns := patternsForExt(ext)
	currentKey := strings.ToLower(current)
	for _, line := range lines {
		if isDefinitionLine(patterns, line) {
			continue
		}
		for _, m := range callRe.FindAllStringSubmatch(line, -1) {
			name := m[1]
			key := strings.ToLower(name)
			if key == currentKey || callSkip[key] || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, name)
			if limit > 0 && len(out) >= limit {
				sort.Strings(out)
				return out
			}
		}
	}
	sort.Strings(out)
	return out
}

func isDefinitionLine(patterns []defPattern, line string) bool {
	for _, p := range patterns {
		if p.re.MatchString(line) {
			return true
		}
	}
	return false
}

func patternsForExt(ext string) []defPattern {
	var out []defPattern
	for _, p := range defPatterns {
		if hasExt(p.exts, ext) {
			out = append(out, p)
		}
	}
	return out
}

func hasExt(exts []string, ext string) bool {
	for _, e := range exts {
		if e == ext {
			return true
		}
	}
	return false
}
