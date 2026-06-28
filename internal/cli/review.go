package cli

import (
	stdctx "context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine"
	ghub "github.com/vanducng/miu-cr/internal/github"
	"github.com/vanducng/miu-cr/internal/sarif"
)

// classifyReviewErr types a ctx timeout/cancel raised during the review pass;
// the review layer is the one place that knows the --timeout value. Any other
// error passes through unchanged (preserving an already-typed CLIError or a bare
// %w). This is the timeout owner; backends keep the ctx chain via %w so the
// errors.Is below still reaches it through the engine pass-through.
func classifyReviewErr(err error, timeout time.Duration) error {
	switch {
	case errors.Is(err, stdctx.DeadlineExceeded):
		msg := "review timed out"
		if timeout > 0 { // only the CLI-owned deadline knows the budget; a 0 means the deadline came from elsewhere
			msg = fmt.Sprintf("review timed out after %s", timeout)
		}
		return &CLIError{
			Code:    "review.timeout",
			Message: msg,
			Hint:    "raise --timeout (e.g. 1800s) or narrow the diff",
			Exit:    1,
			Retry:   true,
			Cause:   err,
		}
	case errors.Is(err, stdctx.Canceled):
		return &CLIError{
			Code:    "review.canceled",
			Message: "review canceled",
			Exit:    130,
			Cause:   err,
		}
	default:
		return err
	}
}

// ReviewRequest is the mode-agnostic review invocation passed to the injected
// Reviewer. It mirrors engine.Request but lives in cli so the engine (which
// transitively imports cli for CLIError) is not imported here, avoiding a cycle.
type ReviewRequest struct {
	Staged          bool
	From            string
	To              string
	Commit          string
	Gate            string
	RepoDir         string
	IncludeGlobs    []string
	ExcludeGlobs    []string
	Extensions      []string
	Provider        string
	APIKey          string
	BaseURL         string
	AuthToken       string
	Model           string
	Timeout         time.Duration
	ExpandWindow    int
	TokenBudget     int
	DeepContext     bool
	ContextHops     int
	ContextHopsAuto bool
	Subagents       config.ReviewSubagents
	FilterMode      string // added|diff_context|file|nofilter (default diff_context)
	WantDiagram     bool   // opt into the mermaid change diagram (default off)
	Instruction     string // optional per-review developer steer; injected fenced/context-only into the USER turn
	OperatorPrompt  string
	NoSave          bool         // opt out of persisting this run to the local history store
	Progress        func(string) // nil = silent; stderr milestones, never the stdout envelope
	// TraceSink, when non-nil, streams each captured trace step (system prompt, diff
	// meta, selected files, injected rules, prompts, response, tool calls) live to
	// stderr as NDJSON (--trace). Local-only; distinct from Progress; the stdout
	// result envelope is untouched.
	TraceSink func(step string, payload any)
}

// ReviewOutcome is the Reviewer's result: anchored findings plus run stats. PR
// is non-nil only on the --pr path and drives the data.pr envelope block.
// ReviewID is the saved record id; it surfaces as the additive review_id envelope
// field. Empty only when the review was not persisted (--no-save). On an
// incremental skip it is the prior review's id (the run reuses that review), not "".
type ReviewOutcome struct {
	Findings []ReviewFinding
	Stats    map[string]any
	PR       *PRResult
	ReviewID string

	// SkippedUnchanged is set on the --pr incremental-skip path: a prior review of
	// the same PR + same head SHA exists and --force was not passed, so no LLM pass
	// ran. PriorReviewID is that prior record's id. Both stay zero on a normal run.
	SkippedUnchanged bool
	PriorReviewID    string
}

// PRResult is the typed PR summary for the data.pr envelope block on the --pr
// path. The token is never carried here (or anywhere in the envelope).
// PostedInline is the count of inline comments posted THIS run (0 on --no-post
// and on re-runs where everything was already posted); SummaryAction reports the
// fate of the single upserted summary issue comment:
// none|created|edited|fork_fallback|failed ("none" on --no-post, checks mode, or
// a clean no-summary run; created vs edited on --post; failed if the upsert
// errored after the inline review already posted).
type PRResult struct {
	Owner         string `json:"owner"`
	Repo          string `json:"repo"`
	Number        int    `json:"number"`
	HeadSHA       string `json:"head_sha"`
	IsFork        bool   `json:"is_fork"`
	Posted        bool   `json:"posted"`
	PostedInline  int    `json:"posted_inline"`
	SummaryAction string `json:"summary_action"`

	// Opt-in write-action outcomes (default OFF). ApproveAction is approved|commented;
	// ApproveReason carries the degrade reason when commented. SuggestionsPosted counts
	// native one-click suggestions emitted this run.
	ApproveAction     string `json:"approve_action"`
	ApproveReason     string `json:"approve_reason"`
	SuggestionsPosted int    `json:"suggestions_posted"`

	// PatchesRepaired counts findings whose rejected suggested patch was recovered
	// by the --patch-repair second pass into a now-clean one-click suggestion this
	// run (0/absent when --patch-repair is OFF). Source of truth is the engine stat.
	PatchesRepaired int `json:"patches_repaired,omitempty"`

	// Mode is the GitHub reporter used: review (inline+summary) | checks (CheckRun).
	// Checks-only fields are populated under --mode checks; FallbackAnnotations counts
	// ::error:: workflow annotations emitted on the fork-PR 403 fallback (0 normally).
	Mode                string `json:"mode,omitempty"`
	CheckRunID          int64  `json:"check_run_id,omitempty"`
	CheckConclusion     string `json:"check_conclusion,omitempty"`
	FallbackAnnotations int    `json:"fallback_annotations,omitempty"`
}

