package cli

import (
	stdctx "context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine"
	ghub "github.com/vanducng/miu-cr/internal/github"
	"github.com/vanducng/miu-cr/internal/serve"
	"github.com/vanducng/miu-cr/internal/store"
)

type options struct {
	output  string
	timeout time.Duration
}

// reviewStoreFactory opens the review store for the REST API, returning the store
// (the serve.ReviewStore subset), a closer, and an error. Injected from the wire
// package (SetReviewStoreFactory) so cli stays below store in the import graph,
// mirroring SetReviewer. Nil when no wiring ran (tests) → serve runs without REST.
var reviewStoreFactory func(ctx stdctx.Context) (serve.ReviewStore, func(), error)

// SetReviewStoreFactory wires the store opener used by `serve` for the opt-in
// REST API. Called once from wire.init before any command runs.
func SetReviewStoreFactory(f func(ctx stdctx.Context) (serve.ReviewStore, func(), error)) {
	reviewStoreFactory = f
}

// historyStoreFactory opens the full review store for the `history` command group
// (list/show/prune). Injected from the wire package so cli stays below store in
// the import graph; nil when no wiring ran (tests inject their own).
var historyStoreFactory func(ctx stdctx.Context) (store.Store, func(), error)

// SetHistoryStoreFactory wires the store opener used by `history`. Called once
// from wire.init before any command runs.
func SetHistoryStoreFactory(f func(ctx stdctx.Context) (store.Store, func(), error)) {
	historyStoreFactory = f
}

var version = "v0.23.0" // x-release-please-version

// Execute runs the miucr root command with args, returning a CLIError whose Exit
// code the caller (cmd/miucr) maps to the process status.
func Execute(args []string) error {
	opts := &options{output: "json", timeout: 30 * time.Second}
	root := rootCommand(opts)
	root.SetArgs(args)
	root.SilenceUsage = true
	root.SilenceErrors = true
	ctx, stop := signal.NotifyContext(stdctx.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := root.ExecuteContext(ctx); err != nil {
		var already *CLIError
		if errors.As(err, &already) && already.AlreadyWritten {
			return err // command emitted its own envelope; just carry the exit code
		}
		errorWriter := io.Writer(os.Stdout)
		if isMCPServeCommand(args) {
			errorWriter = os.Stderr // keep the JSON-RPC stream on stdout clean
		}
		_ = writeError(errorWriter, commandPath(args), err)
		return err
	}
	return nil
}

func rootCommand(opts *options) *cobra.Command {
	root := &cobra.Command{
		Use:   "miucr",
		Short: "Owned local AI code-review CLI for agents",
	}
	root.PersistentFlags().StringVarP(&opts.output, "output", "o", "json", "Output format: json, pretty, or sarif (sarif is review-only)")
	root.PersistentFlags().DurationVar(&opts.timeout, "timeout", 30*time.Second, "Operation timeout")
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		switch opts.output {
		case "", "json":
			outputFormat, prettyOutput = "json", false
		case "pretty":
			outputFormat, prettyOutput = "pretty", true
		case "sarif":
			// SARIF is its own document handled only by review; prettyOutput stays
			// off so every non-review command still emits the JSON envelope.
			outputFormat, prettyOutput = "sarif", false
		default:
			return &CLIError{Code: "output.invalid_format", Message: fmt.Sprintf("unknown output format %q", opts.output), Hint: "use json, pretty, or sarif", Exit: 2}
		}
		return nil
	}
	root.AddCommand(initCommand(opts))
	root.AddCommand(loginCommand(opts))
	root.AddCommand(upgradeCommand(opts))
	root.AddCommand(versionCommand())
	root.AddCommand(reviewCommand(opts))
	root.AddCommand(mcpCommand(opts))
	root.AddCommand(serveCommand(opts))
	root.AddCommand(rulesCommand(opts))
	root.AddCommand(historyCommand(opts))
	return root
}

