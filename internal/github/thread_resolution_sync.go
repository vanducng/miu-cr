package github

import (
	stdctx "context"
	"fmt"
	"strings"
	"time"

	gh "github.com/google/go-github/v84/github"
)

type ThreadResolutionSyncResult struct {
	Action   UpsertAction
	Reason   string
	Resolved int
	Reopened int
	Entries  int
}

func SyncSummaryConversationResolved(ctx stdctx.Context, client Client, info *PRInfo, now time.Time) (ThreadResolutionSyncResult, error) {
	targetID, body, err := lowestMarkedComment(ctx, client, info)
	if err != nil {
		return ThreadResolutionSyncResult{Reason: "summary_fetch_failed"}, err
	}
	if targetID == 0 || strings.TrimSpace(body) == "" {
		return ThreadResolutionSyncResult{Reason: "no_summary"}, nil
	}
	prior := ParseLedger(body)
	if prior == nil {
		return ThreadResolutionSyncResult{Reason: "no_ledger"}, nil
	}
	resolved, err := ResolvedThreadFingerprints(ctx, client, info)
	if err != nil {
		return ThreadResolutionSyncResult{Reason: "thread_fetch_failed"}, err
	}
	next, delta := SyncLedgerConversationResolved(prior, resolved, info.HeadSHA, now)
	result := ThreadResolutionSyncResult{Reason: "unchanged", Entries: len(next), Resolved: delta.Resolved, Reopened: delta.Reopened}
	if delta.Resolved == 0 && delta.Reopened == 0 {
		return result, nil
	}
	inlineURLs, err := ExistingFingerprints(ctx, client, info)
	updateReason := "updated"
	if err != nil {
		inlineURLs = nil
		updateReason = "updated_without_inline_urls"
	}
	nextBody, ok := replaceSummaryLedgerBody(body, info, next, inlineURLs)
	if !ok {
		return ThreadResolutionSyncResult{Reason: "summary_shape_unsupported", Entries: len(next), Resolved: delta.Resolved, Reopened: delta.Reopened}, nil
	}
	if _, err := client.EditIssueComment(ctx, info.Owner, info.Repo, targetID, &gh.IssueComment{Body: gh.Ptr(nextBody)}); err != nil {
		return ThreadResolutionSyncResult{Reason: "summary_edit_failed", Entries: len(next), Resolved: delta.Resolved, Reopened: delta.Reopened}, mapWriteError("github.thread_resolution_sync_failed", "editing summary comment", err)
	}
	result.Action = UpsertEdited
	result.Reason = updateReason
	return result, nil
}

type ThreadResolutionLedgerDelta struct {
	Resolved int
	Reopened int
}

func SyncLedgerConversationResolved(prior []LedgerEntry, resolved map[string]bool, headSHA string, now time.Time) ([]LedgerEntry, ThreadResolutionLedgerDelta) {
	nowStr := now.UTC().Format(time.RFC3339)
	out := make([]LedgerEntry, len(prior))
	copy(out, prior)
	var delta ThreadResolutionLedgerDelta
	for i := range out {
		e := &out[i]
		if resolved[e.FP] {
			if e.Status != statusResolved {
				e.Status = statusResolved
				e.ResSHA = headSHA
				e.ResKind = resolutionConversation
				e.ResAt = nowStr
				delta.Resolved++
			}
			continue
		}
		if e.Status == statusResolved && e.ResKind == resolutionConversation {
			e.Status = statusReopened
			e.ResSHA = ""
			e.ResKind = ""
			e.ResAt = ""
			e.Reopens++
			delta.Reopened++
		}
	}
	return capLedger(out), delta
}

