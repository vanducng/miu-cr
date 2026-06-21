package cli

import (
	stdctx "context"
	"fmt"
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

// ReviewOutcome is the Reviewer's result: anchored findings plus run stats.
type ReviewOutcome struct {
	Findings []ReviewFinding
	Stats    map[string]any
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
	)

	cmd := &cobra.Command{
		Use:   "review",
		Short: "Review local git changes and emit gated findings",
		PreRunE: func(cmd *cobra.Command, args []string) error {
			return validateReviewFlags(staged, from, to, commit, gate)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
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
	f.StringVar(&apiKey, "api-key", "", "API key (overrides ANTHROPIC_API_KEY/OPENAI_API_KEY; never persisted)")
	f.StringVar(&baseURL, "base-url", "", "Override provider base URL (e.g. an Anthropic-compatible gateway; never persisted)")
	f.StringVar(&authToken, "auth-token", "", "Bearer auth token for Anthropic-compatible gateways, Anthropic only (never persisted)")
	f.StringVar(&model, "model", "", "Override the review model (else ANTHROPIC_MODEL/OPENAI_MODEL or pinned default)")
	f.IntVar(&expand, "expand", 5, "Context lines added above/below each hunk in the new-content window (0 disables)")
	f.IntVar(&tokenBudget, "token-budget", 0, "Approximate token budget; over budget degrades context (0 disables)")

	cmd.MarkFlagsRequiredTogether("from", "to")
	return cmd
}

// validateReviewFlags rejects more than one mode group and an unrecognized gate
// by delegating to the shared engine.ValidateInvocation contract, so the CLI and
// the MCP review_run boundary enforce identical rules. MarkFlagsRequiredTogether
// already pairs from/to; this catches every other invalid combo (no half-range,
// no staged+commit, no range+commit, at least one mode) and an out-of-set --gate.
func validateReviewFlags(staged bool, from, to, commit, gate string) error {
	return engine.ValidateInvocation(staged, from, to, commit, gate)
}