// serveCommand runs the webhook daemon. It fails fast on misconfiguration: an
// empty WEBHOOK_SECRET accepts forged webhooks (serve.secret_required), no token
// can't post or clone (serve.token_required), and an empty allowlist would deny
// everything so --repos is required. Each job's reviewFn delegates to the
// in-process M2 path via ReviewPRForServe (Post:true); all serve-side errors are
// routed through config.RedactString inside the serve package.
func serveCommand(opts *options) *cobra.Command {
	var (
		addr         string
		gate         string
		repos        []string
		poll         bool
		pollInterval time.Duration
		pollSource   string
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the HMAC webhook daemon (default) and/or the opt-in poll trigger",
		RunE: func(cmd *cobra.Command, args []string) error {
			secret := strings.TrimSpace(os.Getenv("WEBHOOK_SECRET"))
			// Webhook is the default; --poll without a secret runs poll-only,
			// bypassing the secret requirement. Webhook (with or without poll)
			// still requires a secret so it never accepts forged payloads.
			if secret == "" && !poll {
				return &CLIError{
					Code:    "serve.secret_required",
					Message: "WEBHOOK_SECRET is required: an empty secret would accept forged webhooks",
					Hint:    "set WEBHOOK_SECRET, or pass --poll to run the poll-only trigger",
					Exit:    2,
				}
			}
			cfg, cfgErr := config.Load()
			if cfgErr != nil {
				// Load() returns Defaults() on error, so we proceed — but log it
				// (redacted) so a malformed config (e.g. [github] mode=app) isn't
				// silently degraded to the PAT default with a confusing downstream error.
				slog.Default().Warn("serve: config load failed; using defaults", "error", config.RedactString(cfgErr.Error()))
			}
			appMode := strings.EqualFold(strings.TrimSpace(cfg.Github.Mode), "app")
			token := resolveGitHubToken("")
			if token == "" && !appMode {
				return &CLIError{
					Code:    "serve.token_required",
					Message: "a GitHub token is required: set GITHUB_TOKEN or GH_TOKEN",
					Hint:    "create a PAT with repo scope so serve can clone and post reviews",
					Exit:    2,
				}
			}
			if len(repos) == 0 {
				return &CLIError{
					Code:    "serve.repos_required",
					Message: "--repos is required: an empty allowlist reviews nothing",
					Hint:    "pass --repos owner/repo[,owner/repo...] to allow specific repositories",
					Exit:    2,
				}
			}
			if poll {
				switch pollSource {
				case "", "notifications", "pulls":
				default:
					return &CLIError{
						Code:    "serve.poll_source_invalid",
						Message: fmt.Sprintf("unknown --poll-source %q", pollSource),
						Hint:    "use notifications (default) or pulls",
						Exit:    2,
					}
				}
			}

			log := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: slog.LevelInfo}))
			reviewTO := 15 * time.Minute

			tokenSource, err := buildTokenSource(cfg.Github)
			if err != nil {
				return err
			}
			// resolveToken keeps func()(string,error): it captures the daemon ctx
			// (cmd.Context()) and derives a short bounded timeout for the App-token
			// exchange, so a mint isn't tied to one HTTP request's lifetime. In PAT
			// mode the static source returns the PAT unchanged (no network).
			daemonCtx := cmd.Context()
			resolveToken := func() (string, error) {
				tctx, cancel := stdctx.WithTimeout(daemonCtx, 30*time.Second)
				defer cancel()
				return tokenSource.Token(tctx)
			}

			// The REST API is opt-in: enabled ONLY when MIUCR_API_TOKEN (env-only,
			// like WEBHOOK_SECRET — never a flag) is set. When enabled, open the
			// review store once (shared by the POST pending-persist, the GET read,
			// and the worker's final-record upsert).
			apiToken := strings.TrimSpace(os.Getenv("MIUCR_API_TOKEN"))
			var reviewStore serve.ReviewStore
			if apiToken != "" {
				if reviewStoreFactory == nil {
					return &CLIError{Code: "serve.store_unwired", Message: "MIUCR_API_TOKEN is set but the review store is not wired", Exit: 1}
				}
				st, closeStore, err := reviewStoreFactory(cmd.Context())
				if err != nil {
					return err
				}
				defer closeStore()
				reviewStore = st
			}
			reviewFn := buildServeReviewFn(log, gate, reviewStore)

			pollCfg := func(disp serve.Dispatcher) serve.PollConfig {
				return serve.PollConfig{
					Source:       serve.ParsePollSource(pollSource),
					Repos:        repos,
					Interval:     pollInterval,
					ResolveToken: resolveToken,
					Dispatcher:   disp,
					Logger:       log,
					ReviewTO:     reviewTO,
				}
			}

			// Poll-only: no webhook secret → build the Pool + Poller directly,
			// bypassing serve.New's secret requirement. RunPoll is the sole Drainer.
			if poll && secret == "" {
				pool := serve.NewPool(reviewFn, log)
				poller := serve.NewPoller(pollCfg(pool), serve.NewNotifGetter(token))
				return serve.RunPoll(cmd.Context(), pool, poller)
			}

			// Webhook (+ optional poll). Server.Run is the SOLE Drainer. The REST
			// /v1 routes are registered only when APIToken + ReviewStore are set.
			srv, pool, err := serve.New(serve.Config{
				Addr:          addr,
				Secret:        []byte(secret),
				Repos:         repos,
				ResolveToken:  resolveToken,
				Logger:        log,
				ReviewTimeout: reviewTO,
				APIToken:      []byte(apiToken),
				ReviewStore:   reviewStore,
			}, reviewFn)
			if err != nil {
				return &CLIError{Code: "config.invalid", Message: err.Error(), Hint: "check the serve configuration (secret, token, repos)", Exit: 2}
			}
			if !poll {
				return srv.Run(cmd.Context(), pool)
			}

			// Webhook + poll share one ctx under an errgroup. The poller dispatches
			// to the SAME pool; it never Drains (Server.Run drains exactly once).
			poller := serve.NewPoller(pollCfg(pool), serve.NewNotifGetter(token))
			g, gctx := errgroup.WithContext(cmd.Context())
			g.Go(func() error { return srv.Run(gctx, pool) })
			// poller.Run only exits on ctx cancel; persistent API failures back off
			// and retry forever (never cancel the group). Surfacing a wedged poller
			// as fatal is a deferred nicety.
			g.Go(func() error { poller.Run(gctx); return nil })
			return g.Wait()
		},
	}
	cmd.Flags().StringVar(&addr, "addr", ":8080", "Listen address for the webhook server")
	cmd.Flags().StringVar(&gate, "gate", "high", "Publish-severity gate for posted reviews (publish-only; never affects serve liveness)")
	cmd.Flags().StringSliceVar(&repos, "repos", nil, "Required owner/repo allowlist (comma-separated); webhooks for other repos are ignored")
	cmd.Flags().BoolVar(&poll, "poll", false, "Opt-in poll trigger: periodically ask GitHub for PRs needing review (webhook stays the default)")
	cmd.Flags().DurationVar(&pollInterval, "poll-interval", 60*time.Second, "Poll interval floor (effective = max(this, X-Poll-Interval))")
	cmd.Flags().StringVar(&pollSource, "poll-source", "notifications", "Poll candidate source: notifications (default) or pulls")
	return cmd
}

