package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

var ErrHostStaleAttempt = errors.New("host job attempt is stale")

type HostStore interface {
	ReconcileHostRepo(context.Context, HostRepoInput) (HostRepo, error)
	UpsertHostPRSession(context.Context, HostPRSessionInput) (HostPRSession, error)
	EnqueueHostJob(context.Context, HostJobInput) (HostJob, bool, error)
	ClaimHostJob(context.Context, HostJobClaimInput) (HostJobClaim, bool, error)
	CompleteHostJob(context.Context, HostJobCompleteInput) error
	ReleaseHostJob(context.Context, HostJobReleaseInput) error
	ReconcileHostClosedPRs(context.Context, HostClosedPRsInput) (HostClosedPRsResult, error)
	UpsertHostWorkspace(context.Context, HostWorkspaceInput) (HostWorkspace, error)
	UpsertHostPollCursor(context.Context, HostPollCursorInput) error
	GetHostPollCursor(context.Context, int64, string) (HostPollCursor, bool, error)
	PruneHost(context.Context, HostPrunePolicy) (HostPruneResult, error)
}

type HostRepoInput struct {
	Name          string
	Owner         string
	Repo          string
	Slug          string
	GitURL        string
	DefaultBranch string
	GithubAccount string
	Enabled       bool
	Poll          bool
	ConfigHash    string
}

type HostRepo struct {
	ID int64
	HostRepoInput
	CreatedAt time.Time
	UpdatedAt time.Time
}

type HostPRSessionInput struct {
	RepoID   int64
	Number   int64
	State    string
	HeadSHA  string
	BaseSHA  string
	Branch   string
	Title    string
	ReviewID string
}

type HostPRSession struct {
	ID int64
	HostPRSessionInput
	CreatedAt time.Time
	UpdatedAt time.Time
}

type HostJobInput struct {
	RepoID      int64
	SessionID   int64
	Number      int64
	HeadSHA     string
	BaseSHA     string
	PolicyHash  string
	PromptHash  string
	RulesHash   string
	DedupeKey   string
	Priority    int
	AvailableAt time.Time
	Now         time.Time
}

type HostJob struct {
	ID int64
	HostJobInput
	Status      string
	Attempts    int
	LeaseOwner  string
	LeaseUntil  *time.Time
	ReviewID    string
	Error       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	CompletedAt *time.Time
}

type HostJobClaimInput struct {
	WorkerID      string
	Now           time.Time
	LeaseDuration time.Duration
}

type HostJobClaim struct {
	Job       HostJob
	AttemptID int64
}

type HostJobCompleteInput struct {
	JobID       int64
	AttemptID   int64
	Status      string
	ReviewID    string
	Error       string
	Now         time.Time
	AvailableAt time.Time
}

type HostJobReleaseInput struct {
	JobID       int64
	AttemptID   int64
	Error       string
	Now         time.Time
	AvailableAt time.Time
}

// HostClosedPRsInput reconciles durable state against the authoritative set of
// currently-open PR numbers from a poll. OpenNumbers must come from a complete,
// successful listing; a partial list would wrongly close live PRs.
type HostClosedPRsInput struct {
	RepoID      int64
	OpenNumbers []int64
	Now         time.Time
}

type HostClosedPRsResult struct {
	SessionsClosed int
	JobsCanceled   int
}

type HostWorkspaceInput struct {
	RepoID     int64
	SessionID  int64
	Number     int64
	Path       string
	State      string
	HeadSHA    string
	SizeBytes  int64
	LastUsedAt time.Time
}

type HostWorkspace struct {
	ID int64
	HostWorkspaceInput
	CreatedAt time.Time
	UpdatedAt time.Time
}

type HostPollCursorInput struct {
	RepoID       int64
	Source       string
	Cursor       string
	LastPolledAt time.Time
}

type HostPollCursor struct {
	HostPollCursorInput
	UpdatedAt time.Time
}

type HostPrunePolicy struct {
	ClosedSessionsBefore     time.Time
	CompletedJobsBefore      time.Time
	FinishedAttemptsBefore   time.Time
	InactiveWorkspacesBefore time.Time
	PollCursorsBefore        time.Time
}

type HostPruneResult struct {
	Sessions    int
	Jobs        int
	Attempts    int
	Workspaces  int
	PollCursors int
}

func HostJobDedupeKey(in HostJobInput) string {
	if in.DedupeKey != "" {
		return in.DedupeKey
	}
	payload, err := json.Marshal(struct {
		RepoID     int64  `json:"repo_id"`
		Number     int64  `json:"number"`
		HeadSHA    string `json:"head_sha"`
		PolicyHash string `json:"policy_hash"`
		PromptHash string `json:"prompt_hash"`
		RulesHash  string `json:"rules_hash"`
	}{in.RepoID, in.Number, in.HeadSHA, in.PolicyHash, in.PromptHash, in.RulesHash})
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