// ReviewFinding is a single anchored finding rendered/serialized by cli.
type ReviewFinding struct {
	File           string `json:"file"`
	Line           int    `json:"line"`
	EndLine        int    `json:"end_line"`
	Title          string `json:"title,omitempty"`
	Rule           string `json:"rule,omitempty"`
	Severity       string `json:"severity"`
	Category       string `json:"category"`
	Rationale      string `json:"rationale"`
	SuggestedPatch string `json:"suggested_patch"`
	QuotedCode     string `json:"quoted_code"`
}

// Reviewer runs the engine pipeline. The real implementation is injected at
// startup (internal/cli/wire) so cli stays below engine/agent in the import
// graph. GateFailed reports whether the outcome's worst severity reaches gate.
type Reviewer interface {
	Review(ctx stdctx.Context, req ReviewRequest) (ReviewOutcome, error)
	GateFailed(findings []ReviewFinding, gate string) bool
}

var reviewer Reviewer

// SetReviewer wires the engine-backed Reviewer. Called once from the wire
// package's init before any command runs.
func SetReviewer(r Reviewer) { reviewer = r }

// PRReviewRequest is the --pr invocation: the PR ref plus the resolved-but-
// in-memory-only GitHub token (PAT) and whether to post. The LLM-credential
// fields mirror ReviewRequest (findings still require the LLM).
type PRReviewRequest struct {
	Ref         string
	Token       string
	Post        bool
	Suggest     bool
	PatchRepair bool
	Approval    config.ApprovalPolicy
	Gate        string
	Provider    string
	APIKey      string
	BaseURL     string
	AuthToken   string
	Model       string
	Timeout     time.Duration

	// Quota is the resolved provider's usage quota (nil = none). Threaded by the
	// serve host whose host-config provider quota is not in the user config.toml;
	// the CLI --pr path leaves it nil and wire looks it up from the loaded config.
	// QuotaProvider is the counter key (the provider instance name) — set by serve
	// (where Provider carries the kind); empty on the CLI path (wire uses Provider).
	Quota         *config.QuotaConfig
	QuotaProvider string

	IncludeGlobs    []string
	ExcludeGlobs    []string
	Extensions      []string
	ExpandWindow    int
	TokenBudget     int
	DeepContext     bool
	ContextHops     int
	ContextHopsAuto bool
	Subagents       config.ReviewSubagents
	FilterMode      string // added|diff_context|file|nofilter (default diff_context)
	MinSeverity     string // inline-posting floor: none|info|low|medium|high|critical (default keeps current behavior)
	Format          string // review-comment presentation preset: full (default) | minimal
	WantDiagram     bool   // opt into the mermaid change diagram (default off)
	Instruction     string // optional per-review developer steer; injected fenced/context-only into the USER turn
	OperatorPrompt  string
	Conversation    bool         // opt into fetching the prior PR conversation; injected fenced/context-only, Untrusted, dropped on fork PRs
	Mode            string       // review (default: inline+summary) | checks (GitHub Checks-API reporter)
	NoSave          bool         // opt out of persisting this run to the local history store
	Force           bool         // re-review even when the head SHA is unchanged since the last review (bypass the incremental skip)
	Progress        func(string) // nil = silent; stderr milestones, never the stdout envelope
	// TraceSink streams live trace steps to stderr as NDJSON (--trace); nil = off.
	TraceSink func(step string, payload any)
	// ActionsOut is the command's stdout writer (cmd.OutOrStdout()), used ONLY by the
	// fork-PR 403 fallback to emit ::error:: workflow commands on the same stream as
	// the JSON envelope (GitHub parses workflow commands only from stdout). nil →
	// PostReview falls back to os.Stdout. Not the Progress stream (that is stderr).
	ActionsOut io.Writer
}

// PRReviewer fetches a GitHub PR, runs the engine on a temp clone via ModeRange,
// and (in P2) publishes. Injected from wire so cli stays below github/engine in
// the import graph. GateFailed mirrors Reviewer so the --pr gate is evaluated from
// the PR review's own findings, not a separate local-mode reviewer instance.
type PRReviewer interface {
	ReviewPR(ctx stdctx.Context, req PRReviewRequest) (ReviewOutcome, error)
	GateFailed(findings []ReviewFinding, gate string) bool
}

var prReviewer PRReviewer

// SetPRReviewer wires the github-backed PR reviewer. Called exactly once from
// wire.init, which happens-before any serve worker goroutine that calls
// ReviewPRForServe, so the package-level read needs no lock or atomic.
func SetPRReviewer(r PRReviewer) { prReviewer = r }