// buildTokenSource builds the GitHub TokenSource from [github]. Default (mode=pat
// or unset) → staticTokenSource carrying the resolved PAT (or "" anonymous), the
// pre-M8 behavior byte-for-byte. mode=app → appTokenSource (requires app_id, a
// numeric installation_id, and a readable private_key_path); the key is read,
// parsed, and zeroed inside the github package.
func buildTokenSource(g config.Github) (ghub.TokenSource, error) {
	if !strings.EqualFold(strings.TrimSpace(g.Mode), "app") {
		return ghub.NewStaticTokenSource(resolveGitHubToken("")), nil
	}
	if strings.TrimSpace(g.AppID) == "" || strings.TrimSpace(g.PrivateKeyPath) == "" {
		return nil, &CLIError{
			Code:    "serve.app_config_required",
			Message: "[github] mode=app requires app_id and private_key_path",
			Hint:    "set app_id, installation_id, and private_key_path under [github] in config.toml",
			Exit:    2,
		}
	}
	installID, err := strconv.ParseInt(strings.TrimSpace(g.InstallationID), 10, 64)
	if err != nil || installID <= 0 {
		return nil, &CLIError{
			Code:    "serve.app_installation_invalid",
			Message: fmt.Sprintf("[github] installation_id must be a positive integer, got %q", g.InstallationID),
			Hint:    "use the numeric installation id from the App's installation URL",
			Exit:    2,
		}
	}
	key, err := ghub.ReadPrivateKeyFile(strings.TrimSpace(g.PrivateKeyPath))
	if err != nil {
		return nil, &CLIError{
			Code:    "serve.app_key_unreadable",
			Message: config.RedactString(err.Error()),
			Hint:    "private_key_path must point to a readable RSA PEM (PKCS#1 or PKCS#8)",
			Exit:    2,
		}
	}
	return ghub.NewAppTokenSource(strings.TrimSpace(g.AppID), installID, key, ghub.NewAppExchanger(), nil), nil
}

