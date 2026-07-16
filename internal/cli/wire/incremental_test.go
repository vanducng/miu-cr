package wire

import (
	stdctx "context"
	"errors"
	"testing"

	gh "github.com/google/go-github/v84/github"

	"github.com/vanducng/miu-cr/internal/cli"
	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine"
	mgithub "github.com/vanducng/miu-cr/internal/github"
	"github.com/vanducng/miu-cr/internal/store"
)

const testReuseKey = "0123456789abcdef"

// errStore is a store.Store whose LatestReviewForPR fails, proving the
// incremental check degrades to "always review" on a read error.
type errStore struct {
	store.Store
	err error
}

func (e errStore) LatestReviewForPR(stdctx.Context, store.PRKey) (store.LatestReview, bool, error) {
	return store.LatestReview{}, false, e.err
}

// prInfo builds a PRInfo for the skip tests.
func prInfo(headSHA string) *mgithub.PRInfo {
	return &mgithub.PRInfo{Owner: "o", Repo: "r", Number: 7, HeadSHA: headSHA, BaseSHA: "base", BaseBranch: "main"}
}

// TestSkipUnchangedSameSHA: a prior review of the same PR + same head SHA (no
// --force) short-circuits, surfacing the prior review id.
func TestSkipUnchangedSameSHA(t *testing.T) {
	ctx := stdctx.Background()
	st := tempStore(t)
	if _, err := st.SaveReview(ctx, store.ReviewRecord{
		ID: "prior-1", Mode: "pr", Owner: "o", Repo: "r", Number: 7, HeadSHA: "sha-1",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	prior, ok := skipUnchanged(ctx, st, prInfo("sha-1"), false, false, "", "")
	if !ok {
		t.Fatal("same head SHA must skip")
	}
	if prior.ID != "prior-1" || prior.HeadSHA != "sha-1" {
		t.Fatalf("prior = %+v, want id=prior-1 sha=sha-1", prior)
	}
}

// TestSkipUnchangedPostWithoutCompletedPublishDoesNotSkip: a saved dry-run at the
// same head is not enough for --post; the PR still needs its first completed publish.
func TestSkipUnchangedPostWithoutCompletedPublishDoesNotSkip(t *testing.T) {
	ctx := stdctx.Background()
	st := tempStore(t)
	if _, err := st.SaveReview(ctx, store.ReviewRecord{
		ID: "prior-1", Mode: "pr", Owner: "o", Repo: "r", Number: 7, HeadSHA: "sha-1",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, ok := skipUnchanged(ctx, st, prInfo("sha-1"), false, true, "", testReuseKey); ok {
		t.Fatal("--post must not skip until the PR publish is complete at this head")
	}
}

func TestSkipUnchangedPostSkipsWhenPublishCompletedAtHead(t *testing.T) {
	ctx := stdctx.Background()
	st := tempStore(t)
	if _, err := st.SaveReview(ctx, store.ReviewRecord{
		ID: "prior-1", Mode: "pr", Owner: "o", Repo: "r", Number: 7, HeadSHA: "sha-1",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	info := prInfo("sha-1")
	info.PriorPublishedHeadSHA = "sha-1"
	info.PriorPublishedKey = testReuseKey
	if prior, ok := skipUnchanged(ctx, st, info, false, true, "", testReuseKey); !ok || prior.ID != "prior-1" {
		t.Fatalf("--post same-head rerun with completed publish must skip, prior=%+v ok=%v", prior, ok)
	}
}

func TestSkipUnchangedPostSkipsFromPublishedMarkerWithoutStoreRecord(t *testing.T) {
	info := prInfo("sha-1")
	info.PriorPublishedHeadSHA = "sha-1"
	info.PriorPublishedKey = testReuseKey
	if prior, ok := skipUnchanged(stdctx.Background(), nil, info, false, true, "", testReuseKey); !ok || prior.ID != "" || prior.HeadSHA != "sha-1" {
		t.Fatalf("storeless --post same-head rerun must skip from published marker, prior=%+v ok=%v", prior, ok)
	}
}

func TestLoadPriorReviewNilStore(t *testing.T) {
	if _, ok := loadPriorReview(stdctx.Background(), nil, ""); ok {
		t.Fatal("nil store with marker-only skip must not load a prior review")
	}
}

func TestSkipUnchangedPostSkipsFromPublishedMarkerWhenStoreMisses(t *testing.T) {
	ctx := stdctx.Background()
	st := tempStore(t)
	info := prInfo("sha-1")
	info.PriorPublishedHeadSHA = "sha-1"
	info.PriorPublishedKey = testReuseKey
	if prior, ok := skipUnchanged(ctx, st, info, false, true, "", testReuseKey); !ok || prior.ID != "" || prior.HeadSHA != "sha-1" {
		t.Fatalf("ephemeral store miss must still skip from published marker, prior=%+v ok=%v", prior, ok)
	}
}

func TestSkipUnchangedPostSummaryOnlyDoesNotSkip(t *testing.T) {
	ctx := stdctx.Background()
	st := tempStore(t)
	if _, err := st.SaveReview(ctx, store.ReviewRecord{
		ID: "prior-1", Mode: "pr", Owner: "o", Repo: "r", Number: 7, HeadSHA: "sha-1",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	info := prInfo("sha-1")
	info.PriorSummaryHeadSHA = "sha-1"
	if _, ok := skipUnchanged(ctx, st, info, false, true, "", testReuseKey); ok {
		t.Fatal("--post must not skip on a pre-review summary without the published marker")
	}
}

func TestSkipUnchangedPostChangedReviewShapeDoesNotSkip(t *testing.T) {
	ctx := stdctx.Background()
	st := tempStore(t)
	if _, err := st.SaveReview(ctx, store.ReviewRecord{
		ID: "prior-1", Mode: "pr", Owner: "o", Repo: "r", Number: 7, HeadSHA: "sha-1",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	info := prInfo("sha-1")
	info.PriorPublishedHeadSHA = "sha-1"
	info.PriorPublishedKey = testReuseKey
	if _, ok := skipUnchanged(ctx, st, info, false, true, "", "fedcba9876543210"); ok {
		t.Fatal("--post must not skip when the review-shape hash changed")
	}
}

func TestSkipUnchangedPostShortPublishedSHADoesNotSkip(t *testing.T) {
	ctx := stdctx.Background()
	st := tempStore(t)
	head := "abcdef0123456789abcdef0123456789abcdef01"
	if _, err := st.SaveReview(ctx, store.ReviewRecord{
		ID: "prior-1", Mode: "pr", Owner: "o", Repo: "r", Number: 7, HeadSHA: head,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	info := prInfo(head)
	info.PriorPublishedHeadSHA = head[:7]
	info.PriorPublishedKey = testReuseKey
	if _, ok := skipUnchanged(ctx, st, info, false, true, "", testReuseKey); ok {
		t.Fatal("--post must not skip on a short published SHA prefix")
	}
}

func TestReviewReuseKeyChangesForReviewShape(t *testing.T) {
	cfg := config.Defaults()
	req := cli.PRReviewRequest{Post: true, Gate: "high", DeepContext: true, FilterMode: "diff_context"}
	base := reviewReuseKey(req, cfg)
	req.Instruction = "focus on migrations"
	if got := reviewReuseKey(req, cfg); got == base {
		t.Fatal("instruction change must change the reuse key")
	}
	req.Instruction = ""
	req.Subagents = config.ReviewSubagents{Mode: "always", Agents: []config.ReviewSubagent{{Name: "go", Include: []string{"**/*.go"}}}}
	if got := reviewReuseKey(req, cfg); got == base {
		t.Fatal("subagent config change must change the reuse key")
	}
	req.Subagents = config.ReviewSubagents{}
	cfg.Providers[string(config.KindAnthropic)] = config.Provider{Kind: config.KindAnthropic, Model: "other-model"}
	if got := reviewReuseKey(req, cfg); got == base {
		t.Fatal("provider model change must change the reuse key")
	}
	cfg = config.Defaults()
	t.Setenv("ANTHROPIC_MODEL", "miucr-env-shape-test")
	if got := reviewReuseKey(req, cfg); got == base {
		t.Fatal("model env change must change the reuse key")
	}
	cfg = config.Defaults()
	recoveryOn := reviewReuseKey(req, cfg)
	off := false
	cfg.Review.AnchorRecovery = &off
	if got := reviewReuseKey(req, cfg); got == recoveryOn {
		t.Fatal("anchor_recovery change must change the reuse key")
	}
	cfg = config.Defaults()
	cfg.Providers["custom"] = config.Provider{Kind: config.KindAnthropic, Model: "m", AuthEnv: "MIUCR_REUSE_AUTH_ENV_TEST"}
	req.Provider = "custom"
	t.Setenv("MIUCR_REUSE_AUTH_ENV_TEST", "secret-one")
	authKey := reviewReuseKey(req, cfg)
	t.Setenv("MIUCR_REUSE_AUTH_ENV_TEST", "secret-two")
	if got := reviewReuseKey(req, cfg); got == authKey {
		t.Fatal("custom auth_env value change must change the reuse key")
	}
}

func TestReviewReuseKeyIgnoresPublishOnlyFields(t *testing.T) {
	cfg := config.Defaults()
	req := cli.PRReviewRequest{Post: true, Gate: "high", DeepContext: true, FilterMode: "diff_context", Format: "full"}
	base := reviewReuseKey(req, cfg)
	req.Format = "minimal"
	req.Suggest = true
	req.Approval = config.ApprovalPolicy{Mode: "threshold", MaxPriority: "P3", Note: "on_findings"}
	if got := reviewReuseKey(req, cfg); got != base {
		t.Fatalf("publish-only fields changed reuse key: base=%s got=%s", base, got)
	}
	req.Gate = "critical"
	if got := reviewReuseKey(req, cfg); got == base {
		t.Fatal("analysis field change should change reuse key")
	}
}

func TestApprovalReuseRequiresApprovalWhenEligible(t *testing.T) {
	ctx := stdctx.Background()
	info := prInfo("sha-1")
	info.AuthorAssociation = "MEMBER"
	rec := store.ReviewRecord{Stats: map[string]any{"files_reviewed": float64(1)}}
	policy := config.ApprovalPolicy{Mode: "clean"}
	if approvalReuseOK(ctx, &fakeGitHub{}, info, rec, true, policy, "high") {
		t.Fatal("eligible clean approval rerun must not skip without an approval")
	}
	approved := &fakeGitHub{reviews: []*gh.PullRequestReview{{State: gh.Ptr("APPROVED"), CommitID: gh.Ptr("sha-1")}}}
	if !approvalReuseOK(ctx, approved, info, rec, true, policy, "high") {
		t.Fatal("eligible clean approval rerun may skip once approval exists at the head")
	}
}

func TestApprovalReuseSkipsWhenPriorSubagentsDegraded(t *testing.T) {
	ctx := stdctx.Background()
	info := prInfo("sha-1")
	info.AuthorAssociation = "MEMBER"
	rec := store.ReviewRecord{Stats: map[string]any{"files_reviewed": float64(1), "subagents_degraded": true}}
	if !approvalReuseOK(ctx, &fakeGitHub{}, info, rec, true, config.ApprovalPolicy{Mode: "clean"}, "high") {
		t.Fatal("degraded prior subagent run should reuse; approval is not expected")
	}
}

func TestApprovalReuseSkipsWhenApprovalNotExpected(t *testing.T) {
	ctx := stdctx.Background()
	info := prInfo("sha-1")
	info.AuthorAssociation = "FIRST_TIME_CONTRIBUTOR"
	clean := store.ReviewRecord{Stats: map[string]any{"files_reviewed": float64(1)}}
	cleanPolicy := config.ApprovalPolicy{Mode: "clean"}
	if !approvalReuseOK(ctx, &fakeGitHub{}, info, clean, true, cleanPolicy, "high") {
		t.Fatal("untrusted clean PR should reuse; approval is not expected")
	}
	info.AuthorAssociation = "MEMBER"
	withFinding := store.ReviewRecord{
		Findings: []engine.Finding{{Severity: "low", File: "a.go", Line: 1}},
		Stats:    map[string]any{"files_reviewed": float64(1)},
	}
	if !approvalReuseOK(ctx, &fakeGitHub{}, info, withFinding, true, cleanPolicy, "high") {
		t.Fatal("PR with findings should reuse; approval is not expected")
	}
	info.PriorLedger = []mgithub.LedgerEntry{{Status: "open"}}
	if !approvalReuseOK(ctx, &fakeGitHub{}, info, store.ReviewRecord{}, false, cleanPolicy, "high") {
		t.Fatal("storeless PR summary with open findings should reuse; approval is not expected")
	}
}

func TestApprovalStorelessReuseRequiresApprovalWhenClean(t *testing.T) {
	ctx := stdctx.Background()
	info := prInfo("sha-1")
	info.AuthorAssociation = "MEMBER"
	info.Files = []string{"a.go"}
	policy := config.ApprovalPolicy{Mode: "clean"}
	if approvalReuseOK(ctx, &fakeGitHub{}, info, store.ReviewRecord{}, false, policy, "high") {
		t.Fatal("storeless clean eligible approval rerun must not skip without approval")
	}
	approved := &fakeGitHub{reviews: []*gh.PullRequestReview{{State: gh.Ptr("APPROVED"), CommitID: gh.Ptr("sha-1")}}}
	if !approvalReuseOK(ctx, approved, info, store.ReviewRecord{}, false, policy, "high") {
		t.Fatal("storeless clean eligible approval rerun may skip once approval exists")
	}
}

func TestApprovalReuseThresholdRequiresApprovalForLowFinding(t *testing.T) {
	ctx := stdctx.Background()
	info := prInfo("sha-1")
	info.AuthorAssociation = "MEMBER"
	policy := config.ApprovalPolicy{Mode: "threshold", MaxPriority: "P3"}
	rec := store.ReviewRecord{
		Findings: []engine.Finding{{Severity: "low", File: "a.go", Line: 1}},
		Stats:    map[string]any{"files_reviewed": float64(1)},
	}
	if approvalReuseOK(ctx, &fakeGitHub{}, info, rec, true, policy, "high") {
		t.Fatal("threshold-eligible rerun must not skip without an approval")
	}
	approved := &fakeGitHub{reviews: []*gh.PullRequestReview{{State: gh.Ptr("APPROVED"), CommitID: gh.Ptr("sha-1")}}}
	if !approvalReuseOK(ctx, approved, info, rec, true, policy, "high") {
		t.Fatal("threshold-eligible rerun may skip once approval exists")
	}

	rec.Findings = []engine.Finding{{Severity: "medium", File: "a.go", Line: 1}}
	if !approvalReuseOK(ctx, &fakeGitHub{}, info, rec, true, policy, "high") {
		t.Fatal("P2 finding above P3 threshold should reuse; approval is not expected")
	}
}

func TestApprovalStorelessReuseUsesLedgerSeverity(t *testing.T) {
	ctx := stdctx.Background()
	info := prInfo("sha-1")
	info.AuthorAssociation = "MEMBER"
	info.Files = []string{"a.go"}
	policy := config.ApprovalPolicy{Mode: "threshold", MaxPriority: "P3"}

	info.PriorLedger = []mgithub.LedgerEntry{{Status: "open", Sev: "low"}}
	if approvalReuseOK(ctx, &fakeGitHub{}, info, store.ReviewRecord{}, false, policy, "high") {
		t.Fatal("storeless low finding eligible for threshold approval must not skip without approval")
	}

	approved := &fakeGitHub{reviews: []*gh.PullRequestReview{{State: gh.Ptr("APPROVED"), CommitID: gh.Ptr("sha-1")}}}
	if !approvalReuseOK(ctx, approved, info, store.ReviewRecord{}, false, policy, "high") {
		t.Fatal("storeless low finding may skip once approval exists")
	}

	info.PriorLedger = []mgithub.LedgerEntry{{Status: "open", Sev: "high"}}
	if !approvalReuseOK(ctx, &fakeGitHub{}, info, store.ReviewRecord{}, false, policy, "high") {
		t.Fatal("storeless high finding should reuse; approval is not expected")
	}

	info.PriorLedger = []mgithub.LedgerEntry{{Status: "open"}}
	if !approvalReuseOK(ctx, &fakeGitHub{}, info, store.ReviewRecord{}, false, policy, "high") {
		t.Fatal("storeless missing severity should be conservative; approval is not expected")
	}
}

// TestSkipUnchangedChecksModeNeverSkips: --mode checks --post always publishes the
// (idempotent per-SHA) CheckRun, even at an already-reviewed SHA.
func TestSkipUnchangedChecksModeNeverSkips(t *testing.T) {
	ctx := stdctx.Background()
	st := tempStore(t)
	if _, err := st.SaveReview(ctx, store.ReviewRecord{
		ID: "prior-1", Mode: "pr", Owner: "o", Repo: "r", Number: 7, HeadSHA: "sha-1",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	info := prInfo("sha-1")
	info.PriorPublishedHeadSHA = "sha-1"
	info.PriorPublishedKey = testReuseKey
	if _, ok := skipUnchanged(ctx, st, info, false, true, "checks", testReuseKey); ok {
		t.Fatal("--mode checks --post must always publish the CheckRun")
	}
}

// TestSkipUnchangedDifferentSHA: a new commit (changed head SHA) always reviews.
func TestSkipUnchangedDifferentSHA(t *testing.T) {
	ctx := stdctx.Background()
	st := tempStore(t)
	if _, err := st.SaveReview(ctx, store.ReviewRecord{
		ID: "prior-1", Mode: "pr", Owner: "o", Repo: "r", Number: 7, HeadSHA: "sha-1",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, ok := skipUnchanged(ctx, st, prInfo("sha-2"), false, false, "", ""); ok {
		t.Fatal("a changed head SHA must NOT skip")
	}
}

// TestSkipUnchangedForce: --force re-reviews even on an unchanged head SHA.
func TestSkipUnchangedForce(t *testing.T) {
	ctx := stdctx.Background()
	st := tempStore(t)
	if _, err := st.SaveReview(ctx, store.ReviewRecord{
		ID: "prior-1", Mode: "pr", Owner: "o", Repo: "r", Number: 7, HeadSHA: "sha-1",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, ok := skipUnchanged(ctx, st, prInfo("sha-1"), true, false, "", ""); ok {
		t.Fatal("--force must bypass the skip")
	}
}

// TestSkipUnchangedNoPrior: a first review of a PR (no prior record) reviews.
func TestSkipUnchangedNoPrior(t *testing.T) {
	ctx := stdctx.Background()
	st := tempStore(t)
	if _, ok := skipUnchanged(ctx, st, prInfo("sha-1"), false, false, "", ""); ok {
		t.Fatal("no prior review must NOT skip")
	}
}

// TestSkipUnchangedNilStore: history off / --no-save (nil store) dry-run reviews.
func TestSkipUnchangedNilStore(t *testing.T) {
	if _, ok := skipUnchanged(stdctx.Background(), nil, prInfo("sha-1"), false, false, "", ""); ok {
		t.Fatal("a nil history store must NOT skip (degrade to always-review)")
	}
}

// TestSkipUnchangedReadErrorDegrades: a store read failure degrades to
// always-review (no skip), never blocking the review.
func TestSkipUnchangedReadErrorDegrades(t *testing.T) {
	st := errStore{err: errors.New("db locked")}
	if _, ok := skipUnchanged(stdctx.Background(), st, prInfo("sha-1"), false, false, "", ""); ok {
		t.Fatal("a read error must degrade to always-review, not skip")
	}
}