func lowestMarkedComment(ctx stdctx.Context, client Client, info *PRInfo) (int64, string, error) {
	opts := &gh.IssueListCommentsOptions{ListOptions: gh.ListOptions{PerPage: 100}}
	lowestID := int64(0)
	body := ""
	for page := 0; page < maxConvPages; page++ {
		comments, resp, err := client.ListIssueComments(ctx, info.Owner, info.Repo, info.Number, opts)
		if err != nil {
			return 0, "", mapWriteError("github.thread_resolution_sync_failed", "listing issue comments", err)
		}
		for _, c := range comments {
			if !strings.Contains(c.GetBody(), ReviewMarker) {
				continue
			}
			if id := c.GetID(); id > 0 && (lowestID == 0 || id < lowestID) {
				lowestID = id
				body = c.GetBody()
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return lowestID, body, nil
}

func replaceSummaryLedgerBody(body string, info *PRInfo, ledger []LedgerEntry, inlineURLs map[string]string) (string, bool) {
	if !strings.Contains(body, ReviewMarker) || !strings.Contains(body, ledgerPrefix) {
		return "", false
	}
	next, ok := replaceResultLine(body, ledgerResultLine(ledger))
	if !ok {
		return "", false
	}
	tables := renderLedgerTables(info, ledger, inlineURLs)
	if start := ledgerTablesStart(next); start >= 0 {
		end := ledgerTablesEnd(next, start)
		next = joinSummaryParts(next[:start], tables, next[end:])
	} else if insert := ledgerTailStart(next); insert >= 0 {
		next = joinSummaryParts(next[:insert], tables, next[insert:])
	} else {
		return "", false
	}
	marker := renderLedgerMarker(ledger)
	if ledgerMarkerRe.MatchString(next) {
		next = ledgerMarkerRe.ReplaceAllString(next, marker)
	} else {
		next = strings.TrimRight(next, "\n") + "\n" + marker
	}
	return next, true
}

func replaceResultLine(body, result string) (string, bool) {
	start := 0
	for start <= len(body) {
		end := strings.IndexByte(body[start:], '\n')
		lineEnd := len(body)
		if end >= 0 {
			lineEnd = start + end
		}
		if strings.HasPrefix(body[start:lineEnd], "**Result:**") {
			if end < 0 {
				return body[:start] + "**Result:** " + result, true
			}
			return body[:start] + "**Result:** " + result + body[lineEnd:], true
		}
		if end < 0 {
			break
		}
		start = lineEnd + 1
	}
	return body, false
}

func renderLedgerTables(info *PRInfo, ledger []LedgerEntry, inlineURLs map[string]string) string {
	var b strings.Builder
	renderLedger(&b, info, ledger, inlineURLs, nil)
	return strings.TrimRight(b.String(), "\n")
}

func ledgerTablesStart(body string) int {
	return earliestIndex(body, 0, []string{"**⚠️ Open (", "**✅ Resolved ("})
}

func ledgerTablesEnd(body string, start int) int {
	if end := ledgerTailStartFrom(body, start); end >= 0 {
		return end
	}
	return len(body)
}

func ledgerTailStart(body string) int {
	return ledgerTailStartFrom(body, 0)
}

func ledgerTailStartFrom(body string, start int) int {
	return earliestIndex(body, start, []string{
		"\n> Source:",
		"\n> Omitted inline:",
		"\n<details>\n<summary>Omitted inline findings",
		"\n<details>\n<summary>Important Files Changed",
		"\n<details>\n<summary>Review reference",
		"\n<sub>Last reviewed commit",
		"\n<!-- " + ledgerPrefix,
	})
}

func earliestIndex(s string, start int, needles []string) int {
	best := -1
	if start < 0 || start > len(s) {
		return best
	}
	for _, needle := range needles {
		if idx := strings.Index(s[start:], needle); idx >= 0 {
			idx += start
			if strings.HasPrefix(needle, "\n") {
				idx++
			}
			if best < 0 || idx < best {
				best = idx
			}
		}
	}
	return best
}

func joinSummaryParts(prefix, middle, suffix string) string {
	prefix = strings.TrimRight(prefix, "\n")
	middle = strings.Trim(middle, "\n")
	suffix = strings.TrimLeft(suffix, "\n")
	if middle == "" {
		if suffix == "" {
			return prefix
		}
		return prefix + "\n\n" + suffix
	}
	if suffix == "" {
		return prefix + "\n\n" + middle
	}
	return fmt.Sprintf("%s\n\n%s\n\n%s", prefix, middle, suffix)
}
