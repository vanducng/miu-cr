package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/store"
)

// parseSince accepts a relative span (Nd / Nh) or an absolute YYYY-MM-DD date,
// returning the resulting cutoff time. A relative span is subtracted from now.
func parseSince(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if d, ok := relativeSpan(s); ok {
		return time.Now().Add(-d), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	return time.Time{}, &CLIError{Code: "history.bad_time", Message: fmt.Sprintf("invalid time %q", s), Hint: "use 7d, 24h, or 2026-06-01", Exit: 2}
}

// maxSpanDays caps a relative span (~100 years) so the days/hours→nanoseconds
// conversion can't overflow time.Duration's int64.
const maxSpanDays = 36500

// relativeSpan parses "Nd" (days) or "Nh" (hours) into a Duration.
func relativeSpan(s string) (time.Duration, bool) {
	if len(s) < 2 {
		return 0, false
	}
	unit := s[len(s)-1]
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n < 0 {
		return 0, false
	}
	switch unit {
	case 'd':
		if n > maxSpanDays {
			return 0, false
		}
		return time.Duration(n) * 24 * time.Hour, true
	case 'h':
		if n > maxSpanDays*24 {
			return 0, false
		}
		return time.Duration(n) * time.Hour, true
	default:
		return 0, false
	}
}

// target labels a review by its PR ref (owner/repo#N) or local repo dir.
func target(owner, repo string, number int, repoDir string) string {
	if owner != "" && repo != "" {
		if number > 0 {
			return fmt.Sprintf("%s/%s#%d", owner, repo, number)
		}
		return fmt.Sprintf("%s/%s", owner, repo)
	}
	if repoDir != "" {
		return repoDir
	}
	return repo
}

func summaryRow(r store.ReviewSummary) map[string]any {
	return map[string]any{
		"id":           r.ID,
		"created_at":   r.CreatedAt.Format(time.RFC3339),
		"target":       target(r.Owner, r.Repo, r.Number, r.RepoDir),
		"mode":         r.Mode,
		"findings":     r.FindingsCount,
		"max_severity": r.MaxSeverity,
		"status":       r.Status,
	}
}

func maxSeverityOf(r store.ReviewRecord) string { return engine.MaxSeverity(r.Findings) }

func engineToCLIFindings(in []engine.Finding) []ReviewFinding {
	out := make([]ReviewFinding, 0, len(in))
	for _, f := range in {
		out = append(out, ReviewFinding{
			File: f.File, Line: f.Line, EndLine: f.EndLine, Severity: f.Severity,
			Category: f.Category, Rationale: f.Rationale, SuggestedPatch: f.SuggestedPatch, QuotedCode: f.QuotedCode,
		})
	}
	return out
}

func recordData(r store.ReviewRecord) map[string]any {
	return map[string]any{
		"id":           r.ID,
		"created_at":   r.CreatedAt.Format(time.RFC3339),
		"target":       target(r.Owner, r.Repo, r.Number, r.RepoDir),
		"mode":         r.Mode,
		"head_sha":     r.HeadSHA,
		"provider":     r.Provider,
		"model":        r.Model,
		"status":       r.Status,
		"max_severity": maxSeverityOf(r),
		"findings":     r.Findings,
		"stats":        r.Stats,
		"transcript":   transcriptJSON(r.Transcript),
		"raw_prompt":   r.RawPrompt,
		"raw_response": r.RawResponse,
	}
}

// transcriptJSON returns the stored transcript as decoded JSON (nil when empty)
// so the envelope nests it as structured data rather than an opaque string.
func transcriptJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return string(b)
	}
	return v
}

func renderHistoryList(w io.Writer, rows []store.ReviewSummary) error {
	ew := &errWriter{w: w}
	if len(rows) == 0 {
		ew.printf("No saved reviews.\n")
		return ew.err
	}
	ew.printf("%-22s  %-20s  %-28s  %-7s  %-4s  %-8s  %s\n", "ID", "CREATED", "TARGET", "MODE", "FIND", "MAXSEV", "STATUS")
	for _, r := range rows {
		ew.printf("%-22s  %-20s  %-28s  %-7s  %-4d  %-8s  %s\n",
			r.ID, r.CreatedAt.Format("2006-01-02 15:04:05"), target(r.Owner, r.Repo, r.Number, r.RepoDir),
			r.Mode, r.FindingsCount, r.MaxSeverity, r.Status)
	}
	return ew.err
}

func renderHistoryRecord(w io.Writer, r store.ReviewRecord, raw bool) error {
	ew := &errWriter{w: w}
	ew.printf("id:        %s\n", r.ID)
	ew.printf("created:   %s\n", r.CreatedAt.Format(time.RFC3339))
	ew.printf("target:    %s\n", target(r.Owner, r.Repo, r.Number, r.RepoDir))
	ew.printf("mode:      %s\n", r.Mode)
	ew.printf("provider:  %s  model: %s\n", r.Provider, r.Model)
	ew.printf("status:    %s  max_severity: %s\n\n", r.Status, maxSeverityOf(r))
	if err := renderReviewTable(w, ReviewOutcome{Findings: engineToCLIFindings(r.Findings), Stats: r.Stats}); err != nil {
		return err
	}
	if raw {
		ew.printf("\n--- raw prompt ---\n%s\n", r.RawPrompt)
		ew.printf("\n--- raw response ---\n%s\n", r.RawResponse)
	}
	return ew.err
}
