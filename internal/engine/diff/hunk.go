package diff

import (
	"regexp"
	"strconv"
	"strings"
)

// HunkLineType is the kind of a line within a diff hunk.
type HunkLineType int

const (
	HunkContext HunkLineType = iota // ' ' prefix: unchanged context line
	HunkAdded                       // '+' prefix: added line
	HunkDeleted                     // '-' prefix: removed line
)

// HunkLine is a single line within a hunk, content stripped of its marker.
type HunkLine struct {
	Type    HunkLineType
	Content string
}

// Hunk is one @@ ... @@ block of a unified diff.
type Hunk struct {
	OldStart int
	OldCount int
	NewStart int
	NewCount int
	Lines    []HunkLine
}

var hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// ParseHunks splits a single file's unified diff text into hunks. Lines before
// the first @@ header (diff --git, ---, +++) are ignored; parsing stops at the
// next file's "diff --git" header and skips "\ No newline" markers.
func ParseHunks(rawDiffText string) []Hunk {
	lines := strings.Split(rawDiffText, "\n")
	var hunks []Hunk
	var current *Hunk

	flush := func() {
		if current != nil {
			hunks = append(hunks, *current)
			current = nil
		}
	}

	for _, line := range lines {
		if m := hunkHeaderRe.FindStringSubmatch(line); m != nil {
			flush()
			oldStart, _ := strconv.Atoi(m[1])
			oldCount := 1
			if m[2] != "" {
				oldCount, _ = strconv.Atoi(m[2])
			}
			newStart, _ := strconv.Atoi(m[3])
			newCount := 1
			if m[4] != "" {
				newCount, _ = strconv.Atoi(m[4])
			}
			current = &Hunk{OldStart: oldStart, OldCount: oldCount, NewStart: newStart, NewCount: newCount}
			continue
		}

		if current == nil {
			continue
		}

		if strings.HasPrefix(line, "\\ No newline at end of file") {
			continue
		}
		if strings.HasPrefix(line, "diff --git ") {
			break
		}

		switch {
		case strings.HasPrefix(line, "+"):
			current.Lines = append(current.Lines, HunkLine{Type: HunkAdded, Content: line[1:]})
		case strings.HasPrefix(line, "-"):
			current.Lines = append(current.Lines, HunkLine{Type: HunkDeleted, Content: line[1:]})
		default:
			content := line
			if len(content) > 0 && content[0] == ' ' {
				content = content[1:]
			}
			current.Lines = append(current.Lines, HunkLine{Type: HunkContext, Content: content})
		}
	}

	flush()
	return hunks
}
