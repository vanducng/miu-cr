package github

import (
	stdctx "context"
	"fmt"
	"strings"
	"time"

	gh "github.com/google/go-github/v84/github"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/diff"
)

// checkRunName is the CheckRun name; it doubles as the required-check id a repo
// can mark required in branch protection. Stable so re-runs update one check.
const checkRunName = "miu-cr"

// maxAnnotationsPerBatch is GitHub's hard cap on annotations per Checks call; we
// create the run with the first batch and update it with each further batch.
const maxAnnotationsPerBatch = 50

// maxCheckAnnotations caps total annotations so a pathological run can't spew
// thousands of update calls; the rest are summarized in the output text.
const maxCheckAnnotations = 200

// PostChecksResult reports what PostChecks did: the created CheckRun id, the
// number of annotations posted, any over the cap, and the resolved conclusion.
type PostChecksResult struct {
	CheckRunID  int64
	Annotations int
	Omitted     int
	Conclusion  string
	// Posted carries (fingerprint, path) for the findings actually turned into
	// annotations this run, so the caller can feed semantic recall regardless of
	// reporter (mirrors PostReviewResult.PostedFindings on the inline path).
	Posted []PostedFinding
}

// PostChecks creates a GitHub CheckRun at the head SHA carrying annotations built
// from the diff-eligible findings (reusing the same inline filter the review path
// uses), batching ≤50 annotations per Checks call. The conclusion maps from the
// gate: gateClean→success, gate-hit→failure. Survives force-push and works on
// fork PRs (no comment-write scope needed) and can be marked a required check.
func PostChecks(ctx stdctx.Context, client Client, info *PRInfo, findings []engine.Finding, diffs []diff.Diff, stats map[string]any, gateClean bool, mode FilterMode) (PostChecksResult, error) {
	eligible := inlineEligible(findings, diffs, mode)

	// Checks API requires start_line/end_line >= 1 and has no file-level annotation;
	// an unanchored finding (Line<=0) would 422 the whole CheckRun. Drop it from the
	// annotation list here (defense-in-depth — the diff filter already excludes it);
	// it still counts in the summary histogram below, which keys on all findings.
	anchored := make([]engine.Finding, 0, len(eligible))
	for _, f := range eligible {
		if f.Line <= 0 {
			continue
		}
		anchored = append(anchored, f)
	}

	anns := make([]*gh.CheckRunAnnotation, 0, len(anchored))
	posted := make([]PostedFinding, 0, len(anchored))
	for _, f := range anchored {
		if len(anns) >= maxCheckAnnotations {
			break
		}
		anns = append(anns, annotationFor(f))
		posted = append(posted, PostedFinding{Fingerprint: fingerprint(f), Path: f.File})
	}
	omitted := len(anchored) - len(anns)

	conclusion := "failure"
	if gateClean {
		conclusion = "success"
	}

	title := fmt.Sprintf("%d finding(s)", len(findings))
	summary := checkSummary(findings, stats, omitted)

	first := anns
	if len(first) > maxAnnotationsPerBatch {
		first = anns[:maxAnnotationsPerBatch]
	}
	now := gh.Timestamp{Time: time.Now()}
	run, err := client.CreateCheckRun(ctx, info.Owner, info.Repo, gh.CreateCheckRunOptions{
		Name:        checkRunName,
		HeadSHA:     info.HeadSHA,
		Status:      gh.Ptr("completed"),
		Conclusion:  gh.Ptr(conclusion),
		CompletedAt: &now,
		Output: &gh.CheckRunOutput{
			Title:       gh.Ptr(title),
			Summary:     gh.Ptr(summary),
			Annotations: first,
		},
	})
	if err != nil {
		return PostChecksResult{}, mapWriteError("github.create_check_run_failed", "creating check run", err)
	}

	for start := maxAnnotationsPerBatch; start < len(anns); start += maxAnnotationsPerBatch {
		end := min(start+maxAnnotationsPerBatch, len(anns))
		if _, uerr := client.UpdateCheckRun(ctx, info.Owner, info.Repo, run.GetID(), gh.UpdateCheckRunOptions{
			Name: checkRunName,
			Output: &gh.CheckRunOutput{
				Title:       gh.Ptr(title),
				Summary:     gh.Ptr(summary),
				Annotations: anns[start:end],
			},
		}); uerr != nil {
			return PostChecksResult{}, mapWriteError("github.update_check_run_failed", "updating check run annotations", uerr)
		}
	}

	return PostChecksResult{CheckRunID: run.GetID(), Annotations: len(anns), Omitted: omitted, Conclusion: conclusion, Posted: posted}, nil
}

// annotationFor maps one finding to a CheckRunAnnotation: repo-relative path,
// start/end line from the anchor, annotation_level from severity, message from
// rationale (finding text only — never a token).
func annotationFor(f engine.Finding) *gh.CheckRunAnnotation {
	start := f.Line
	end := f.EndLine
	if end < start {
		end = start
	}
	msg := strings.TrimSpace(f.Rationale)
	if msg == "" {
		msg = "miu-cr finding"
	}
	a := &gh.CheckRunAnnotation{
		Path:            gh.Ptr(f.File),
		StartLine:       gh.Ptr(start),
		EndLine:         gh.Ptr(end),
		AnnotationLevel: gh.Ptr(annotationLevel(f.Severity)),
		Message:         gh.Ptr(msg),
	}
	if f.Category != "" {
		a.Title = gh.Ptr(strings.ToUpper(f.Severity) + " (" + f.Category + ")")
	}
	return a
}

// annotationLevel maps a finding severity to a Checks annotation_level:
// critical/high→failure, medium→warning, low/info/other→notice.
func annotationLevel(sev string) string {
	switch strings.ToLower(strings.TrimSpace(sev)) {
	case "critical", "high":
		return "failure"
	case "medium":
		return "warning"
	default:
		return "notice"
	}
}

// checkSummary renders the CheckRun output summary (severity histogram + run
// stats + any over-cap note), reusing the same stat helpers as the PR summary.
func checkSummary(findings []engine.Finding, stats map[string]any, omitted int) string {
	var b strings.Builder
	if len(findings) == 0 {
		b.WriteString("No findings.")
	} else {
		fmt.Fprintf(&b, "%d finding(s).", len(findings))
		counts := map[string]int{}
		for _, f := range findings {
			sev := strings.ToLower(strings.TrimSpace(f.Severity))
			if sev == "" {
				sev = "info"
			}
			counts[sev]++
		}
		for _, sev := range severityOrder {
			if n := counts[sev]; n > 0 {
				fmt.Fprintf(&b, " %s: %d", sev, n)
			}
		}
	}
	fmt.Fprintf(&b, "\n\nFiles reviewed: %s. Context: %s.", statInt(stats, "files_reviewed"), truncationLevel(stats))
	if omitted > 0 {
		fmt.Fprintf(&b, "\n\n%d annotation(s) over the %d-annotation cap were omitted.", omitted, maxCheckAnnotations)
	}
	return b.String()
}
