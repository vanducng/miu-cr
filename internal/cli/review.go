package cli

import (
	stdctx "context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/vanducng/miu-cr/internal/engine"
)

// ReviewRequest is the mode-agnostic review invocation passed to the injected
// Reviewer. It mirrors engine.Request but lives in cli so the engine (which
// transitively imports cli for CLIError) is not imported here, avoiding a cycle.
type ReviewRequest struct {
	Staged       bool
	From         string
	To           string
	Commit       string
	Gate         string
	RepoDir      string
	IncludeGlobs []string
	ExcludeGlobs []string
	Extensions   []string
	Provider     string
	APIKey       string
	BaseURL      string
	AuthToken    string
	Model        string
	Timeout      time.Duration
	ExpandWindow int
	TokenBudget  int
}

// ReviewOutcome is the Reviewer's result: anchored findings plus run stats. PR
// is non-nil only on the --pr path and drives the data.pr envelope block.
type ReviewOutcome struct {
	Findings []ReviewFinding
	Stats    map[string]any
	PR       *PRResult
}

// PRResult is the typed PR summary for the data.pr envelope block on the --pr
// path. The token is never carried here (or anywhere in the envelope).
// PostedInline is the count of inline comments posted THIS run (0 on --no-post
// and on re-runs where everything was already posted); SummaryAction is
// created|edited on --post, "none" on --no-post.
type PRResult struct {
	Owner         string `json:"owner"`
	Repo          string `json:"repo"`
	Number        int    `json:"number"`
	HeadSHA       string `json:"head_sha"`
	IsFork        bool   `json:"is_fork"`
	Posted        bool   `json:"posted"`
	PostedInline  int    `json:"posted_inline"`
	SummaryAction string `json:"summary_action"`
}