// buildServeReviewFn returns the reviewFn shared by webhook + poll dispatch. It
// delegates to the in-process M2 path via ReviewPRForServe (Post:true); all
// errors are redacted. The job runs detached from cmd.Context() so graceful
// drain can finish in-flight reviews; j.Timeout still bounds each job. A non-nil
// return reaches Job.OnDone so the poller leaves a failed head retryable next tick.
//
// When j.ReviewID is set (REST-initiated) and st is non-nil, reviewFn persists the
// FINAL record under that id: done (with the returned outcome's findings/stats/
// HeadSHA) on success, failed on error. The findings/stats/HeadSHA live in the
// RETURNED cli.ReviewOutcome — not in Job.OnDone(error) — so the upsert rides here,
// inside reviewFn, not in OnDone. The webhook/poll paths leave ReviewID empty and
// skip the upsert (byte-for-byte unchanged).
func buildServeReviewFn(log *slog.Logger, gate string, st serve.ReviewStore) func(serve.Job) error {
	return func(j serve.Job) error {
		jobCtx := stdctx.Background()
		if j.Timeout > 0 {
			var cancel stdctx.CancelFunc
			jobCtx, cancel = stdctx.WithTimeout(jobCtx, j.Timeout)
			defer cancel()
		}
		out, err := ReviewPRForServe(jobCtx, PRReviewRequest{
			Ref:   j.Ref,
			Token: j.Token,
			Post:  true,
			// serve inherits both opt-in write-actions OFF: a webhook/poll-driven
			// daemon must not auto-suggest or auto-approve by default.
			Suggest:      false,
			ApproveClean: false,
			Gate:         gate,
			Timeout:      j.Timeout,
		})
		if err != nil {
			log.Error("review failed", "ref", j.Ref, "err", config.RedactString(err.Error()))
			persistFinalReview(log, st, j.ReviewID, "failed", ReviewOutcome{})
			return err
		}
		posted, action := 0, "none"
		if out.PR != nil {
			posted, action = out.PR.PostedInline, out.PR.SummaryAction
		}
		log.Info("review done", "ref", j.Ref, "findings", len(out.Findings), "posted_inline", posted, "summary", action)
		persistFinalReview(log, st, j.ReviewID, "done", out)
		return nil
	}
}

