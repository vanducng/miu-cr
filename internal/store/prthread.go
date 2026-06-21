package store

import "context"

// PRKey identifies a single pull request; resolution tracking is per-PR only.
type PRKey struct {
	Owner  string
	Repo   string
	Number int
}

// PRFinding is one tracked finding on a PR. Path is required so the resolution
// check can test "stored path still in diff" for a finding absent from a run.
// Status is one of "posted" or "resolved" only.
type PRFinding struct {
	Fingerprint string
	Path        string
	Status      string
}

// PRThreadStore persists per-PR finding fingerprints for cross-push dedupe and
// resolution tracking. It is intentionally separate from Store so M6 can swap
// the backend without touching the review path.
type PRThreadStore interface {
	UpsertPosted(ctx context.Context, key PRKey, findings []PRFinding) error
	MarkResolved(ctx context.Context, key PRKey, fps []string) error
	ListFindings(ctx context.Context, key PRKey) ([]PRFinding, error)
}