// ReviewFinding is a single anchored finding rendered/serialized by cli.
type ReviewFinding struct {
	File           string `json:"file"`
	Line           int    `json:"line"`
	EndLine        int    `json:"end_line"`
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
	Ref       string
	Token     string
	Post      bool
	Gate      string
	Provider  string
	APIKey    string
	BaseURL   string
	AuthToken string
	Model     string
	Timeout   time.Duration

	IncludeGlobs []string
	ExcludeGlobs []string
	Extensions   []string
	ExpandWindow int
	TokenBudget  int
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

// SetPRReviewer wires the github-backed PR reviewer. Called once from wire.init.
func SetPRReviewer(r PRReviewer) { prReviewer = r }

// ReviewPRForServe is the in-process seam serve calls: it delegates straight to
// the wired prReviewer.ReviewPR (NOT runPRReview) so the gate_failed exit path is
// bypassed — serve's gate governs publish severity only, never worker liveness.
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

func reviewCommand(opts *options) *cobra.Command {
	var (
		staged      bool
		from        string
		to          string
		commit      string
		gate        string
		repoDir     string
		include     []string
		exclude     []string
		exts        []string
		provider    string
		apiKey      string
		baseURL     string
		authToken   string
		model       string
		expand      int
		tokenBudget int
		pr          string
		token       string
		post        bool
		noPost      bool
	)

	cmd := &cobra.Command{
		Use:   "review",
		Short: "Review local git changes and emit gated findings",
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if pr != "" {
				return validatePRFlags(post, noPost, token)
			}
			return validateReviewFlags(staged, from, to, commit, gate)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if pr != "" {
				return runPRReview(cmd, prRunArgs{
					ref:         pr,
					token:       token,
					post:        post && !noPost,
					gate:        gate,
					provider:    provider,
					apiKey:      apiKey,
					baseURL:     baseURL,
					authToken:   authToken,
					model:       model,
					timeout:     opts.timeout,
					include:     include,
					exclude:     exclude,
					exts:        exts,
					expand:      expand,
					tokenBudget: tokenBudget,
				})
			}
			if reviewer == nil {
				return &CLIError{Code: "review.not_wired", Message: "review engine not wired", Exit: 1}
			}
			req := ReviewRequest{
				Staged:       staged,
				From:         from,
				To:           to,
				Commit:       commit,
				Gate:         gate,
				RepoDir:      repoDir,
				IncludeGlobs: include,
				ExcludeGlobs: exclude,
				Extensions:   exts,
				Provider:     provider,
				APIKey:       apiKey,
				BaseURL:      baseURL,
				AuthToken:    authToken,
				Model:        model,
				Timeout:      opts.timeout,
				ExpandWindow: expand,
				TokenBudget:  tokenBudget,
			}
			ctx := cmd.Context()
			if opts.timeout > 0 {
				var cancel stdctx.CancelFunc
				ctx, cancel = stdctx.WithTimeout(ctx, opts.timeout)
				defer cancel()
			}
			out, err := reviewer.Review(ctx, req)
			if err != nil {
				return err
			}
			summary := map[string]any{
				"findings": len(out.Findings),
				"gate":     gate,
			}
			data := map[string]any{
				"findings": out.Findings,
				"stats":    out.Stats,
			}
			if prettyOutput {
				if err := renderReviewTable(cmd.OutOrStdout(), out); err != nil {
					return err
				}
			} else if err := writeSuccess(cmd.OutOrStdout(), "review", "review.result", data, summary); err != nil {
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
	f.IntVar(&tokenBudget, "token-budget", 0, "Approximate token budget; over budget degrades context (0 disables)")
	f.StringVar(&pr, "pr", "", "Review a GitHub PR: https://github.com/owner/repo/pull/N or owner/repo#N (no GitHub PAT needed for public repos in dry-run)")
	f.StringVar(&token, "token", "", "GitHub PAT (overrides GITHUB_TOKEN/GH_TOKEN; required only for --post; never persisted)")
	f.BoolVar(&post, "post", false, "Publish inline comments + a summary to the PR (requires a token)")
	f.BoolVar(&noPost, "no-post", false, "Dry-run the PR review without posting (default for --pr)")

	cmd.MarkFlagsRequiredTogether("from", "to")
	return cmd
}

// prRunArgs bundles the --pr invocation values RunE forwards to runPRReview.
type prRunArgs struct {
	ref       string
	token     string
	post      bool
	gate      string
	provider  string
	apiKey    string
	baseURL   string
	authToken string
	model     string
	timeout   time.Duration

	include     []string
	exclude     []string
	exts        []string
	expand      int
	tokenBudget int
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
		Ref:          a.ref,
		Token:        ghToken,
		Post:         a.post,
		Gate:         a.gate,
		Provider:     a.provider,
		APIKey:       a.apiKey,
		BaseURL:      a.baseURL,
		AuthToken:    a.authToken,
		Model:        a.model,
		Timeout:      a.timeout,
		IncludeGlobs: a.include,
		ExcludeGlobs: a.exclude,
		Extensions:   a.exts,
		ExpandWindow: a.expand,
		TokenBudget:  a.tokenBudget,
	})
	if err != nil {
		return err
	}

	summary := map[string]any{"findings": len(out.Findings), "gate": a.gate}
	data := map[string]any{"findings": out.Findings, "stats": out.Stats}
	if out.PR != nil {
		data["pr"] = out.PR
	}
	if prettyOutput {
		if err := renderReviewTable(cmd.OutOrStdout(), out); err != nil {
			return err
		}
	} else if err := writeSuccess(cmd.OutOrStdout(), "review", "review.result", data, summary); err != nil {
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

// validateReviewFlags rejects more than one mode group and an unrecognized gate
// by delegating to the shared engine.ValidateInvocation contract, so the CLI and
// the MCP review_run boundary enforce identical rules. MarkFlagsRequiredTogether
// already pairs from/to; this catches every other invalid combo (no half-range,
// no staged+commit, no range+commit, at least one mode) and an out-of-set --gate.
func validateReviewFlags(staged bool, from, to, commit, gate string) error {
	return engine.ValidateInvocation(staged, from, to, commit, gate)
}