// ReviewPRForServe is the in-process seam serve calls: it delegates straight to
// the wired prReviewer.ReviewPR (NOT runPRReview) so the gate_failed exit path is
// bypassed: serve's gate governs publish severity only, never worker liveness.
func ReviewPRForServe(ctx stdctx.Context, req PRReviewRequest) (ReviewOutcome, error) {
	if prReviewer == nil {
		return ReviewOutcome{}, &CLIError{Code: "review.not_wired", Message: "PR review engine not wired", Exit: 1}
	}
	return prReviewer.ReviewPR(ctx, req)
}

// resolveGitHubToken applies the M2 token precedence: --token > GITHUB_TOKEN >
// GH_TOKEN. Empty is allowed (anonymous client for public-repo reads); the
// caller enforces "token required" only for --post. Kept local because the agent
// package's firstNonEmpty is unexported.
func resolveGitHubToken(flag string) string {
	for _, v := range []string{flag, os.Getenv("GITHUB_TOKEN"), os.Getenv("GH_TOKEN")} {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// nudgeIfUnconfigured returns a friendly typed nudge to run `miucr init` only
// when nothing at all provides auth: no config file on disk AND no LLM-credential
// env var AND no --api-key/--auth-token flag. Soft by design: when a key or flag
// IS present, this is a no-op and review proceeds (zero-config still works).
func nudgeIfUnconfigured(apiKey, authToken string) error {
	if strings.TrimSpace(apiKey) != "" || strings.TrimSpace(authToken) != "" {
		return nil
	}
	for _, k := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY"} {
		if strings.TrimSpace(os.Getenv(k)) != "" {
			return nil
		}
	}
	if p := config.FilePathOrEmpty(); p != "" {
		if _, err := os.Stat(p); err == nil {
			return nil // user has a config file; let resolve emit any specific error
		}
	}
	if p, perr := config.OAuthPath(); perr == nil && p != "" {
		if _, err := os.Stat(p); err == nil {
			return nil // user has a cached `miucr login` credential; let resolve use it
		}
	}
	return &CLIError{
		Code:    "provider.unconfigured",
		Message: "no LLM provider configured: no config, no API-key env var, and no --api-key",
		Hint:    "run `miucr init` (≈30s) or set ANTHROPIC_API_KEY",
		Exit:    1,
	}
}

const defaultTokenBudget = 0
const defaultReviewTimeout = 15 * time.Minute
const maxContextHops = 5

func reviewCommand(opts *options) *cobra.Command {
	var (
		staged       bool
		from         string
		to           string
		commit       string
		gate         string
		repoDir      string
		include      []string
		exclude      []string
		exts         []string
		provider     string
		apiKey       string
		baseURL      string
		authToken    string
		model        string
		expand       int
		tokenBudget  int
		deepContext  bool
		contextHops  int
		filterMode   string
		minSeverity  string
		format       string
		wantDiagram  bool
		instruction  string
		conversation bool
		mode         string
		sarifOut     string
		pr           string
		token        string
		post         bool
		noPost       bool
		suggest      bool
		patchRepair  bool
		approval     config.ApprovalPolicy
		noSave       bool
		force        bool
		verbose      bool
		quiet        bool
		traceLive    bool
	)

	cmd := &cobra.Command{
		Use:   "review",
		Short: "Review local git changes and emit gated findings",
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if err := validateFilterMode(filterMode); err != nil {
				return err
			}
			if err := validateMinSeverity(minSeverity); err != nil {
				return err
			}
			if err := validateFormat(format); err != nil {
				return err
			}
			if contextHops < 0 || contextHops > maxContextHops {
				return &CLIError{Code: "config.invalid", Message: fmt.Sprintf("--context-hops must be between 0 and %d", maxContextHops), Exit: 2}
			}
			// --mode (review|checks) only steers the PR reporter; it's inert for a
			// local review, so only validate it when the PR path is in play.
			if pr != "" {
				if err := validateMode(mode); err != nil {
					return err
				}
				return validatePRFlags(post, noPost, token)
			}
			return validateReviewFlags(staged, from, to, commit, gate)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			rcfg, err := loadReviewDefaults(cmd, &gate, &filterMode, &minSeverity, &format, &expand, &tokenBudget, &deepContext, &contextHops, &conversation, &suggest, &patchRepair, &approval)
			if err != nil {
				return err
			}
			if err := validateApprovalPolicy(approval); err != nil {
				return err
			}
			// Fail loud: --patch-repair only recovers one-click suggestions, so it is
			// meaningless without --suggest. A new intentional flag-combination guard
			// (config.invalid), not a silent no-op.
			if patchRepair && !suggest {
				return &CLIError{Code: "config.invalid", Message: "--patch-repair requires --suggest", Exit: 2}
			}
			// Review uses a 15m default unless config or flags override it.
			if !cmd.Flags().Changed("timeout") {
				opts.timeout = reviewTimeoutDefault(rcfg)
			}
			if deepContext {
				if !cmd.Flags().Changed("expand") && rcfg.Expand == nil {
					expand = 20
				}
				if !cmd.Flags().Changed("token-budget") && rcfg.TokenBudget == nil {
					tokenBudget = 0
				}
				if !cmd.Flags().Changed("timeout") && rcfg.Timeout == "" {
					opts.timeout = defaultReviewTimeout
				}
				if !cmd.Flags().Changed("context-hops") && rcfg.ContextHops == nil {
					contextHops = 0
				}
			}
			contextHopsAuto := deepContext && !cmd.Flags().Changed("context-hops") && rcfg.ContextHops == nil
			prog := newProgress(cmd.ErrOrStderr(), verbose, quiet)
			var traceSink func(step string, payload any)
			if traceLive {
				traceSink = newTraceSink(cmd.ErrOrStderr())
			}
			if pr != "" {
				return runPRReview(cmd, prRunArgs{
					ref:             pr,
					token:           token,
					post:            post && !noPost,
					suggest:         suggest,
					patchRepair:     patchRepair,
					approval:        approval,
					gate:            gate,
					provider:        provider,
					apiKey:          apiKey,
					baseURL:         baseURL,
					authToken:       authToken,
					model:           model,
					timeout:         opts.timeout,
					include:         include,
					exclude:         exclude,
					exts:            exts,
					expand:          expand,
					tokenBudget:     tokenBudget,
					deepContext:     deepContext,
					contextHops:     contextHops,
					contextHopsAuto: contextHopsAuto,
					subagents:       rcfg.Subagents,
					filterMode:      filterMode,
					minSeverity:     minSeverity,
					format:          format,
					wantDiagram:     wantDiagram,
					instruction:     instruction,
					conversation:    conversation,
					mode:            mode,
					sarifOut:        sarifOut,
					noSave:          noSave,
					force:           force,
					progress:        prog,
					traceSink:       traceSink,
				})
			}
			if err := nudgeIfUnconfigured(apiKey, authToken); err != nil {
				return err
			}
			if reviewer == nil {
				return &CLIError{Code: "review.not_wired", Message: "review engine not wired", Exit: 1}
			}
			req := ReviewRequest{
				Staged:          staged,
				From:            from,
				To:              to,
				Commit:          commit,
				Gate:            gate,
				RepoDir:         repoDir,
				IncludeGlobs:    include,
				ExcludeGlobs:    exclude,
				Extensions:      exts,
				Provider:        provider,
				APIKey:          apiKey,
				BaseURL:         baseURL,
				AuthToken:       authToken,
				Model:           model,
				Timeout:         opts.timeout,
				ExpandWindow:    expand,
				TokenBudget:     tokenBudget,
				DeepContext:     deepContext,
				ContextHops:     contextHops,
				ContextHopsAuto: contextHopsAuto,
				Subagents:       rcfg.Subagents,
				FilterMode:      filterMode,
				WantDiagram:     wantDiagram,
				Instruction:     instruction,
				NoSave:          noSave,
				Progress:        prog,
				TraceSink:       traceSink,
			}
			ctx := cmd.Context()
			if opts.timeout > 0 {
				var cancel stdctx.CancelFunc
				ctx, cancel = stdctx.WithTimeout(ctx, opts.timeout)
				defer cancel()
			}
			out, err := reviewer.Review(ctx, req)
			if err != nil {
				return classifyReviewErr(err, opts.timeout)
			}
			if prog != nil {
				prog(fmt.Sprintf("done: %d findings", len(out.Findings)))
			}
			if err := writeSARIFOut(sarifOut, out.Findings); err != nil {
				return err
			}
			summary := map[string]any{
				"findings": len(out.Findings),
				"gate":     gate,
			}
			data := map[string]any{
				"findings":  out.Findings,
				"stats":     out.Stats,
				"review_id": out.ReviewID,
			}
			if err := emitReview(cmd.OutOrStdout(), out, data, summary); err != nil {
				return err
			}
			if reviewer.GateFailed(out.Findings, gate) {
				return &CLIError{
					Code:           "review.gate_failed",
					Message:        fmt.Sprintf("findings reached gate %q", gate),
					Exit:           2,
					AlreadyWritten: true,
				}
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.BoolVar(&staged, "staged", false, "Review staged changes against the index")
	f.StringVar(&from, "from", "", "Range mode: base ref (use with --to)")
	f.StringVar(&to, "to", "", "Range mode: target ref (use with --from)")
	f.StringVar(&commit, "commit", "", "Review a single commit against its parent")
	f.StringVar(&gate, "gate", "high", "Fail (exit 2) when a finding reaches this severity: none|info|low|medium|high|critical")
	f.StringVar(&repoDir, "repo", ".", "Repository directory")
	f.StringSliceVar(&include, "include", nil, "Doublestar globs a path must match")
	f.StringSliceVar(&exclude, "exclude", nil, "Doublestar globs to drop")
	f.StringSliceVar(&exts, "ext", nil, "Restrict review to these file extensions")
	f.StringVar(&provider, "provider", "auto", "LLM provider profile: anthropic|openai|<configured name>|auto (auto detects from env / config default_provider)")
	f.StringVar(&apiKey, "api-key", "", "API key for the selected/default provider (anthropic unless --provider or config default_provider says otherwise; for OpenAI pass --provider openai); overrides ANTHROPIC_API_KEY/OPENAI_API_KEY; never persisted")
	f.StringVar(&baseURL, "base-url", "", "Override provider base URL (e.g. an Anthropic-compatible gateway; never persisted)")
	f.StringVar(&authToken, "auth-token", "", "Bearer auth token for Anthropic-compatible gateways, Anthropic only (never persisted)")
	f.StringVar(&model, "model", "", "Override the review model (else ANTHROPIC_MODEL/OPENAI_MODEL or pinned default)")
	f.IntVar(&expand, "expand", 5, "Context lines added above/below each hunk in the new-content window (0 disables)")
	f.IntVar(&tokenBudget, "token-budget", defaultTokenBudget, "Approximate token budget; over budget degrades context (0 disables)")
	f.BoolVar(&deepContext, "deep-context", false, "Use heavier context defaults for large reviews: --expand 20, --token-budget 0, --timeout 900s, auto --context-hops unless those flags are set")
	f.IntVar(&contextHops, "context-hops", 0, "Related-file context hop depth from changed files (0 disables, max 5)")
	f.StringVar(&filterMode, "filter-mode", "diff_context", "Inline-eligibility filter on --pr: added|diff_context|file|nofilter (default diff_context; file/nofilter route off-diff findings to the summary/SARIF/local output, never inline)")
	f.StringVar(&minSeverity, "min-severity", "", "Minimum severity posted INLINE on --pr: none|info|low|medium|high|critical (default keeps current behavior; below-threshold findings still appear in the summary histogram + SARIF, never inline)")
	f.StringVar(&format, "format", "", "Review-comment presentation on --pr: full (default) | minimal (minimal drops the summary section + severity/priority badges, keeping inline findings)")
	f.BoolVar(&wantDiagram, "walkthrough-diagram", false, "Ask the model to also emit an optional mermaid change diagram in the summary (opt-in; diagram quality varies; a malformed/omitted diagram degrades to a plain note)")
	f.StringVar(&instruction, "instruction", "", "Extra free-text steer for THIS review (e.g. 'focus on the auth changes'); injected fenced, context-only, and length-capped, so it never redefines the finding schema")
	f.BoolVar(&conversation, "conversation", false, "On --pr, fetch the prior PR conversation (miucr summary + review overviews + finding threads + developer replies) and inject it fenced/context-only as UNTRUSTED context (dropped on fork PRs); one extra read pass, no extra LLM call (default OFF)")
	f.StringVar(&mode, "mode", "review", "GitHub reporter on --pr --post: review (inline comments + summary, default) | checks (a GitHub CheckRun with annotations — survives force-push, works on fork PRs, can be a required check)")
	f.StringVar(&sarifOut, "sarif-out", "", "Also write a SARIF 2.1.0 report to this path (in addition to the normal --output/posting), from the same single review run; written only on success (atomic temp+rename, so a failed run leaves no file)")
	f.StringVar(&pr, "pr", "", "Review a GitHub PR: https://github.com/owner/repo/pull/N or owner/repo#N (no GitHub PAT needed for public repos in dry-run)")
	f.StringVar(&token, "token", "", "GitHub PAT (overrides GITHUB_TOKEN/GH_TOKEN; required only for --post; never persisted)")
	f.BoolVar(&post, "post", false, "Publish inline comments + a summary to the PR (requires a token)")
	f.BoolVar(&noPost, "no-post", false, "Dry-run the PR review without posting (default for --pr)")
	f.BoolVar(&suggest, "suggest", false, "Emit GitHub native one-click suggestions for proven single-line replacements; author-applied, never pushed. Requires --post (inert in dry-run) (default OFF; else a plain hint)")
	f.BoolVar(&patchRepair, "patch-repair", false, "On --suggest, run a focused 2nd LLM pass per finding whose suggested patch was rejected, to recover a one-click suggestion (highest-severity-first, capped). Requires --suggest; one extra LLM call per repaired candidate (default OFF)")
	f.StringVar(&approval.Mode, "approval", "off", "Approval policy on --pr --post: off|clean|threshold. A PAT APPROVE counts toward required reviews. Requires --post (inert in dry-run)")
	f.StringVar(&approval.MaxPriority, "approval-max-priority", "", "For --approval threshold, approve only when the worst active finding is at or below this priority: P0|P1|P2|P3|P4 (default P4)")
	f.StringVar(&approval.Note, "approval-note", "", "Approval review body policy: none|on_findings|always (default none for clean, on_findings for threshold)")
	f.BoolVar(&noSave, "no-save", false, "Do not persist this review to the local history store (default: every review is saved to ~/.config/miu/cr/state.db)")
	f.BoolVar(&force, "force", false, "On --pr, re-review even when the head SHA is unchanged since the last saved review (default: an unchanged head SHA short-circuits with skipped_unchanged, no LLM pass)")
	f.BoolVarP(&verbose, "verbose", "v", false, "Print progress to stderr (default when stderr is a terminal; stdout envelope unchanged)")
	f.BoolVarP(&quiet, "quiet", "q", false, "Silence progress output (overrides --verbose and TTY auto-detect)")
	f.BoolVar(&traceLive, "trace", false, "Stream the live review trace (system prompt, diff, rules, prompts, response) as NDJSON to stderr; local-only, distinct from --verbose; the stdout result envelope is unchanged")

	cmd.MarkFlagsRequiredTogether("from", "to")
	cmd.MarkFlagsMutuallyExclusive("verbose", "quiet")
	return cmd
}

// prRunArgs bundles the --pr invocation values RunE forwards to runPRReview.
type prRunArgs struct {
	ref         string
	token       string
	post        bool
	suggest     bool
	patchRepair bool
	approval    config.ApprovalPolicy
	gate        string
	provider    string
	apiKey      string
	baseURL     string
	authToken   string
	model       string
	timeout     time.Duration

	include         []string
	exclude         []string
	exts            []string
	expand          int
	tokenBudget     int
	deepContext     bool
	contextHops     int
	contextHopsAuto bool
	subagents       config.ReviewSubagents
	filterMode      string
	minSeverity     string
	format          string
	wantDiagram     bool
	instruction     string
	conversation    bool
	mode            string
	sarifOut        string
	noSave          bool
	force           bool
	progress        func(string)
	traceSink       func(step string, payload any)
}

// runPRReview drives the --pr path: resolve the GitHub token (empty-tolerant for
// public dry-runs), invoke the injected PRReviewer, emit a miucr.cli/v1 envelope
// with a data.pr block. The token never enters the envelope.
func runPRReview(cmd *cobra.Command, a prRunArgs) error {
	if prReviewer == nil {
		return &CLIError{Code: "review.not_wired", Message: "PR review engine not wired", Exit: 1}
	}
	ghToken := resolveGitHubToken(a.token)
	if a.post && ghToken == "" {
		return &CLIError{
			Code:    "github.post_requires_token",
			Message: "--post needs a GitHub token: pass --token or set GITHUB_TOKEN/GH_TOKEN",
			Hint:    "create a PAT with repo scope; dry-run (--no-post) needs no token for public repos",
			Exit:    2,
		}
	}

	ctx := cmd.Context()
	if a.timeout > 0 {
		var cancel stdctx.CancelFunc
		ctx, cancel = stdctx.WithTimeout(ctx, a.timeout)
		defer cancel()
	}

	out, err := prReviewer.ReviewPR(ctx, PRReviewRequest{
		Ref:             a.ref,
		Token:           ghToken,
		Post:            a.post,
		Suggest:         a.suggest,
		PatchRepair:     a.patchRepair,
		Approval:        a.approval,
		Gate:            a.gate,
		Provider:        a.provider,
		APIKey:          a.apiKey,
		BaseURL:         a.baseURL,
		AuthToken:       a.authToken,
		Model:           a.model,
		Timeout:         a.timeout,
		IncludeGlobs:    a.include,
		ExcludeGlobs:    a.exclude,
		Extensions:      a.exts,
		ExpandWindow:    a.expand,
		TokenBudget:     a.tokenBudget,
		DeepContext:     a.deepContext,
		ContextHops:     a.contextHops,
		ContextHopsAuto: a.contextHopsAuto,
		Subagents:       a.subagents,
		FilterMode:      a.filterMode,
		MinSeverity:     a.minSeverity,
		Format:          a.format,
		WantDiagram:     a.wantDiagram,
		Instruction:     a.instruction,
		Conversation:    a.conversation,
		Mode:            a.mode,
		NoSave:          a.noSave,
		Force:           a.force,
		Progress:        a.progress,
		TraceSink:       a.traceSink,
		ActionsOut:      cmd.OutOrStdout(),
	})
	if err != nil {
		return classifyReviewErr(err, a.timeout)
	}

	// Incremental-skip path (additive, back-compatible): an unchanged head SHA
	// short-circuited with no LLM pass. Emit a coherent envelope: findings as an
	// empty array (never null) + an empty stats object, so a consumer expecting an
	// array/object shape doesn't break, and surface skipped_unchanged +
	// prior_review_id. Do NOT write SARIF: a zero-finding write would clobber a
	// prior good report. No gate evaluation (no findings ran this pass).
	if out.SkippedUnchanged {
		if a.progress != nil {
			a.progress("skipped: unchanged head SHA already reviewed")
		}
		data := map[string]any{
			"findings":          []ReviewFinding{},
			"stats":             map[string]any{},
			"review_id":         out.ReviewID,
			"skipped_unchanged": true,
			"prior_review_id":   out.PriorReviewID,
		}
		if out.PR != nil {
			data["pr"] = out.PR
		}
		summary := map[string]any{"findings": 0, "gate": a.gate, "skipped_unchanged": true}
		return emitReview(cmd.OutOrStdout(), out, data, summary)
	}

	if a.progress != nil {
		a.progress(fmt.Sprintf("done: %d findings", len(out.Findings)))
	}
	if err := writeSARIFOut(a.sarifOut, out.Findings); err != nil {
		return err
	}

	summary := map[string]any{"findings": len(out.Findings), "gate": a.gate}
	data := map[string]any{"findings": out.Findings, "stats": out.Stats, "review_id": out.ReviewID}
	if out.PR != nil {
		data["pr"] = out.PR
	}
	if err := emitReview(cmd.OutOrStdout(), out, data, summary); err != nil {
		return err
	}
	if prReviewer.GateFailed(out.Findings, a.gate) {
		return &CLIError{
			Code:           "review.gate_failed",
			Message:        fmt.Sprintf("findings reached gate %q", a.gate),
			Exit:           2,
			AlreadyWritten: true,
		}
	}
	return nil
}

// loadReviewDefaults loads [review] from config, validates it (config.invalid on
// a bad enum/timeout), and fills any review flag the user did NOT set on the
// command line. An explicit flag always wins (cmd.Flags().Changed); a config
// value only fills an unset flag. Returns the validated Review so the caller can
// also derive the timeout default. A config-load error propagates (typed).
func loadReviewDefaults(cmd *cobra.Command, gate, filterMode, minSeverity, format *string, expand, tokenBudget *int, deepContext *bool, contextHops *int, conversation, suggest, patchRepair *bool, approval *config.ApprovalPolicy) (config.Review, error) {
	cfg, err := config.Load()
	if err != nil {
		return config.Review{}, err
	}
	r := cfg.Review
	if err := config.ValidateReview(r); err != nil {
		return r, err
	}
	if err := config.ValidateProviderQuotas(cfg.Providers); err != nil {
		return r, err
	}
	f := cmd.Flags()
	if r.Gate != "" && !f.Changed("gate") {
		*gate = r.Gate
	}
	if r.FilterMode != "" && !f.Changed("filter-mode") {
		*filterMode = r.FilterMode
	}
	if r.MinSeverity != "" && !f.Changed("min-severity") {
		*minSeverity = r.MinSeverity
	}
	if r.Format != "" && !f.Changed("format") {
		*format = r.Format
	}
	if r.Expand != nil && !f.Changed("expand") {
		*expand = *r.Expand
	}
	if r.TokenBudget != nil && !f.Changed("token-budget") {
		*tokenBudget = *r.TokenBudget
	}
	if r.DeepContext != nil && !f.Changed("deep-context") {
		*deepContext = *r.DeepContext
	}
	if r.ContextHops != nil && !f.Changed("context-hops") {
		*contextHops = *r.ContextHops
	}
	if r.Conversation != nil && !f.Changed("conversation") {
		*conversation = *r.Conversation
	}
	if r.Suggest != nil && !f.Changed("suggest") {
		*suggest = *r.Suggest
	}
	if r.PatchRepair != nil && !f.Changed("patch-repair") {
		*patchRepair = *r.PatchRepair
	}
	if r.Approval.Mode != "" && !f.Changed("approval") {
		approval.Mode = r.Approval.Mode
	}
	if r.Approval.MaxPriority != "" && !f.Changed("approval-max-priority") {
		approval.MaxPriority = r.Approval.MaxPriority
	}
	if r.Approval.Note != "" && !f.Changed("approval-note") {
		approval.Note = r.Approval.Note
	}
	return r, nil
}

func reviewTimeoutDefault(r config.Review) time.Duration {
	if r.Timeout != "" {
		if d, err := time.ParseDuration(r.Timeout); err == nil {
			return d
		}
	}
	return defaultReviewTimeout
}

// validatePRFlags rejects --post together with --no-post and (defense-in-depth)
// surfaces the post-without-token failure early in PreRunE by resolving the same
// precedence (--token > GITHUB_TOKEN > GH_TOKEN) runPRReview uses.
func validatePRFlags(post, noPost bool, token string) error {
	if post && noPost {
		return &CLIError{
			Code:    "flags.conflict",
			Message: "--post and --no-post are mutually exclusive",
			Hint:    "pass one or neither (default is dry-run)",
			Exit:    2,
		}
	}
	if post && resolveGitHubToken(token) == "" {
		return &CLIError{
			Code:    "github.post_requires_token",
			Message: "--post needs a GitHub token: pass --token or set GITHUB_TOKEN/GH_TOKEN",
			Hint:    "create a PAT with repo scope; dry-run (--no-post) needs no token for public repos",
			Exit:    2,
		}
	}
	return nil
}

func validateApprovalPolicy(a config.ApprovalPolicy) error {
	switch a.Mode {
	case "", "off", "clean", "threshold":
	default:
		return &CLIError{Code: "config.invalid", Message: "--approval must be off|clean|threshold", Exit: 2}
	}
	if a.MaxPriority != "" {
		switch a.MaxPriority {
		case "P0", "P1", "P2", "P3", "P4":
		default:
			return &CLIError{Code: "config.invalid", Message: "--approval-max-priority must be P0|P1|P2|P3|P4", Exit: 2}
		}
	}
	switch a.Note {
	case "", "none", "on_findings", "always":
	default:
		return &CLIError{Code: "config.invalid", Message: "--approval-note must be none|on_findings|always", Exit: 2}
	}
	return nil
}

// validateReviewFlags rejects more than one mode group and an unrecognized gate
// by delegating to the shared engine.ValidateInvocation contract, so the CLI and
// the MCP review_run boundary enforce identical rules. MarkFlagsRequiredTogether
// already pairs from/to; this catches every other invalid combo (no half-range,
// no staged+commit, no range+commit, at least one mode) and an out-of-set --gate.
func validateReviewFlags(staged bool, from, to, commit, gate string) error {
	return engine.ValidateInvocation(staged, from, to, commit, gate)
}

// validateMode rejects an out-of-set --mode; empty defaults to review.
func validateMode(mode string) error {
	switch mode {
	case "", "review", "checks":
		return nil
	}
	return &CLIError{
		Code:    "flags.invalid_mode",
		Message: fmt.Sprintf("unknown --mode %q", mode),
		Hint:    "use review (default) or checks",
		Exit:    2,
	}
}

// validateFilterMode rejects an out-of-set --filter-mode, delegating to the github
// enum so the CLI and the publish path enforce one source of truth.
func validateFilterMode(mode string) error {
	if mode == "" || ghub.ValidFilterMode(mode) {
		return nil
	}
	return &CLIError{
		Code:    "flags.invalid_filter_mode",
		Message: fmt.Sprintf("unknown --filter-mode %q", mode),
		Hint:    "use added, diff_context, file, or nofilter",
		Exit:    2,
	}
}

// validateMinSeverity rejects an out-of-set --min-severity (empty keeps current
// behavior), delegating to the github enum so the CLI and the publish floor share
// one source of truth. Mirrors validateFilterMode's flags.* namespace.
func validateMinSeverity(sev string) error {
	if sev == "" || ghub.ValidMinSeverity(sev) {
		return nil
	}
	return &CLIError{
		Code:    "flags.invalid_min_severity",
		Message: fmt.Sprintf("unknown --min-severity %q", sev),
		Hint:    "use none, info, low, medium, high, or critical",
		Exit:    2,
	}
}

// validateFormat rejects an out-of-set --format (empty = full), delegating to the
// github registry so the CLI and the renderer share one source of truth.
func validateFormat(format string) error {
	if ghub.ValidFormat(format) {
		return nil
	}
	return &CLIError{
		Code:    "flags.invalid_format",
		Message: fmt.Sprintf("unknown --format %q", format),
		Hint:    "use " + strings.Join(ghub.ModeNames(), ", "),
		Exit:    2,
	}
}

// emitReview writes the review result in the resolved --output format: json (the
// miucr.cli/v1 envelope, default + unchanged), pretty (the local terminal
// reporter), or sarif (a SARIF 2.1.0 document). SARIF/pretty are review-only
// formats; the JSON envelope stays the primary, byte-for-byte-stable contract.
func emitReview(w io.Writer, out ReviewOutcome, data, summary map[string]any) error {
	switch outputFormat {
	case "sarif":
		return sarif.EmitSARIFWithLinks(w, toSARIFFindings(out.Findings), versionString(), categoryURLs())
	case "pretty":
		return renderReviewTable(w, out)
	default:
		return writeSuccess(w, "review", "review.result", data, summary)
	}
}

// writeSARIFOut atomically writes findings as a SARIF 2.1.0 document to path so a
// caller can upload it (e.g. github/codeql-action/upload-sarif) alongside the
// normal --output/posting from the SAME single review run. Empty path is a no-op
// (the default). It writes to a temp file in the target dir and renames on
// success, so a failed/partial write never leaves a broken file, and callers
// invoke it only after the review succeeded, so a review error leaves no file.
func writeSARIFOut(path string, findings []ReviewFinding) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	fail := func(err error) error {
		return &CLIError{Code: "sarif.write_failed", Message: fmt.Sprintf("write SARIF to %q: %v", path, err), Exit: 1}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".miucr-sarif-*.tmp")
	if err != nil {
		return fail(err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed; cleans up on any error path
	if err := sarif.EmitSARIFWithLinks(tmp, toSARIFFindings(findings), versionString(), categoryURLs()); err != nil {
		tmp.Close()
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fail(err)
	}
	return nil
}

// categoryURLs loads the validated [review].category_urls map from TRUSTED config
// (user file + built-in defaults, never repo rules) for the local SARIF emit path.
// A config-load error degrades to no links (the map is presentation-only).
func categoryURLs() map[string]string {
	cfg, err := config.Load()
	if err != nil {
		slog.Warn("review: config load failed; category links disabled", "error", config.RedactString(err.Error()))
	}
	return cfg.Review.CategoryURLMap()
}

// toSARIFFindings maps cli findings to the sarif leaf-package input shape.
func toSARIFFindings(in []ReviewFinding) []sarif.Finding {
	out := make([]sarif.Finding, 0, len(in))
	for _, f := range in {
		out = append(out, sarif.Finding{
			File:           f.File,
			Line:           f.Line,
			EndLine:        f.EndLine,
			Severity:       f.Severity,
			Category:       f.Category,
			Rationale:      f.Rationale,
			SuggestedPatch: f.SuggestedPatch,
			QuotedCode:     f.QuotedCode,
		})
	}
	return out
}