// persistFinalReview upserts the terminal REST record under id. A no-op when id
// is empty (webhook/poll) or st is nil (REST disabled). Best-effort: a store
// failure is logged (redacted), never returned, so it can't fail the review.
//
// It derives a FRESH bounded context rather than reusing the job's ctx: a review
// that fails by TIMEOUT (the common case) leaves jobCtx already canceled, so a
// write on it would fail and strand the record at pending forever. The detached
// ctx guarantees the terminal status is recorded regardless of why the job ended.
func persistFinalReview(log *slog.Logger, st serve.ReviewStore, id, status string, out ReviewOutcome) {
	if id == "" || st == nil {
		return
	}
	ctx, cancel := stdctx.WithTimeout(stdctx.Background(), 10*time.Second)
	defer cancel()
	headSHA := ""
	if out.PR != nil {
		headSHA = out.PR.HeadSHA
	}
	if _, err := st.UpsertReview(ctx, store.ReviewRecord{
		ID:       id,
		Mode:     "pr",
		HeadSHA:  headSHA,
		Status:   status,
		Findings: serveFindingsToEngine(out.Findings),
		Stats:    out.Stats,
	}); err != nil {
		log.Error("rest: persist final review failed", "id", id, "status", status, "err", config.RedactString(err.Error()))
	}
}

// serveFindingsToEngine maps the cli finding shape to engine.Finding (identical
// fields) for store persistence.
func serveFindingsToEngine(in []ReviewFinding) []engine.Finding {
	out := make([]engine.Finding, 0, len(in))
	for _, f := range in {
		out = append(out, engine.Finding{
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

// MCPRequest carries the resolved serve options to the injected MCPServer.
type MCPRequest struct {
	Transport string
	Version   string
	Timeout   time.Duration
	In        io.Reader
	Out       io.Writer
	Err       io.Writer
}

// MCPServer serves the review engine over MCP. The engine-backed implementation
// is injected at startup (internal/cli/wire) so cli stays below engine/store in
// the import graph.
type MCPServer interface {
	Serve(ctx stdctx.Context, req MCPRequest) error
}

var mcpServer MCPServer

// SetMCPServer wires the engine-backed MCP server. Called once from the wire
// package's init before any command runs.
func SetMCPServer(s MCPServer) { mcpServer = s }

func mcpCommand(opts *options) *cobra.Command {
	var transport string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the review engine over MCP on stdio",
		RunE: func(cmd *cobra.Command, args []string) error {
			if mcpServer == nil {
				return &CLIError{Code: "mcp.not_wired", Message: "MCP server not wired", Exit: 1}
			}
			return mcpServer.Serve(cmd.Context(), MCPRequest{
				Transport: transport,
				Version:   versionString(),
				Timeout:   opts.timeout,
				In:        cmd.InOrStdin(),
				Out:       cmd.OutOrStdout(),
				Err:       cmd.ErrOrStderr(),
			})
		},
	}
	cmd.Flags().StringVar(&transport, "transport", "stdio", "MCP transport: stdio")
	return cmd
}

func isMCPServeCommand(args []string) bool {
	for _, a := range args {
		if a == "mcp" {
			return true
		}
		if !strings.HasPrefix(a, "-") {
			// first non-flag arg is the subcommand; stop after it.
			return a == "mcp"
		}
	}
	return false
}

func versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		RunE: func(cmd *cobra.Command, args []string) error {
			return writeSuccess(cmd.OutOrStdout(), "version", "version", map[string]any{"version": versionString()}, nil)
		},
	}
}

func versionString() string {
	if version != "" && !strings.HasSuffix(version, "-dev") {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

func commandPath(args []string) string {
	if len(args) == 0 {
		return "miucr"
	}
	if len(args) > 2 {
		args = args[:2]
	}
	return strings.Join(args, " ")
}

func init() {
	cobra.EnableCommandSorting = false
}
